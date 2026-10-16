#!/usr/bin/env python3
"""Gera versões enxutas de arquivos de padrões/regras (Markdown/texto) para
apontar em "Arquivos de padrões" do projeto no DevPipe. Esses arquivos vão
INTEIROS em toda análise, desenvolvimento e correção — cada linha conta.

O que é removido (sem mudar o conteúdo das regras):
  comentários HTML, imagens e badges, sumário/índice (TOC), linhas
  decorativas (---, ===), espaços finais, linhas em branco repetidas e,
  opcionalmente (--sem-exemplos), blocos de código maiores que N linhas.

Uso:
  python enxugar_markdown.py docs/padroes.md                 -> docs/padroes.min.md
  python enxugar_markdown.py docs/*.md --sem-exemplos 15
  python enxugar_markdown.py docs/padroes.md --somente-relatorio
"""
import argparse
import glob
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _comum import compactar_texto, estimar_tokens  # noqa: E402

TOC_TITULO = re.compile(r"^#{1,6}\s*(sumário|índice|indice|table of contents|contents|toc)\s*$", re.I)


def enxugar(s: str, max_codigo: int = 0) -> str:
    s = re.sub(r"(?s)<!--.*?-->", "", s)
    s = re.sub(r"(?m)^\s*(\[!\[.*|<img [^\n]*>|<p align[^\n]*>|</p>)\s*$", "", s)
    s = re.sub(r"!\[[^\]]*\]\([^)]*\)", "", s)
    s = re.sub(r"(?m)^\s*([-*_=]\s*){3,}$", "", s)
    linhas, out, i = s.split("\n"), [], 0
    while i < len(linhas):
        l = linhas[i]
        if TOC_TITULO.match(l.strip()):  # pula o TOC até o próximo título
            i += 1
            while i < len(linhas) and not linhas[i].lstrip().startswith("#"):
                i += 1
            continue
        if max_codigo and l.lstrip().startswith("```"):
            j = i + 1
            while j < len(linhas) and not linhas[j].lstrip().startswith("```"):
                j += 1
            bloco = linhas[i:j + 1]
            if len(bloco) - 2 > max_codigo:
                out += bloco[:max_codigo + 1] + [f"// … ({len(bloco) - 2 - max_codigo} linhas de exemplo omitidas)", "```"]
            else:
                out += bloco
            i = j + 1
            continue
        out.append(l)
        i += 1
    return compactar_texto("\n".join(out)) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("arquivos", nargs="+")
    ap.add_argument("--sem-exemplos", type=int, default=0, metavar="N",
                    help="encurta blocos de código com mais de N linhas (0 = não mexe)")
    ap.add_argument("--somente-relatorio", action="store_true", help="não grava, só mostra a economia")
    a = ap.parse_args()
    total0 = total1 = 0
    for padrao in a.arquivos:
        for f in glob.glob(padrao) or [padrao]:
            if f.endswith(".min.md"):
                continue
            with open(f, encoding="utf-8", errors="replace") as h:
                orig = h.read()
            novo = enxugar(orig, a.sem_exemplos)
            t0, t1 = estimar_tokens(orig), estimar_tokens(novo)
            total0, total1 = total0 + t0, total1 + t1
            destino = re.sub(r"(\.\w+)?$", ".min.md", f, count=1)
            if not a.somente_relatorio:
                with open(destino, "w", encoding="utf-8", newline="\n") as h:
                    h.write(novo)
            print(f"{f}: ~{t0} -> ~{t1} tokens" + ("" if a.somente_relatorio else f"  => {destino}"))
    if total0:
        print(f"TOTAL: ~{total0} -> ~{total1} tokens ({100 * (1 - total1 / total0):.0f}% menos) por execução do agente")
    return 0


if __name__ == "__main__":
    sys.exit(main())
