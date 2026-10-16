// Package rules compõe as regras de desenvolvimento enviadas ao agente,
// aplicando a precedência definida e gerando um hash para auditoria.
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"devpipe/internal/domain"
	"devpipe/internal/textx"
)

// Precedence é explicada ao agente literalmente.
const Precedence = `## PRECEDÊNCIA DAS REGRAS (da maior para a menor)
1. Política de segurança da aplicação (fixa, sempre prevalece).
2. Regras GLOBAIS marcadas como OBRIGATÓRIAS.
3. Regras do PROJETO marcadas como OBRIGATÓRIAS.
4. Regras do PROJETO (padrões e arquivos de padrões do repositório).
5. Regras GLOBAIS recomendadas.
6. Instruções específicas da demanda — podem detalhar ou restringir, mas NUNCA violar os itens 1 a 3.
Em caso de conflito entre regras do mesmo nível, siga a de menor número de prioridade e registre o conflito no resumo.`

type Composed struct {
	Text string
	Hash string
}

// Compose monta o texto de regras. standardsRoot é o diretório onde os arquivos
// de padrões do projeto são procurados (normalmente a worktree).
func Compose(all []domain.Rule, p domain.Project, standardsRoot string) Composed {
	var b strings.Builder
	b.WriteString(Precedence + "\n\n")
	section := func(title string, filter func(domain.Rule) bool) {
		var items []domain.Rule
		for _, r := range all {
			if r.Enabled && filter(r) {
				items = append(items, r)
			}
		}
		if len(items) == 0 {
			return
		}
		b.WriteString("### " + title + "\n")
		for _, r := range items {
			fmt.Fprintf(&b, "#### [%s v%d] %s", ruleRef(r), r.Version, r.Title)
			if r.Category != "" {
				fmt.Fprintf(&b, " — %s", r.Category)
			}
			b.WriteString("\n")
			var scope []string
			if r.PathGlob != "" {
				scope = append(scope, "arquivos/diretórios: "+r.PathGlob)
			}
			if r.Language != "" {
				scope = append(scope, "linguagem: "+r.Language)
			}
			if len(scope) > 0 {
				b.WriteString("_Aplica-se somente a " + strings.Join(scope, "; ") + "_\n")
			}
			b.WriteString(textx.Compact(r.Content) + "\n\n")
		}
	}
	section("2. Regras globais OBRIGATÓRIAS", func(r domain.Rule) bool { return r.Scope == "global" && r.Mandatory })
	section("3. Regras do projeto OBRIGATÓRIAS", func(r domain.Rule) bool { return r.Scope == "projeto" && r.ProjectID == p.ID && r.Mandatory })
	section("4. Regras do projeto", func(r domain.Rule) bool { return r.Scope == "projeto" && r.ProjectID == p.ID && !r.Mandatory })

	if strings.TrimSpace(p.ExtraRules) != "" {
		b.WriteString("#### Regras adicionais do cadastro do projeto\n" + textx.Compact(p.ExtraRules) + "\n\n")
	}
	for _, f := range p.StandardsFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		full := filepath.Join(standardsRoot, filepath.FromSlash(f))
		rel, err := filepath.Rel(standardsRoot, full)
		if err != nil || strings.HasPrefix(rel, "..") {
			b.WriteString("#### Arquivo de padrões ignorado (fora do repositório): " + f + "\n\n")
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			b.WriteString("#### Arquivo de padrões não encontrado: " + f + "\n\n")
			continue
		}
		txt := compactStandards(string(data))
		if len(txt) > 40<<10 {
			txt = txt[:40<<10] + "\n…(truncado; leia o arquivo " + f + " se precisar do restante)"
		}
		b.WriteString("#### Arquivo de padrões do projeto: " + f + "\n" + txt + "\n\n")
	}
	section("5. Regras globais recomendadas", func(r domain.Rule) bool { return r.Scope == "global" && !r.Mandatory })

	text := b.String()
	sum := sha256.Sum256([]byte(text))
	return Composed{Text: text, Hash: hex.EncodeToString(sum[:])[:16]}
}

func ruleRef(r domain.Rule) string {
	if r.Scope == "global" {
		return fmt.Sprintf("G%d", r.ID)
	}
	return fmt.Sprintf("P%d", r.ID)
}

var (
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	mdImageRe     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	badgeLineRe   = regexp.MustCompile(`(?m)^\s*(\[!\[[^\n]*|<img [^\n]*>|<p align[^\n]*>|</p>)\s*$`)
)

// compactStandards remove de arquivos de padrões o que não orienta o agente
// (comentários HTML, imagens, badges) e normaliza espaços em branco.
func compactStandards(s string) string {
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = badgeLineRe.ReplaceAllString(s, "")
	s = mdImageRe.ReplaceAllString(s, "")
	return textx.Compact(s)
}
