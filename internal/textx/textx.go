// Package textx reúne rotinas de compactação de texto usadas para reduzir o
// consumo de tokens nos prompts enviados ao agente (anexos, arquivos de
// padrões e saídas de build/teste). Nenhuma rotina altera o conteúdo
// semântico: removem apenas ruído (espaços, linhas repetidas, códigos ANSI,
// barras de progresso) e, quando necessário, priorizam as linhas de erro.
package textx

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)
	blankRunsRe = regexp.MustCompile(`\n{3,}`)
	// linhas que costumam carregar a causa de uma falha de build/teste
	errLineRe = regexp.MustCompile(`(?i)(\berr(or|o)?\b|\bfail(ed|ure|ou|ha)?\b|\bfalh|exception|panic|traceback|assert|expected|esperad|undefined|not found|não encontrad|cannot|unable|fatal|\bCS\d{4}\b|\bTS\d{4}\b|✗|✖|×\s)`)
	// ruído típico de ferramentas de build (progresso, downloads, restauração)
	noiseLineRe = regexp.MustCompile(`(?i)^\s*(\d{1,3}%|\[=*>?\s*\]|downloading|downloaded|restoring|restored|determining projects|resolving|fetching|progress|\.{3,}\s*$)`)
)

// Compact normaliza texto livre (anexos, regras, markdown): quebras de linha
// CRLF→LF, remove espaços no fim das linhas, caracteres invisíveis comuns em
// documentos (NBSP, zero-width, BOM) e colapsa sequências de linhas em branco.
// A indentação no início das linhas é preservada (código continua legível).
func Compact(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.NewReplacer("\u00a0", " ", "\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "", "\u00ad", "").Replace(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	s = strings.Join(lines, "\n")
	s = blankRunsRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// CompactLog reduz a saída de comandos de build/teste para caber em max bytes
// sem perder a causa da falha:
//  1. remove códigos ANSI e reescritas de barra de progresso (\r);
//  2. descarta linhas de ruído (downloads, percentuais, restauração);
//  3. colapsa linhas idênticas consecutivas ("(repetida N×)");
//  4. se ainda exceder max, mantém o cabeçalho, as linhas de erro com 2 linhas
//     de contexto e o final da saída (onde ficam os resumos de falha).
func CompactLog(s string, max int) string {
	if s == "" {
		return s
	}
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, len(raw))
	prev, rep := "\x00", 0
	flush := func() {
		if rep > 0 {
			lines = append(lines, fmt.Sprintf("    (linha anterior repetida %d×)", rep))
			rep = 0
		}
	}
	for _, l := range raw {
		if i := strings.LastIndexByte(l, '\r'); i >= 0 { // barra de progresso: vale o último estado
			l = l[i+1:]
		}
		l = strings.TrimRight(l, " \t")
		if l == "" || (noiseLineRe.MatchString(l) && !errLineRe.MatchString(l)) {
			continue
		}
		if l == prev {
			rep++
			continue
		}
		flush()
		lines = append(lines, l)
		prev = l
	}
	flush()
	out := strings.Join(lines, "\n")
	if max <= 0 || len(out) <= max {
		return out
	}

	// seleção: cabeçalho + erros com contexto + final
	const head, tailN, ctx = 8, 40, 2
	keep := make([]bool, len(lines))
	for i := 0; i < len(lines) && i < head; i++ {
		keep[i] = true
	}
	for i := len(lines) - tailN; i < len(lines); i++ {
		if i >= 0 {
			keep[i] = true
		}
	}
	for i, l := range lines {
		if errLineRe.MatchString(l) {
			for j := i - ctx; j <= i+ctx; j++ {
				if j >= 0 && j < len(lines) {
					keep[j] = true
				}
			}
		}
	}
	var b strings.Builder
	skipped := 0
	for i, l := range lines {
		if !keep[i] {
			skipped++
			continue
		}
		if skipped > 0 {
			fmt.Fprintf(&b, "    … (%d linha(s) sem erro omitida(s))\n", skipped)
			skipped = 0
		}
		b.WriteString(l + "\n")
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "    … (%d linha(s) omitida(s))\n", skipped)
	}
	res := b.String()
	if len(res) > max { // ainda grande: preserva o final, onde ficam os resumos
		res = Tail(res, max)
	}
	return strings.TrimRight(res, "\n")
}

// Tail mantém os últimos n bytes sem cortar caracteres UTF-8 ao meio.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && (s[i]&0xC0) == 0x80 {
		i++
	}
	return "…" + s[i:]
}

// EstimateTokens dá uma estimativa grosseira (≈ 3,5 caracteres por token para
// português/código). Serve apenas para registro e comparação.
func EstimateTokens(s string) int {
	return (len([]rune(s))*2 + 6) / 7
}
