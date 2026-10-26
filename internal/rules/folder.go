package rules

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"devpipe/internal/docs"
)

// DefaultFolderGlob é o filtro aplicado quando a regra não define o seu:
// apenas documentação, que é o que costuma existir numa pasta de padrões.
const DefaultFolderGlob = "*.md; *.markdown; *.txt"

// Limites da leitura de pastas de padrões. Existem para proteger o prompt:
// uma pasta grande demais estouraria o contexto do agente.
const (
	MaxFolderFiles = 60
	MaxFileBytes   = 60 << 10
	MaxFolderBytes = 160 << 10
	maxFolderDepth = 8
)

// skipDirs nunca entram na varredura — não são padrões de desenvolvimento.
var skipDirs = map[string]bool{"node_modules": true, "bin": true, "obj": true, "dist": true,
	"vendor": true, "packages": true, "target": true, "__pycache__": true, "venv": true}

// FolderFile é um arquivo lido da pasta de padrões.
type FolderFile struct {
	Rel   string `json:"rel"`   // caminho relativo à pasta, com "/"
	Bytes int    `json:"bytes"` // tamanho do texto aproveitado
	Note  string `json:"note"`  // aviso (truncado, não foi possível extrair…)
	Text  string `json:"-"`
}

// FolderResult descreve o que foi lido de uma pasta de padrões.
type FolderResult struct {
	Dir     string       `json:"dir"`
	Glob    string       `json:"glob"`
	Files   []FolderFile `json:"files"`
	Bytes   int          `json:"bytes"`
	Skipped int          `json:"skipped"` // arquivos que casaram o filtro mas ficaram de fora pelos limites
	Error   string       `json:"error"`
}

// ParseGlobs quebra o filtro digitado pelo usuário (";", "," ou linhas).
func ParseGlobs(s string) []string {
	f := func(r rune) bool { return r == ';' || r == ',' || r == '\n' || r == '\r' }
	var out []string
	for _, part := range strings.FieldsFunc(s, f) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, filepath.ToSlash(part))
		}
	}
	return out
}

// globRe traduz um glob (com "**") em expressão regular ancorada.
func globRe(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?i)\A`)
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString(`(?:[^/]*/)*`) // **/ = qualquer número de diretórios
					continue
				}
				b.WriteString(`.*`)
				continue
			}
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// matchGlobs: filtro sem "/" casa com o nome do arquivo; com "/", com o
// caminho relativo inteiro.
func matchGlobs(rel string, globs []string) bool {
	base := filepath.Base(rel)
	for _, g := range globs {
		target := base
		if strings.Contains(g, "/") {
			target = rel
		}
		if re := globRe(g); re != nil && re.MatchString(target) {
			return true
		}
	}
	return false
}

// ReadFolder lê os arquivos de padrões de dir que casam com o filtro, em ordem
// alfabética e respeitando os limites. Nunca segue links simbólicos.
func ReadFolder(dir, glob string) FolderResult {
	res := FolderResult{Dir: dir, Glob: strings.TrimSpace(glob), Files: []FolderFile{}}
	if res.Glob == "" {
		res.Glob = DefaultFolderGlob
	}
	globs := ParseGlobs(res.Glob)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		res.Error = "pasta não informada"
		return res
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		res.Error = "caminho inválido"
		return res
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		res.Error = "pasta não encontrada: " + dir
		return res
	}
	var matched []string
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // diretório sem permissão: ignora e segue
		}
		rel, rerr := filepath.Rel(abs, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path == abs {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") || skipDirs[strings.ToLower(name)] {
				return filepath.SkipDir
			}
			if strings.Count(rel, "/")+1 >= maxFolderDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // links simbólicos e afins ficam de fora
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || !matchGlobs(rel, globs) {
			return nil
		}
		matched = append(matched, rel)
		return nil
	})
	if err != nil {
		res.Error = "falha ao ler a pasta: " + err.Error()
		return res
	}
	sort.Strings(matched)
	for _, rel := range matched {
		if len(res.Files) >= MaxFolderFiles || res.Bytes >= MaxFolderBytes {
			res.Skipped++
			continue
		}
		text, note := docs.Extract(filepath.Join(abs, filepath.FromSlash(rel)))
		text = compactStandards(text)
		if strings.TrimSpace(text) == "" {
			res.Files = append(res.Files, FolderFile{Rel: rel, Note: "sem texto aproveitável (" + note + ")"})
			continue
		}
		f := FolderFile{Rel: rel}
		if len(text) > MaxFileBytes {
			text = text[:MaxFileBytes] + "\n…(truncado)"
			f.Note = "truncado em " + fmt.Sprintf("%d KB", MaxFileBytes>>10)
		}
		if rest := MaxFolderBytes - res.Bytes; len(text) > rest {
			text = text[:rest] + "\n…(truncado: limite da pasta atingido)"
			f.Note = "truncado pelo limite da pasta"
		}
		f.Text, f.Bytes = text, len(text)
		res.Bytes += f.Bytes
		res.Files = append(res.Files, f)
	}
	return res
}

// writeFolder despeja no prompt o conteúdo da pasta de padrões de uma regra.
func writeFolder(b *strings.Builder, r FolderResult) {
	if r.Error != "" {
		fmt.Fprintf(b, "_Pasta de padrões indisponível (%s) — siga apenas o texto da regra._\n\n", r.Error)
		return
	}
	used := 0
	for _, f := range r.Files {
		if f.Text == "" {
			continue
		}
		used++
	}
	if used == 0 {
		fmt.Fprintf(b, "_Pasta de padrões %s: nenhum arquivo casou com o filtro `%s`._\n\n", r.Dir, r.Glob)
		return
	}
	fmt.Fprintf(b, "_Padrões lidos da pasta %s (filtro `%s`, %d arquivo(s))", r.Dir, r.Glob, used)
	if r.Skipped > 0 {
		fmt.Fprintf(b, "; %d arquivo(s) ficaram de fora pelo limite de tamanho", r.Skipped)
	}
	b.WriteString("._\n\n")
	for _, f := range r.Files {
		if f.Text == "" {
			continue
		}
		fmt.Fprintf(b, "##### Arquivo de padrões: %s\n", f.Rel)
		b.WriteString(f.Text + "\n\n")
	}
}
