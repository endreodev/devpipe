package textx

import (
	"strings"
	"testing"
)

func TestCompact(t *testing.T) {
	in := "\ufeffTítulo  \r\n\r\n\r\n\r\n    código\u00a0aqui\t\r\nfim\u200b"
	got := Compact(in)
	want := "Título\n\n    código aqui\nfim"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCompactLogNoiseAndRepeats(t *testing.T) {
	in := "\x1b[32mBuild started\x1b[0m\n  Restoring packages...\n10%\r50%\r100%\nwarn x\nwarn x\nwarn x\nok\n"
	got := CompactLog(in, 0)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "Restoring") || strings.Contains(got, "%") {
		t.Fatalf("ruído não removido: %q", got)
	}
	if !strings.Contains(got, "repetida 2×") {
		t.Fatalf("repetições não colapsadas: %q", got)
	}
}

func TestCompactLogKeepsErrors(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("compilando módulo ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	b.WriteString("src/a.cs(10,5): error CS1002: ; expected\n")
	for i := 0; i < 2000; i++ {
		b.WriteString("passo intermediário ")
		b.WriteString(strings.Repeat("y", i%5))
		b.WriteString("\n")
	}
	b.WriteString("Build FAILED.\n")
	got := CompactLog(b.String(), 4000)
	if len(got) > 4100 {
		t.Fatalf("saída grande demais: %d", len(got))
	}
	if !strings.Contains(got, "error CS1002") || !strings.Contains(got, "Build FAILED") {
		t.Fatalf("erro perdido na compactação:\n%s", got)
	}
}
