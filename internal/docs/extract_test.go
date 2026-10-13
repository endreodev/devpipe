package docs

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDocx(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "a.docx")
	f, _ := os.Create(p)
	z := zip.NewWriter(f)
	w, _ := z.Create("word/document.xml")
	w.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`))
	z.Close()
	f.Close()
	return p
}

func TestExtractDocxParagraphsAndTables(t *testing.T) {
	p := writeDocx(t, `<w:p><w:r><w:t>Regra RN1</w:t></w:r></w:p>`+
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Cenário</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Resultado</w:t></w:r></w:p></w:tc></w:tr>`+
		`<w:tr><w:tc><w:p><w:r><w:t>Cupom 10%</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>total 180</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	text, note := Extract(p)
	if !strings.Contains(text, "Regra RN1") || !strings.Contains(text, "| Cupom 10% | total 180 |") {
		t.Fatalf("extração incorreta (%s):\n%s", note, text)
	}
}

func TestKindAndBlocked(t *testing.T) {
	if Kind("x.PDF") != "pdf" || Kind("a.png") != "imagem" || Kind("r.md") != "texto" || Kind("x.docx") != "docx" {
		t.Fatal("classificação")
	}
	if CheckAllowed("virus.exe") == nil || CheckAllowed("script.ps1") == nil || CheckAllowed("spec.pdf") != nil {
		t.Fatal("bloqueio de tipos")
	}
}

func TestLatin1Text(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(p, []byte{'a', 0xE7, 0xE3, 'o'}, 0o644) // "ação" em latin-1 parcial
	text, _ := Extract(p)
	if !strings.Contains(text, "çã") {
		t.Fatalf("conversão latin-1: %q", text)
	}
}
