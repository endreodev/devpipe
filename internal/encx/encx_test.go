package encx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cp(t *testing.T, s string) []byte {
	b, bad := EncodeCP1252(s)
	if len(bad) > 0 {
		t.Fatalf("fixture inválida: %q", bad)
	}
	return b
}

// simulateAgentEdit imita a ferramenta Edit: lê como UTF-8 (bytes inválidos
// viram U+FFFD), aplica a troca e grava o arquivo inteiro em UTF-8.
func simulateAgentEdit(t *testing.T, path, old, new string) {
	b, _ := os.ReadFile(path)
	s := strings.ToValidUTF8(string(b), "�")
	if !strings.Contains(s, old) {
		t.Fatalf("trecho não encontrado: %q em %q", old, s)
	}
	os.WriteFile(path, []byte(strings.Replace(s, old, new, 1)), 0o644)
}

const original = "// Valida permissões do usuário\r\npublic class Permissao {\r\n    // ação padrão: negar\r\n    string msg = \"Operação não permitida\";\r\n}\r\n"

func setupRepo(t *testing.T) string {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "Permissao.cs"), cp(t, original), 0o644)
	os.WriteFile(filepath.Join(dir, "Util.cs"), cp(t, "// utilitário de conversão\r\nclass Util {}\r\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Bom.cs"), append(append([]byte{}, bom...), []byte("// configuração\r\n")...), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
	git(t, dir, "checkout", "-qb", "trabalho")
	return dir
}

func TestCP1252RoundTripIsLossless(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	back, bad := EncodeCP1252(DecodeCP1252(all))
	if len(bad) > 0 || !bytes.Equal(back, all) {
		t.Fatal("ida e volta Windows-1252 não é sem perdas")
	}
}

func TestPrepareEditRestoreKeepsEncoding(t *testing.T) {
	dir := setupRepo(t)
	base := gitBase(t, dir)
	m, err := Prepare(dir)
	if err != nil || m.Converted["Permissao.cs"] != CP1252 || m.Predominant != CP1252 {
		t.Fatalf("prepare: %+v %v", m, err)
	}
	// com o guarda, o agente vê o texto correto em UTF-8
	simulateAgentEdit(t, filepath.Join(dir, "Permissao.cs"), "// ação padrão: negar", "// ação padrão: negar com exceção")
	os.WriteFile(filepath.Join(dir, "Novo.cs"), []byte("// nova validação\nclass Novo {}\n"), 0o644)

	rep := Restore(dir, base, m, Auto)
	if len(rep.Issues) > 0 {
		t.Fatalf("issues: %v", rep.Issues)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "Permissao.cs"))
	want := cp(t, strings.Replace(original, "// ação padrão: negar", "// ação padrão: negar com exceção", 1))
	if !bytes.Equal(got, want) {
		t.Fatalf("arquivo não voltou a Windows-1252 com CRLF:\n%q\n%q", got, want)
	}
	// arquivo convertido e não editado volta byte a byte
	util, _ := os.ReadFile(filepath.Join(dir, "Util.cs"))
	if !bytes.Equal(util, cp(t, "// utilitário de conversão\r\nclass Util {}\r\n")) {
		t.Fatal("arquivo não editado foi alterado")
	}
	if st := git(t, dir, "status", "--porcelain", "Util.cs", "Bom.cs"); st != "" {
		t.Fatalf("arquivos não editados aparecem como alterados: %q", st)
	}
	// arquivo novo segue a codificação predominante (Windows-1252)
	novo, _ := os.ReadFile(filepath.Join(dir, "Novo.cs"))
	if Detect(novo) != CP1252 {
		t.Fatalf("arquivo novo deveria estar em Windows-1252: %q", novo)
	}
}

func TestRepairAlreadyCorruptedBranch(t *testing.T) {
	dir := setupRepo(t)
	base := gitBase(t, dir)
	// cenário da v0.1: agente editou SEM o guarda e o commit foi feito
	simulateAgentEdit(t, filepath.Join(dir, "Permissao.cs"), "public class Permissao {", "public class Permissao { // revisado")
	b, _ := os.ReadFile(filepath.Join(dir, "Permissao.cs"))
	if !strings.Contains(string(b), "permiss�es") {
		t.Fatalf("simulação não reproduziu o defeito: %q", b)
	}
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qam", "quebrado")

	rep := Restore(dir, base, nil, Auto)
	got, _ := os.ReadFile(filepath.Join(dir, "Permissao.cs"))
	want := cp(t, strings.Replace(original, "public class Permissao {", "public class Permissao { // revisado", 1))
	if !bytes.Equal(got, want) {
		t.Fatalf("reparo falhou (%v):\n%q\n%q", rep.Issues, DecodeCP1252(got), original)
	}
	if len(rep.Repaired) != 1 || len(rep.Issues) != 0 {
		t.Fatalf("relatório: %+v", rep)
	}
}

func TestUnrepresentableAndUnrecoverableAreReported(t *testing.T) {
	dir := setupRepo(t)
	base := gitBase(t, dir)
	m, _ := Prepare(dir)
	p := filepath.Join(dir, "Permissao.cs")
	b, _ := os.ReadFile(p)
	s := strings.Replace(string(b), "negar", "negar → registrar", 1) // seta não existe em 1252
	s = strings.Replace(s, "}", "// xyz�w\n}", 1)                    // sem candidata no vocabulário
	os.WriteFile(p, []byte(s), 0o644)
	rep := Restore(dir, base, m, Auto)
	if len(rep.Issues) != 2 {
		t.Fatalf("esperava 2 problemas, obtido %v", rep.Issues)
	}
}

func TestBOMPreserved(t *testing.T) {
	dir := setupRepo(t)
	base := gitBase(t, dir)
	m, _ := Prepare(dir)
	p := filepath.Join(dir, "Bom.cs")
	os.WriteFile(p, []byte("// configuração alterada\r\n"), 0o644) // ferramenta removeu o BOM
	Restore(dir, base, m, Auto)
	got, _ := os.ReadFile(p)
	if !bytes.HasPrefix(got, bom) || !utf8.Valid(got) {
		t.Fatalf("BOM não preservado: %q", got)
	}
}

func TestRepairAmbiguityIsNotGuessed(t *testing.T) {
	out, n := RepairReplacementChars("a �rea", "área êrea")
	if n != 0 || !strings.Contains(out, "�") {
		t.Fatal("não pode chutar quando há duas candidatas")
	}
	out, n = RepairReplacementChars("permiss�es e a��o", "permissões, ação")
	if n != 2 || out != "permissões e ação" {
		t.Fatalf("%q %d", out, n)
	}
}

func gitBase(t *testing.T, dir string) string { return git(t, dir, "rev-parse", "main") }

func TestAnyTextExtensionIsProtected(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for _, n := range []string{"pagina.asp", "rotina.prg", "LEIAME", "tela.w"} {
		os.WriteFile(filepath.Join(dir, n), cp(t, "' Rotina de impressão\r\n"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0, 0xE7}, 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
	m, err := Prepare(dir)
	if err != nil || len(m.Converted) != 4 {
		t.Fatalf("esperava 4 arquivos protegidos, obtido %v (%v)", m.Converted, err)
	}
	if _, ok := m.Converted["logo.png"]; ok {
		t.Fatal("binário não pode ser convertido")
	}
	rep := Restore(dir, git(t, dir, "rev-parse", "HEAD"), m, Auto)
	if len(rep.Issues) > 0 || git(t, dir, "status", "--porcelain") != "" {
		t.Fatalf("restauração deveria deixar tudo idêntico: %v %q", rep.Issues, git(t, dir, "status", "--porcelain"))
	}
}

func TestHookModeConvertsOnlyTouchedFiles(t *testing.T) {
	dir := setupRepo(t)
	base := gitBase(t, dir)
	man := filepath.Join(t.TempDir(), "m.txt")
	// o agente abre só Permissao.cs
	if err := HookConvert(dir, man, filepath.Join(dir, "Permissao.cs")); err != nil {
		t.Fatal(err)
	}
	HookConvert(dir, man, filepath.Join(dir, "Permissao.cs")) // reabrir não duplica
	HookConvert(dir, man, "/fora/da/worktree.cs")             // ignorado
	u, _ := os.ReadFile(filepath.Join(dir, "Util.cs"))
	if Detect(u) != CP1252 {
		t.Fatal("arquivo não aberto pelo agente não deveria ser convertido")
	}
	simulateAgentEdit(t, filepath.Join(dir, "Permissao.cs"), "// ação padrão: negar", "// ação padrão: negar sempre")
	m := LoadManifest(man)
	if len(m.Converted) != 1 {
		t.Fatalf("manifesto: %+v", m.Converted)
	}
	rep := Restore(dir, base, m, Auto)
	got, _ := os.ReadFile(filepath.Join(dir, "Permissao.cs"))
	want := cp(t, strings.Replace(original, "// ação padrão: negar", "// ação padrão: negar sempre", 1))
	if !bytes.Equal(got, want) || len(rep.Issues) > 0 {
		t.Fatalf("%v\n%q", rep.Issues, got)
	}
	if st := git(t, dir, "status", "--porcelain"); st != "M Permissao.cs" {
		t.Fatalf("só o arquivo editado deveria aparecer: %q", st)
	}
}

func TestReadOnlyConvertedFileRestoredByteExact(t *testing.T) {
	dir := setupRepo(t)
	man := filepath.Join(t.TempDir(), "m.txt")
	HookConvert(dir, man, "Util.cs") // caminho relativo também é aceito
	Restore(dir, gitBase(t, dir), LoadManifest(man), Auto)
	if st := git(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("arquivo apenas lido deveria voltar idêntico: %q", st)
	}
}
