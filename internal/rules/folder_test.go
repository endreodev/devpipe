package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devpipe/internal/domain"
)

func TestReadFolderFiltraOrdenaELimita(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-padrao.md", "PADRAO-A")
	write("sub/b-padrao.md", "PADRAO-B")
	write("leia.txt", "PADRAO-TXT")
	write("codigo.cs", "NAO-E-DOC")
	write("node_modules/x.md", "DEPENDENCIA")
	write(".oculta/y.md", "OCULTA")

	r := ReadFolder(dir, "")
	if r.Error != "" {
		t.Fatalf("erro inesperado: %s", r.Error)
	}
	var got []string
	for _, f := range r.Files {
		got = append(got, f.Rel)
	}
	if strings.Join(got, "|") != "a-padrao.md|leia.txt|sub/b-padrao.md" {
		t.Fatalf("filtro/ordem incorretos: %v", got)
	}
	if r.Bytes == 0 {
		t.Fatal("deveria contabilizar o texto lido")
	}

	if f := ReadFolder(dir, "*.cs").Files; len(f) != 1 || f[0].Rel != "codigo.cs" {
		t.Fatalf("filtro por extensão falhou: %v", f)
	}
	if f := ReadFolder(dir, "sub/**").Files; len(f) != 1 || f[0].Rel != "sub/b-padrao.md" {
		t.Fatalf("filtro por subpasta falhou: %v", f)
	}
	if e := ReadFolder(filepath.Join(dir, "nao-existe"), "").Error; e == "" {
		t.Fatal("pasta inexistente deveria reportar erro")
	}
}

func TestComposeIncluiPastaDePadroes(t *testing.T) {
	pastas := t.TempDir()
	os.WriteFile(filepath.Join(pastas, "banco.md"), []byte("PADRAO-DA-PASTA"), 0o644)
	p := domain.Project{ID: 1}
	rs := []domain.Rule{
		{ID: 1, Scope: "global", Title: "Padrões do time", SourceDir: pastas, Enabled: true, Mandatory: true, Version: 1},
		{ID: 2, Scope: "global", Title: "Pasta sumiu", SourceDir: filepath.Join(pastas, "nao-existe"), Content: "TEXTO", Enabled: true, Version: 1},
	}
	c := Compose(rs, p, t.TempDir())
	if !strings.Contains(c.Text, "PADRAO-DA-PASTA") || !strings.Contains(c.Text, "banco.md") {
		t.Fatalf("conteúdo da pasta não entrou no prompt:\n%s", c.Text)
	}
	if !strings.Contains(c.Text, "Pasta de padrões indisponível") || !strings.Contains(c.Text, "TEXTO") {
		t.Fatalf("pasta ausente deveria avisar e manter o texto da regra:\n%s", c.Text)
	}
	os.WriteFile(filepath.Join(pastas, "banco.md"), []byte("PADRAO-ALTERADO"), 0o644)
	if Compose(rs, p, t.TempDir()).Hash == c.Hash {
		t.Fatal("hash deveria mudar quando os arquivos da pasta mudam")
	}
}
