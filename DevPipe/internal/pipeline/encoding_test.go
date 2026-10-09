package pipeline

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devpipe/internal/domain"
	"devpipe/internal/encx"
)

// Agente que se comporta como as ferramentas de edição reais: lê o arquivo
// como UTF-8 (bytes inválidos viram U+FFFD) e grava o arquivo inteiro em UTF-8.
const corruptingAgent = `case "$DEVPIPE_PHASE" in
analise) cat >/dev/null; printf '# Especificação — E\n## Objetivo\nAjustar mensagem\n';;
*) cat >/dev/null; python3 -c "
p='Servico.cs'
s=open(p,'rb').read().decode('utf-8','replace')
s=s.replace('negada', 'negada pela regra de permissão')
open(p,'w',encoding='utf-8',newline='').write(s)
"; echo feito;;
esac`

func TestEncodingIsPreservedThroughPipeline(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 indisponível")
	}
	e, st, p := setup(t, corruptingAgent)
	orig := "// Serviço de permissões\r\nclass Servico {\r\n    string Msg = \"Ação negada\";\r\n}\r\n"
	raw, _ := encx.EncodeCP1252(orig)
	os.WriteFile(filepath.Join(p.Path, "Servico.cs"), raw, 0o644)
	run(t, p.Path, "git", "add", ".")
	run(t, p.Path, "git", "commit", "-qm", "arquivo ANSI")
	p.BuildCommand, p.TestCommand = "", ""
	st.SaveProject(&p)

	d := domain.Demand{Ticket: "ENC-1", Title: "acentuação", ProjectID: p.ID, Provider: "generico"}
	st.CreateDemand(&d, "x")
	e.Enqueue(d.ID)
	got := waitFor(t, st, d.ID, func(x domain.Demand) bool {
		return (x.Stage == domain.StageReview || x.Stage == domain.StageBlocked) && !e.IsRunning(x.ID)
	})
	if got.Stage != domain.StageReview {
		t.Fatalf("esperava revisão: %s %s", got.Stage, got.StatusMessage)
	}
	b, _ := os.ReadFile(filepath.Join(got.WorktreePath, "Servico.cs"))
	want, _ := encx.EncodeCP1252(strings.Replace(orig, "negada", "negada pela regra de permissão", 1))
	if !bytes.Equal(b, want) {
		t.Fatalf("codificação não preservada:\n%q\n%q", encx.DecodeCP1252(b), encx.DecodeCP1252(want))
	}
}
