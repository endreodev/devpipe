// Package encx protege a codificação dos arquivos do projeto.
//
// Problema: as ferramentas de edição dos agentes de IA leem e gravam UTF-8.
// Em projetos Windows-1252 (ANSI), cada caractere acentuado vira U+FFFD na
// leitura e o arquivo inteiro é regravado em UTF-8 ("permissões" passa a
// aparecer como "permissï¿½es" no editor).
//
// Solução determinística, independente de prompt:
//  1. Prepare: antes do agente, converte (sem perdas) para UTF-8 os arquivos
//     versionados que estão em Windows-1252, guardando a codificação original.
//  2. Restore: depois do agente, devolve cada arquivo alterado à codificação
//     original (ou à da branch base / padrão do projeto), preservando BOM e
//     CRLF, repara caracteres U+FFFD usando o vocabulário da versão base e
//     reporta o que não puder ser representado.
package encx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"devpipe/internal/gitx"
)

const (
	UTF8    = "utf-8"
	UTF8BOM = "utf-8-bom"
	CP1252  = "windows-1252"
	ASCII   = "ascii"
	UTF16   = "utf-16"
	Auto    = "auto"
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// ---------------------------------------------------------------- Windows-1252

// 0x80–0x9F; bytes indefinidos mapeiam para o controle C1 de mesmo valor
// (ida e volta sem perdas).
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

var cp1252Rev = func() map[rune]byte {
	m := map[rune]byte{}
	for i, r := range cp1252High {
		m[r] = byte(0x80 + i)
	}
	return m
}()

func DecodeCP1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/8)
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// EncodeCP1252 converte; devolve as runas que não existem em Windows-1252.
func EncodeCP1252(s string) ([]byte, []rune) {
	out := make([]byte, 0, len(s))
	var bad []rune
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			if b, ok := cp1252Rev[r]; ok {
				out = append(out, b)
			} else {
				bad = append(bad, r)
				out = append(out, '?')
			}
		}
	}
	return out, bad
}

// ---------------------------------------------------------------- detecção

// Detect classifica o conteúdo.
func Detect(b []byte) string {
	switch {
	case bytes.HasPrefix(b, bom):
		return UTF8BOM
	case len(b) >= 2 && ((b[0] == 0xFF && b[1] == 0xFE) || (b[0] == 0xFE && b[1] == 0xFF)):
		return UTF16
	}
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return ASCII
	}
	if utf8.Valid(b) {
		return UTF8
	}
	return CP1252
}

// binaryExt: extensões nunca tratadas como texto. Qualquer outro arquivo
// versionado é analisado pelo conteúdo (sem bytes nulos = texto), para cobrir
// qualquer linguagem (.asp, .prg, .p, .w, .srw, .4gl, sem extensão etc.).
var binaryExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.png .jpg .jpeg .gif .bmp .ico .tif .tiff .webp .svgz .pdf .zip .7z .rar .gz .tgz .bz2 .xz .tar
		.exe .dll .pdb .so .dylib .a .lib .obj .o .class .jar .war .ear .apk .nupkg .msi .cab .ocx .tlb .chm .res .bin .dat
		.doc .docx .xls .xlsx .ppt .pptx .odt .ods .mdb .accdb .db .sqlite .sdf .mdf .ldf .bak
		.woff .woff2 .ttf .otf .eot .mp3 .mp4 .avi .mov .wav .wmv .flv .snk .pfx .cer .p12 .keystore .suo .cache .dcu .bpl .dcp .exp`) {
		binaryExt[e] = true
	}
}

// IsTextPath: candidato a texto (a confirmação final é pelo conteúdo).
func IsTextPath(p string) bool {
	return !binaryExt[strings.ToLower(filepath.Ext(p))]
}

func looksBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	if bytes.IndexByte(b[:n], 0) >= 0 {
		return true
	}
	ctrl := 0
	for _, c := range b[:n] {
		if c < 0x09 || (c > 0x0D && c < 0x20 && c != 0x1A) {
			ctrl++
		}
	}
	return n > 0 && ctrl*20 > n // mais de 5% de caracteres de controle
}

// ---------------------------------------------------------------- Prepare

// Manifest registra a codificação original dos arquivos convertidos.
type Manifest struct {
	Converted   map[string]string // caminho relativo -> codificação original
	Hash        map[string]string // caminho relativo -> sha256 do UTF-8 gravado na conversão
	Predominant string            // codificação predominante (vazio = calcular se necessário)
	Legacy      int
	UTF8        int
}

func newManifest() *Manifest {
	return &Manifest{Converted: map[string]string{}, Hash: map[string]string{}}
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ConvertFile converte um único arquivo Windows-1252 para UTF-8 (sem perdas).
// Devolve converted=false se o arquivo não precisa de conversão.
func ConvertFile(full string) (converted bool, hash string, err error) {
	b, err := os.ReadFile(full)
	if err != nil || len(b) > 20<<20 || looksBinary(b) || Detect(b) != CP1252 {
		return false, "", nil
	}
	st, err := os.Stat(full)
	if err != nil {
		return false, "", err
	}
	out := []byte(DecodeCP1252(b))
	if err := os.WriteFile(full, out, st.Mode().Perm()); err != nil {
		return false, "", err
	}
	return true, sha(out), nil
}

// Prepare converte TODOS os arquivos versionados em Windows-1252 (modo
// completo, usado por provedores sem suporte a hooks).
func Prepare(dir string) (*Manifest, error) {
	m := newManifest()
	files, err := gitx.Repo{Dir: dir}.TrackedFiles()
	if err != nil {
		return m, err
	}
	for _, rel := range files {
		if !IsTextPath(rel) {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(full)
		if err != nil || looksBinary(b) || len(b) > 20<<20 {
			continue
		}
		switch Detect(b) {
		case CP1252:
			m.Legacy++
			ok, h, err := ConvertFile(full)
			if err != nil {
				return m, fmt.Errorf("falha ao converter %s: %w", rel, err)
			}
			if ok {
				m.Converted[rel], m.Hash[rel] = CP1252, h
			}
		case UTF8, UTF8BOM:
			m.UTF8++
		}
	}
	if m.Legacy > m.UTF8 {
		m.Predominant = CP1252
	} else {
		m.Predominant = UTF8
	}
	return m, nil
}

// ---------------------------------------------------------------- modo sob demanda (hook)

// HookConvert é chamado pelo hook PreToolUse do agente, imediatamente antes de
// ler/editar um arquivo: converte só esse arquivo e registra no manifesto.
func HookConvert(root, manifestPath, file string) error {
	if file == "" {
		return nil
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	file = filepath.Clean(file)
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || !IsTextPath(file) {
		return nil // fora da worktree: não mexe
	}
	ok, h, err := ConvertFile(file)
	if err != nil || !ok {
		return err
	}
	f, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\n", filepath.ToSlash(rel), CP1252, h)
	return err
}

// LoadManifest lê o manifesto gravado pelos hooks.
func LoadManifest(path string) *Manifest {
	m := newManifest()
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, l := range strings.Split(string(b), "\n") {
		p := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(p) == 3 {
			if _, dup := m.Converted[p[0]]; !dup { // vale a primeira conversão
				m.Converted[p[0]], m.Hash[p[0]] = p[1], p[2]
			}
		}
	}
	return m
}

// ---------------------------------------------------------------- Restore

type Issue struct {
	File    string
	Line    int
	Problem string
}

func (i Issue) String() string {
	if i.Line > 0 {
		return fmt.Sprintf("%s:%d — %s", i.File, i.Line, i.Problem)
	}
	return i.File + " — " + i.Problem
}

type Report struct {
	Restored []string // arquivos devolvidos à codificação original
	Repaired []string // arquivos com caracteres U+FFFD recuperados
	Issues   []Issue
}

// Restore devolve à codificação correta todos os arquivos alterados na
// worktree e nos commits desde baseRef. defaultEnc vale para arquivos novos
// ("auto" usa a codificação predominante do repositório).
func Restore(dir, baseRef string, m *Manifest, defaultEnc string) Report {
	var rep Report
	repo := gitx.Repo{Dir: dir}
	if m == nil {
		m = newManifest()
	}
	if m.Hash == nil {
		m.Hash = map[string]string{}
	}
	resolvedDefault := ""
	defaultFor := func() string { // calculado só se houver arquivo novo
		if resolvedDefault == "" {
			switch {
			case defaultEnc != "" && defaultEnc != Auto:
				resolvedDefault = defaultEnc
			case m.Predominant != "":
				resolvedDefault = m.Predominant
			default:
				resolvedDefault = predominantOnDisk(dir)
			}
		}
		return resolvedDefault
	}
	seen := map[string]bool{}
	var paths []string
	changed, _ := repo.ChangedPaths()
	paths = append(paths, changed...)
	if baseRef != "" {
		paths = append(paths, repo.FilesChangedSince(baseRef)...)
	}
	for rel := range m.Converted {
		paths = append(paths, rel)
	}
	for _, rel := range paths {
		if seen[rel] || !IsTextPath(rel) {
			continue
		}
		seen[rel] = true
		full := filepath.Join(dir, filepath.FromSlash(rel))
		cur, err := os.ReadFile(full)
		if err != nil || looksBinary(cur) {
			continue
		}
		// caminho rápido: convertido e não alterado pelo agente -> bytes originais
		if h, ok := m.Hash[rel]; ok && h == sha(cur) && m.Converted[rel] == CP1252 {
			if orig, bad := EncodeCP1252(string(cur)); len(bad) == 0 {
				st, _ := os.Stat(full)
				if err := os.WriteFile(full, orig, st.Mode().Perm()); err != nil {
					rep.Issues = append(rep.Issues, Issue{rel, 0, "falha ao restaurar codificação: " + err.Error()})
				}
				continue
			}
		}
		baseBytes, inBase := repo.ShowFile(baseRef, rel)
		baseEnc := ""
		if inBase {
			baseEnc = Detect(baseBytes)
		}
		target := m.Converted[rel]
		if target == "" {
			switch baseEnc {
			case CP1252, UTF8, UTF8BOM, UTF16:
				target = baseEnc
			default: // novo ou ASCII puro: segue o padrão do projeto
				if inBase && baseEnc == ASCII && !hasNonASCII(cur) {
					continue // continua ASCII puro: nada a fazer
				}
				target = defaultFor()
			}
		}
		if target == UTF16 || target == ASCII {
			continue // não alteramos UTF-16; ASCII puro não tem o que converter
		}

		// texto atual
		curEnc := Detect(cur)
		var text string
		switch curEnc {
		case CP1252:
			text = DecodeCP1252(cur) // agente não tocou: continua legado
		case UTF8BOM:
			text = string(cur[3:])
		case UTF16:
			continue
		default:
			text = string(cur)
		}
		var baseText string
		if inBase {
			switch baseEnc {
			case CP1252:
				baseText = DecodeCP1252(baseBytes)
			case UTF8BOM:
				baseText = string(baseBytes[3:])
			default:
				baseText = string(baseBytes)
			}
		}

		// reparo de U+FFFD introduzido (não existia na base)
		if strings.ContainsRune(text, utf8.RuneError) && !strings.ContainsRune(baseText, utf8.RuneError) {
			fixed, n := RepairReplacementChars(text, baseText)
			if n > 0 {
				text = fixed
				rep.Repaired = append(rep.Repaired, rel)
			}
			if strings.ContainsRune(text, utf8.RuneError) {
				for _, ln := range linesWith(text, utf8.RuneError) {
					rep.Issues = append(rep.Issues, Issue{rel, ln, "caractere corrompido (U+FFFD '�'): reescreva a palavra com a acentuação correta"})
				}
			}
		}

		// quebras de linha: se a base era toda CRLF, mantém CRLF
		if inBase && allCRLF(baseText) {
			text = toCRLF(text)
		}

		var out []byte
		switch target {
		case CP1252:
			enc, bad := EncodeCP1252(text)
			bad = withoutRune(bad, utf8.RuneError) // já reportado como corrompido
			if len(bad) > 0 {
				for _, ln := range linesWithAny(text, bad) {
					rep.Issues = append(rep.Issues, Issue{rel, ln, fmt.Sprintf("caractere(s) %q não existem em Windows-1252 (codificação do arquivo); use equivalentes compatíveis", uniqueRunes(bad))})
				}
			}
			out = enc
		case UTF8BOM:
			out = append(append([]byte{}, bom...), text...)
		default:
			out = []byte(text)
		}
		if !bytes.Equal(out, cur) {
			st, _ := os.Stat(full)
			if err := os.WriteFile(full, out, st.Mode().Perm()); err != nil {
				rep.Issues = append(rep.Issues, Issue{rel, 0, "falha ao gravar na codificação original: " + err.Error()})
				continue
			}
			if curEnc != target && curEnc != ASCII && m.Converted[rel] == "" {
				rep.Restored = append(rep.Restored, rel)
			}
		}
	}
	return rep
}

// predominantOnDisk amostra os arquivos de texto da pasta (sem subprocessos).
func predominantOnDisk(dir string) string {
	legacy, u, seen := 0, 0, 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if seen >= 400 {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "bin" || name == "obj" || name == "packages" {
				return filepath.SkipDir
			}
			return nil
		}
		if !IsTextPath(name) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 2<<20 || looksBinary(b) {
			return nil
		}
		seen++
		switch Detect(b) {
		case CP1252:
			legacy++
		case UTF8, UTF8BOM:
			u++
		}
		return nil
	})
	if legacy > u {
		return CP1252
	}
	return UTF8
}

func hasNonASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- reparo

// RepairReplacementChars recupera palavras em que caracteres acentuados viraram
// U+FFFD, usando o vocabulário do texto de referência (versão base do arquivo).
// Uma palavra só é corrigida quando existe exatamente uma candidata compatível.
func RepairReplacementChars(text, reference string) (string, int) {
	if reference == "" {
		return text, 0
	}
	vocab := map[string]bool{} // palavras da referência com caracteres não-ASCII
	for _, w := range words(reference) {
		if !isASCII(w) && !strings.ContainsRune(w, utf8.RuneError) {
			vocab[w] = true
		}
	}
	fixedCount := 0
	cache := map[string]string{}
	var out strings.Builder
	rs := []rune(text)
	for i := 0; i < len(rs); {
		if !isWordRune(rs[i]) {
			out.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && isWordRune(rs[j]) {
			j++
		}
		w := string(rs[i:j])
		if strings.ContainsRune(w, utf8.RuneError) {
			c, ok := cache[w]
			if !ok {
				c, _ = uniqueCandidate(w, vocab)
				cache[w] = c
			}
			if c != "" {
				w = c
				fixedCount++
			}
		}
		out.WriteString(w)
		i = j
	}
	return out.String(), fixedCount
}

// uniqueCandidate: cada sequência de U+FFFD corresponde a 1 ou mais caracteres
// não-ASCII (leitores diferentes geram um '�' por byte ou por sequência).
func uniqueCandidate(w string, vocab map[string]bool) (string, bool) {
	var pat strings.Builder
	pat.WriteString("^")
	inRun := false
	for _, r := range w {
		if r == utf8.RuneError {
			if !inRun {
				pat.WriteString(`[^\x00-\x7F]+`)
				inRun = true
			}
			continue
		}
		inRun = false
		pat.WriteString(regexp.QuoteMeta(string(r)))
	}
	pat.WriteString("$")
	re, err := regexp.Compile(pat.String())
	if err != nil {
		return "", false
	}
	found := ""
	for c := range vocab {
		if re.MatchString(c) {
			if found != "" && found != c {
				return "", false // ambíguo: não chutar
			}
			found = c
		}
	}
	return found, found != ""
}

func isWordRune(r rune) bool {
	return r == utf8.RuneError || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !isWordRune(r) })
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func linesWith(s string, r rune) []int {
	return linesWithAny(s, []rune{r})
}

func linesWithAny(s string, rs []rune) []int {
	var out []int
	for i, l := range strings.Split(s, "\n") {
		if strings.ContainsAny(l, string(rs)) {
			out = append(out, i+1)
			if len(out) >= 20 {
				break
			}
		}
	}
	return out
}

func uniqueRunes(rs []rune) string {
	seen := map[rune]bool{}
	var b strings.Builder
	for _, r := range rs {
		if !seen[r] {
			seen[r] = true
			b.WriteRune(r)
		}
	}
	return b.String()
}

func allCRLF(s string) bool {
	n := strings.Count(s, "\n")
	return n > 0 && strings.Count(s, "\r\n") == n
}

func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func withoutRune(rs []rune, x rune) []rune {
	out := rs[:0:0]
	for _, r := range rs {
		if r != x {
			out = append(out, r)
		}
	}
	return out
}
