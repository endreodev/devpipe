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
