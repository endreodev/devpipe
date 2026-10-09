// Package docs extrai texto de anexos (PDF, DOCX, TXT/MD, código) e classifica
// imagens. Os originais nunca são alterados; o texto extraído é gravado ao lado.
package docs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxUploadBytes = 50 << 20 // 50 MB por arquivo

var textExt = map[string]bool{".txt": true, ".md": true, ".markdown": true, ".csv": true, ".json": true, ".xml": true, ".yaml": true, ".yml": true,
	".sql": true, ".cs": true, ".go": true, ".js": true, ".ts": true, ".py": true, ".java": true, ".html": true, ".css": true, ".log": true, ".feature": true, ".rtf": false}
var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}
var blockedExt = map[string]bool{".exe": true, ".dll": true, ".bat": true, ".cmd": true, ".ps1": true, ".sh": true, ".msi": true, ".scr": true, ".com": true, ".vbs": true, ".jar": true}

// Kind classifica o arquivo pela extensão.
func Kind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".pdf":
		return "pdf"
	case ext == ".docx":
		return "docx"
	case ext == ".doc":
		return "doc"
	case imageExt[ext]:
		return "imagem"
	case textExt[ext]:
		return "texto"
	}
	return "outro"
}

// CheckAllowed rejeita executáveis/scripts como anexos.
func CheckAllowed(name string) error {
	if blockedExt[strings.ToLower(filepath.Ext(name))] {
		return fmt.Errorf("tipo de arquivo não permitido como anexo: %s", filepath.Ext(name))
	}
	return nil
}

// Extract produz texto a partir do arquivo. Retorna (texto, nota). Quando não é
// possível extrair, texto vem vazio e a nota explica (o agente ainda pode ler o
// arquivo original diretamente — p. ex. PDFs e imagens com o Claude).
func Extract(path string) (string, string) {
	switch Kind(path) {
	case "texto":
		b, err := readLimited(path, 5<<20)
		if err != nil {
			return "", "falha ao ler: " + err.Error()
		}
		if !utf8.Valid(b) {
			b = latin1ToUTF8(b)
		}
		return string(b), "texto lido diretamente"
	case "docx":
		t, err := extractDocx(path)
		if err != nil {
			return "", "falha ao extrair DOCX: " + err.Error()
		}
		return t, "texto extraído do DOCX"
	case "pdf":
		if t, err := runTool("pdftotext", "-layout", path, "-"); err == nil && strings.TrimSpace(t) != "" {
			return t, "texto extraído com pdftotext"
		}
		return "", "pdftotext indisponível ou PDF sem camada de texto; o agente lerá o PDF original"
	case "doc":
		for _, tool := range [][]string{{"antiword", path}, {"catdoc", path}} {
			if t, err := runTool(tool[0], tool[1:]...); err == nil && strings.TrimSpace(t) != "" {
				return t, "texto extraído com " + tool[0]
			}
		}
		return "", "formato .doc legado: instale antiword/catdoc ou converta para .docx/PDF"
	case "imagem":
		return "", "imagem: será analisada visualmente pelo provedor de IA (se suportado)"
	}
	return "", "formato não suportado para extração; arquivo preservado"
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

func latin1ToUTF8(b []byte) []byte {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return []byte(string(r))
}

func runTool(name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// extractDocx lê word/document.xml (stdlib apenas) preservando parágrafos e tabelas.
func extractDocx(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
		}
	}
	if doc == nil {
		return "", errors.New("word/document.xml não encontrado")
	}
	if doc.UncompressedSize64 > 100<<20 {
		return "", errors.New("documento grande demais")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var sb, para, row bytes.Buffer
	inText, cellDepth := false, 0
	var cells []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				para.WriteString("\t")
			case "br":
				para.WriteString(" ")
			case "tc":
				cellDepth++
				row.Reset()
			case "tr":
				cells = cells[:0]
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				txt := strings.TrimSpace(para.String())
				para.Reset()
				if cellDepth > 0 {
					if row.Len() > 0 && txt != "" {
						row.WriteString(" ")
					}
					row.WriteString(txt)
				} else {
					sb.WriteString(txt + "\n")
				}
			case "tc":
				cellDepth--
				cells = append(cells, strings.ReplaceAll(row.String(), "|", "/"))
			case "tr":
				sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
			case "tbl":
				sb.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return sb.String(), nil
}
