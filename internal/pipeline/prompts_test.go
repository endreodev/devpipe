package pipeline

import (
	"strings"
	"testing"

	"devpipe/internal/domain"
)

func samplePromptCtx() promptCtx {
	return promptCtx{
		D:     domain.Demand{Ticket: "CH-1", Title: "Teste"},
		Rev:   domain.Revision{Number: 1, Instructions: "Criar cupom"},
		Rules: "REGRA-LONGA " + strings.Repeat("padrão de código. ", 2000),
	}
}

// Sessão continuada: regras e especificação já estão no histórico do agente
// e não devem ser reenviadas (economia de tokens).
func TestResumedPromptsSkipContext(t *testing.T) {
	c := samplePromptCtx()
	spec := "ESPEC-UNICA " + strings.Repeat("critério. ", 500)

	full := developPrompt(c, spec)
	c.Resumed = true
	short := developPrompt(c, spec)
	if !strings.Contains(full, "REGRA-LONGA") || !strings.Contains(full, "ESPEC-UNICA") {
		t.Fatal("prompt completo deve conter regras e especificação")
	}
	if strings.Contains(short, "REGRA-LONGA") || strings.Contains(short, "ESPEC-UNICA") {
		t.Fatal("prompt continuado não deve reenviar regras nem especificação")
	}
	if len(short)*5 > len(full) {
		t.Fatalf("redução insuficiente: %d -> %d bytes", len(full), len(short))
	}

	fix := fixPrompt(c, spec, "error CS1002: ; expected", 1)
	if strings.Contains(fix, "REGRA-LONGA") || !strings.Contains(fix, "CS1002") {
		t.Fatalf("correção continuada incorreta:\n%s", fix)
	}
	c.Resumed = false
	if fix := fixPrompt(c, spec, "x", 1); !strings.Contains(fix, "REGRA-LONGA") {
		t.Fatal("correção em sessão nova precisa das regras")
	}
}

// O código gerado fica no repositório da equipe: o prompt precisa proibir
// explicitamente que a IA assine o trabalho em comentários.
func TestPromptProibeAssinaturaDaFerramenta(t *testing.T) {
	c := promptCtx{D: domain.Demand{Ticket: "CH101010", Title: "T"}, P: domain.Project{Name: "P"}}
	dev := developPrompt(c, "spec")
	for _, termo := range []string{"comentário", "ferramenta automatizada", "por IA"} {
		if !strings.Contains(dev, termo) {
			t.Errorf("o prompt de desenvolvimento não contém %q", termo)
		}
	}
}

// Nenhum prompt pode levar o nome da ferramenta para dentro da sessão do
// agente — é de lá que sairiam os comentários no código.
func TestPromptsNaoCitamAFerramenta(t *testing.T) {
	c := promptCtx{D: domain.Demand{Ticket: "CH101010", Title: "T"}, P: domain.Project{Name: "P"}, Rules: "regras"}
	for nome, p := range map[string]string{
		"analise":         analysisPrompt(c),
		"desenvolvimento": developPrompt(c, "spec"),
		"correcao":        fixPrompt(c, "spec", "saida", 1),
	} {
		if strings.Contains(strings.ToLower(p), "devpipe") {
			t.Errorf("o prompt de %s cita a ferramenta", nome)
		}
	}
}
