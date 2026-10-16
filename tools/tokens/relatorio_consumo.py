#!/usr/bin/env python3
"""Relatório de consumo do DevPipe a partir do banco (devpipe.db).

Mostra, por demanda: custo equivalente (US$) informado pela CLI, quantas
leituras de arquivo, RELEITURAS (mesmo arquivo lido de novo = desperdício),
buscas, comandos, tentativas de correção e quanto a janela de 5h da
assinatura subiu durante a execução. Ajuda a achar onde o limite é gasto.

Uso:
  python relatorio_consumo.py                          (procura o banco no local padrão)
  python relatorio_consumo.py --db "%APPDATA%\\DevPipe\\devpipe.db" --dias 7
  python relatorio_consumo.py --demanda CH-1001        (detalha os arquivos mais lidos)

O banco é aberto somente para leitura.
"""
import argparse
import collections
import os
import re
import sqlite3
import sys
from datetime import datetime, timedelta, timezone


def banco_padrao():
    cands = []
    if os.environ.get("APPDATA"):
        cands.append(os.path.join(os.environ["APPDATA"], "DevPipe", "devpipe.db"))
    home = os.path.expanduser("~")
    cands += [os.path.join(home, "Library", "Application Support", "DevPipe", "devpipe.db"),
              os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.join(home, ".config")), "DevPipe", "devpipe.db"),
              os.path.join("DevPipe", "devpipe.db"), "devpipe.db"]
    for c in cands:
        if os.path.exists(c):
            return c
    return cands[0] if cands else "devpipe.db"


USO_RE = re.compile(r"janela 5h:\s*(\d+)%")


def no_periodo(valor, desde):
    """Aceita datas gravadas como texto ISO, Unix ou dia juliano."""
    if valor is None:
        return True
    if isinstance(valor, (int, float)):
        if valor > 1e9:  # Unix (s ou ms)
            valor = valor / 1000 if valor > 1e11 else valor
            return datetime.fromtimestamp(valor, timezone.utc).strftime("%Y-%m-%d") >= desde
        if valor > 2e6:  # dia juliano
            return datetime.fromtimestamp((valor - 2440587.5) * 86400, timezone.utc).strftime("%Y-%m-%d") >= desde
        return True
    return str(valor)[:10] >= desde


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--db", default=None, help="caminho do devpipe.db")
    ap.add_argument("--dias", type=int, default=30, help="período analisado (padrão 30 dias)")
    ap.add_argument("--demanda", help="ticket para detalhar arquivos mais lidos")
    a = ap.parse_args()
    db = os.path.expandvars(a.db) if a.db else banco_padrao()
    if not os.path.exists(db):
        print(f"Banco não encontrado: {db}\nInforme com --db (a pasta de dados aparece no log ao iniciar o DevPipe).")
        return 1
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    desde = (datetime.now(timezone.utc) - timedelta(days=a.dias)).strftime("%Y-%m-%d")

    custos = collections.defaultdict(float)
    for ticket, custo, inicio in con.execute(
            "SELECT d.ticket, COALESCE(r.cost_usd,0), r.started_at FROM runs r JOIN demands d ON d.id=r.demand_id"):
        if no_periodo(inicio, desde):
            custos[ticket] += custo

    st = collections.defaultdict(lambda: {"lidos": collections.Counter(), "buscas": 0, "cmds": 0,
                                          "correcoes": 0, "uso": [], "negadas": 0})
    for ticket, kind, msg, quando in con.execute(
            "SELECT ticket, kind, message, time FROM events WHERE kind IN "
            "('arquivo_lido','busca','comando','erro_validacao','permissao_negada','uso_plano') ORDER BY id"):
        if not no_periodo(quando, desde):
            continue
        s = st[ticket or "?"]
        if kind == "arquivo_lido":
            s["lidos"][msg.split(": ", 1)[-1]] += 1
        elif kind == "busca":
            s["buscas"] += 1
        elif kind == "comando" and msg.startswith("Comando do agente"):
            s["cmds"] += 1
        elif kind == "erro_validacao":
            s["correcoes"] += 1
        elif kind == "permissao_negada":
            s["negadas"] += 1
        elif kind == "uso_plano":
            m = USO_RE.search(msg)
            if m:
                s["uso"].append(int(m.group(1)))

    tickets = sorted(set(custos) | set(st), key=lambda t: -custos.get(t, 0))
    if not tickets:
        print("Nenhuma execução no período.")
        return 0
    print(f"Banco: {db}   Período: últimos {a.dias} dia(s)\n")
    cab = f"{'Demanda':<14}{'US$ eq.':>9}{'Leituras':>10}{'Releit.':>9}{'Buscas':>8}{'Cmds':>6}{'Correç.':>9}{'Negadas':>9}{'5h +%':>7}"
    print(cab + "\n" + "-" * len(cab))
    tot = collections.Counter()
    for t in tickets:
        s = st[t]
        leit = sum(s["lidos"].values())
        relei = sum(n - 1 for n in s["lidos"].values() if n > 1)
        uso = s["uso"]
        delta = (max(uso) - uso[0]) if len(uso) > 1 else 0
        print(f"{t[:13]:<14}{custos.get(t, 0):>9.2f}{leit:>10}{relei:>9}{s['buscas']:>8}{s['cmds']:>6}{s['correcoes']:>9}{s['negadas']:>9}{delta:>7}")
        tot.update(custo=custos.get(t, 0), leit=leit, relei=relei, corr=s["correcoes"], neg=s["negadas"])
    print("-" * len(cab))
    print(f"{'TOTAL':<14}{tot['custo']:>9.2f}{tot['leit']:>10}{tot['relei']:>9}{'':>8}{'':>6}{tot['corr']:>9}{tot['neg']:>9}")

    print("\nComo ler:")
    if tot["leit"] and tot["relei"] / max(tot["leit"], 1) > 0.2:
        print(f"- {tot['relei']} releituras ({100*tot['relei']//tot['leit']}% das leituras): cite arquivos/funções na demanda e"
              " mantenha a análise e o desenvolvimento na mesma sessão (padrão).")
    if tot["corr"]:
        print(f"- {tot['corr']} rodada(s) de correção: cada uma reenvia o contexto. Libere ao agente os comandos de build/teste"
              " (ele verifica antes) e reduza 'Tentativas de correção' se forem frequentes.")
    if tot["neg"]:
        print(f"- {tot['neg']} ação(ões) negada(s): o agente gasta passos tentando comandos não liberados; ajuste a lista de comandos.")
    print("- 'US$ eq.' é o equivalente informado pela CLI; na assinatura não é cobrado, mas é proporcional ao consumo do limite.")

    if a.demanda:
        s = st.get(a.demanda)
        if not s:
            print(f"\nDemanda {a.demanda} sem eventos no período.")
        else:
            print(f"\nArquivos mais lidos em {a.demanda}:")
            for f, n in s["lidos"].most_common(15):
                print(f"  {n:>3}×  {f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
