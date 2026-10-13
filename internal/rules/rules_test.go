package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devpipe/internal/domain"
)

func TestComposePrecedenceAndHash(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "p.md"), []byte("PADRAO-DO-REPO"), 0o644)
	p := domain.Project{ID: 7, StandardsFiles: []string{"docs/p.md", "../fora.md"}, ExtraRules: "EXTRA"}
	rs := []domain.Rule{
		{ID: 1, Scope: "global", Title: "GRec", Content: "G-REC", Enabled: true, Version: 1},
		{ID: 2, Scope: "global", Title: "GObr", Content: "G-OBR", Enabled: true, Mandatory: true, Version: 3},
		{ID: 3, Scope: "projeto", ProjectID: 7, Title: "PObr", Content: "P-OBR", Enabled: true, Mandatory: true, PathGlob: "src/**", Version: 1},
		{ID: 4, Scope: "projeto", ProjectID: 9, Title: "Outro", Content: "OUTRO-PROJETO", Enabled: true, Version: 1},
		{ID: 5, Scope: "global", Title: "Off", Content: "DESATIVADA", Enabled: false, Version: 1},
	}
	c := Compose(rs, p, dir)
	order := []string{"G-OBR", "P-OBR", "EXTRA", "PADRAO-DO-REPO", "G-REC"}
	last := -1
	for _, s := range order {
		i := strings.Index(c.Text, s)
		if i < 0 || i < last {
			t.Fatalf("ordem/precedência incorreta para %s:\n%s", s, c.Text)
		}
		last = i
	}
	for _, s := range []string{"OUTRO-PROJETO", "DESATIVADA"} {
		if strings.Contains(c.Text, s) {
			t.Fatalf("não deveria conter %s", s)
		}
	}
	if !strings.Contains(c.Text, "fora do repositório") || !strings.Contains(c.Text, "src/**") || !strings.Contains(c.Text, "v3") {
		t.Fatal("faltam metadados")
	}
	rs[1].Version = 4
	rs[1].Content = "G-OBR alterada"
	if Compose(rs, p, dir).Hash == c.Hash {
		t.Fatal("hash deveria mudar quando as regras mudam")
	}
}
