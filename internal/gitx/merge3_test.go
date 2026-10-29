package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devpipe/internal/gittest"
)

func init() { gittest.SemAutoCRLF() }

func TestMergeWorkingTreeFrom(t *testing.T) {
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main")
	wt := filepath.Join(tmp, "wt")
	os.MkdirAll(main, 0o755)
	run := func(dir string, args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(dir, name, s string) { os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644) }
	read := func(dir, name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	run(main, "init", "-q", "-b", "main")
	run(main, "config", "user.email", "t@t")
	run(main, "config", "user.name", "t")
	write(main, "a.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	write(main, "b.txt", "x\n")
	write(main, "del.txt", "apagar\n")
	run(main, "add", ".")
	run(main, "commit", "-q", "-m", "init")
	base := run(main, "rev-parse", "HEAD")
	run(main, "worktree", "add", "-q", "--detach", wt, base)

	// demanda: altera linha 2 de a.txt, linha de b.txt, cria novo.txt, apaga del.txt
	write(wt, "a.txt", "1\nDEMANDA\n3\n4\n5\n6\n7\n8\n9\n")
	write(wt, "b.txt", "demanda\n")
	write(wt, "novo.txt", "novo\n")
	os.Remove(filepath.Join(wt, "del.txt"))
	run(wt, "add", "-A")
	// usuário: altera linha 8 de a.txt e a mesma linha de b.txt
	write(main, "a.txt", "1\n2\n3\n4\n5\n6\n7\nLOCAL\n9\n")
	write(main, "b.txt", "local\n")

	res, err := Repo{Dir: main}.MergeWorkingTreeFrom(Repo{Dir: wt}, base, "CH101010")
	if err != nil {
		t.Fatal(err)
	}
	if a := read(main, "a.txt"); a != "1\nDEMANDA\n3\n4\n5\n6\n7\nLOCAL\n9\n" {
		t.Fatalf("merge limpo de a.txt: %q", a)
	}
	if b := read(main, "b.txt"); !strings.Contains(b, "<<<<<<<") || !strings.Contains(b, "local") || !strings.Contains(b, "demanda") {
		t.Fatalf("b.txt deveria ter marcação de conflito: %q", b)
	}
	if read(main, "novo.txt") != "novo\n" {
		t.Fatal("arquivo novo não criado")
	}
	if _, err := os.Stat(filepath.Join(main, "del.txt")); err == nil {
		t.Fatal("del.txt deveria ter sido excluído")
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "b.txt" || len(res.Changed) != 3 {
		t.Fatalf("resultado: %+v", res)
	}
	if st := run(main, "diff", "--cached", "--name-only"); st != "" {
		t.Fatalf("não deveria mexer no índice: %q", st)
	}
	// reaplicar não muda nada (b.txt com conflito pendente fica como está)
	antes := read(main, "b.txt")
	res, _ = Repo{Dir: main}.MergeWorkingTreeFrom(Repo{Dir: wt}, base, "CH101010")
	if len(res.Same) != 3 || len(res.Kept) != 1 || len(res.Changed)+len(res.Conflicts) != 0 {
		t.Fatalf("reaplicação: %+v", res)
	}
	if read(main, "b.txt") != antes {
		t.Fatal("b.txt não deveria mudar na reaplicação")
	}
}

// A marcação de conflito fica DENTRO do arquivo do usuário e vai para o commit
// de quem resolver: ela identifica a demanda, nunca a ferramenta.
func TestMarcacaoDeConflitoNaoCitaAFerramenta(t *testing.T) {
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main")
	wt := filepath.Join(tmp, "wt")
	os.MkdirAll(main, 0o755)
	run := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(main, "init", "-q", "-b", "main")
	run(main, "config", "user.email", "t@t")
	run(main, "config", "user.name", "t")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("original\n"), 0o644)
	run(main, "add", ".")
	run(main, "commit", "-q", "-m", "init")
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = main
	out, _ := c.Output()
	base := strings.TrimSpace(string(out))
	run(main, "worktree", "add", "-q", "--detach", wt, base)

	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("da demanda\n"), 0o644)
	run(wt, "add", "-A")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("minha alteracao\n"), 0o644)

	if _, err := (Repo{Dir: main}).MergeWorkingTreeFrom(Repo{Dir: wt}, base, "CH101010"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(main, "a.txt"))
	txt := string(b)
	if !strings.Contains(txt, "<<<<<<<") {
		t.Fatalf("esperava marcação de conflito: %q", txt)
	}
	if strings.Contains(strings.ToLower(txt), "devpipe") {
		t.Fatalf("a marcação cita a ferramenta: %q", txt)
	}
	if !strings.Contains(txt, "CH101010") {
		t.Fatalf("a marcação deveria identificar a demanda: %q", txt)
	}
}
