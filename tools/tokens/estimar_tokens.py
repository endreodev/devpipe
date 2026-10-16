#!/usr/bin/env python3
"""Estima quantos tokens arquivos e pastas custariam no contexto do agente e
aponta os maiores. Use para decidir o que enxugar (arquivos de padrões,
anexos) e para escrever demandas citando só os arquivos certos.

Uso:
  python estimar_tokens.py docs/padroes.md anexos/
  python estimar_tokens.py C:\\repos\\meu-projeto --top 30
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _comum import estimar_tokens  # noqa: E402

IGNORAR = {".git", "node_modules", "bin", "obj", "dist", "build", ".vs", ".idea", "packages", "vendor", "__pycache__", ".venv"}
BINARIOS = {".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".zip", ".exe", ".dll", ".so", ".wasm", ".woff", ".woff2", ".ttf", ".db", ".pdb", ".lock"}


def arquivos(caminhos):
    for c in caminhos:
        if os.path.isfile(c):
            yield c
            continue
        for raiz, dirs, nomes in os.walk(c):
            dirs[:] = [d for d in dirs if d not in IGNORAR]
            for n in nomes:
                if os.path.splitext(n)[1].lower() not in BINARIOS:
                    yield os.path.join(raiz, n)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("caminhos", nargs="+")
    ap.add_argument("--top", type=int, default=20)
    a = ap.parse_args()
    itens = []
    for f in arquivos(a.caminhos):
        try:
            with open(f, encoding="utf-8", errors="replace") as h:
                itens.append((estimar_tokens(h.read()), f))
        except OSError:
            pass
    itens.sort(reverse=True)
    total = sum(t for t, _ in itens)
    print(f"{len(itens)} arquivo(s), ~{total:,} tokens no total\n".replace(",", ".").replace("(s). ", "(s), ", 1))
    for t, f in itens[:a.top]:
        print(f"{t:>9,}  {f}".replace(",", "."))
    if itens and itens[0][0] > 8000:
        print("\nDica: arquivos acima de ~8.000 tokens custam caro se forem lidos inteiros;"
              " cite trechos/funções específicas na demanda.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
