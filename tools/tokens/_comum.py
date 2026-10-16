"""Rotinas compartilhadas pelos scripts de economia de tokens do DevPipe.

Mantém a mesma lógica de compactação usada internamente pelo DevPipe
(internal/textx), para que o que você mede aqui seja o que o agente recebe.
Somente biblioteca padrão do Python 3.8+.
"""
import re

ANSI_RE = re.compile(r"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07")
ERR_RE = re.compile(
    r"(\berr(or|o)?\b|\bfail(ed|ure|ou|ha)?\b|\bfalh|exception|panic|traceback|assert|expected|"
    r"esperad|undefined|not found|não encontrad|cannot|unable|fatal|\bCS\d{4}\b|\bTS\d{4}\b|✗|✖|×\s)",
    re.IGNORECASE,
)
NOISE_RE = re.compile(
    r"^\s*(\d{1,3}%|\[=*>?\s*\]|downloading|downloaded|restoring|restored|determining projects|"
    r"resolving|fetching|progress|\.{3,}\s*$)",
    re.IGNORECASE,
)
INVISIVEIS = {"\u00a0": " ", "\u200b": "", "\u200c": "", "\u200d": "", "\ufeff": "", "\u00ad": ""}


def estimar_tokens(texto: str) -> int:
    """Estimativa grosseira (~3,5 caracteres por token em português/código)."""
    return (len(texto) * 2 + 6) // 7


def compactar_texto(s: str) -> str:
    """CRLF->LF, remove espaços finais e invisíveis, colapsa linhas em branco."""
    s = s.replace("\r\n", "\n").replace("\r", "\n")
    for k, v in INVISIVEIS.items():
        s = s.replace(k, v)
    s = "\n".join(l.rstrip(" \t") for l in s.split("\n"))
    s = re.sub(r"\n{3,}", "\n\n", s)
    return s.strip()


def compactar_log(s: str, maximo: int = 8000) -> str:
    """Remove ruído de build/teste e, se preciso, mantém só erros + contexto + final."""
    s = ANSI_RE.sub("", s).replace("\r\n", "\n")
    linhas, anterior, rep = [], None, 0
    for l in s.split("\n"):
        if "\r" in l:  # barra de progresso: vale o último estado
            l = l.rsplit("\r", 1)[1]
        l = l.rstrip(" \t")
        if not l or (NOISE_RE.search(l) and not ERR_RE.search(l)):
            continue
        if l == anterior:
            rep += 1
            continue
        if rep:
            linhas.append(f"    (linha anterior repetida {rep}×)")
            rep = 0
        linhas.append(l)
        anterior = l
    if rep:
        linhas.append(f"    (linha anterior repetida {rep}×)")
    out = "\n".join(linhas)
    if maximo <= 0 or len(out) <= maximo:
        return out

    cab, fim, ctx = 8, 40, 2
    manter = [False] * len(linhas)
    for i in range(min(cab, len(linhas))):
        manter[i] = True
    for i in range(max(0, len(linhas) - fim), len(linhas)):
        manter[i] = True
    for i, l in enumerate(linhas):
        if ERR_RE.search(l):
            for j in range(max(0, i - ctx), min(len(linhas), i + ctx + 1)):
                manter[j] = True
    res, pulou = [], 0
    for i, l in enumerate(linhas):
        if not manter[i]:
            pulou += 1
            continue
        if pulou:
            res.append(f"    … ({pulou} linha(s) sem erro omitida(s))")
            pulou = 0
        res.append(l)
    if pulou:
        res.append(f"    … ({pulou} linha(s) omitida(s))")
    out = "\n".join(res)
    if len(out) > maximo:
        out = "…" + out[-maximo:]
    return out
