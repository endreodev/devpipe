package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeBranchName(t *testing.T) {
	got := SafeBranchName("devpipe/", "CH-1001", "Cupom de desconto: ação ~ especial!! no pedido final")
	if got != "devpipe/ch-1001-cupom-de-desconto-acao-especial" {
		t.Fatalf("obtido %q", got)
	}
	if SafeBranchName("x/", "", "") != "x/demanda" {
		t.Fatal("nome vazio")
	}
}

func TestCommitAuthor(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Usuario Repo")
	run("config", "user.email", "repo@exemplo.local")
	r := Repo{Dir: dir}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1"), 0o644)
	if _, err := r.CommitAll("primeiro"); err != nil {
		t.Fatal(err)
	}
	if a := run("log", "-1", "--format=%an <%ae>"); a != "Usuario Repo <repo@exemplo.local>" {
		t.Fatalf("sem configuração deve usar o Git do repositório: %s", a)
	}
	CommitAuthorName, CommitAuthorEmail = "fulano.tal", "fulano@empresa.local"
	defer func() { CommitAuthorName, CommitAuthorEmail = "", "" }()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("2"), 0o644)
	r.CommitAll("segundo")
	if a := run("log", "-1", "--format=%an <%ae> | %cn"); a != "fulano.tal <fulano@empresa.local> | fulano.tal" {
		t.Fatalf("autor configurado: %s", a)
	}
}
