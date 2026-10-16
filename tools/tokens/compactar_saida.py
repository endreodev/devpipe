#!/usr/bin/env python3
"""Executa um comando de build/teste e imprime a saída COMPACTADA, preservando o
código de saída. Use para reduzir os tokens que a saída de comandos consome.

Onde usar no DevPipe:
  * "Comandos liberados ao agente" do projeto: libere o prefixo
        python tools/tokens/compactar_saida.py
    e oriente nas regras do projeto: "rode build/testes via
    python tools/tokens/compactar_saida.py -- <comando>". Cada saída de comando
    que o agente lê fica no histórico e é reenviada em todos os passos
    seguintes — compactar aqui economiza de forma multiplicativa.
  * Comandos de build/teste do projeto (validação oficial): opcional, pois o
    DevPipe já compacta a saída antes de enviá-la à correção.

Exemplos:
  python compactar_saida.py -- dotnet build -v q
  python compactar_saida.py --max 4000 -- npm test
  dotnet test 2>&1 | python compactar_saida.py      (modo filtro; exit code do pipe é o do python)
"""
import argparse
import subprocess
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _comum import compactar_log, estimar_tokens  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--max", type=int, default=6000, help="tamanho máximo da saída em caracteres (padrão 6000)")
    ap.add_argument("--stats", action="store_true", help="mostrar no stderr a economia obtida")
    ap.add_argument("comando", nargs=argparse.REMAINDER, help="comando a executar (após --)")
    a = ap.parse_args()
    cmd = a.comando[1:] if a.comando[:1] == ["--"] else a.comando

    if cmd:
        p = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                           shell=(os.name == "nt"))  # Windows: permite .cmd/.bat (npm, dotnet)
        bruto = p.stdout.decode("utf-8", errors="replace")
        code = p.returncode
    else:
        bruto = sys.stdin.buffer.read().decode("utf-8", errors="replace")
        code = 0

    curto = compactar_log(bruto, a.max)
    sys.stdout.write(curto + ("\n" if curto else ""))
    if cmd:
        sys.stdout.write(f"[código de saída: {code}]\n")
    if a.stats:
        t0, t1 = estimar_tokens(bruto), estimar_tokens(curto)
        pct = 100 * (1 - t1 / t0) if t0 else 0
        sys.stderr.write(f"[compactar_saida] ~{t0} -> ~{t1} tokens ({pct:.0f}% menos)\n")
    return code


if __name__ == "__main__":
    sys.exit(main())
