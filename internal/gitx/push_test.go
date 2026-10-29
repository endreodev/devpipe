package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cenario monta um servidor (repositório nu) e um clone com um commit em main.
func cenario(t *testing.T) (servidor, clone string, g func(dir string, args ...string) string) {
	t.Helper()
	tmp := t.TempDir()
	servidor = filepath.Join(tmp, "servidor.git")
	clone = filepath.Join(tmp, "clone")
	g = func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v em %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	g(tmp, "init", "-q", "--bare", "-b", "main", servidor)
	g(tmp, "init", "-q", "-b", "main", clone)
	g(clone, "config", "user.email", "t@t")
	g(clone, "config", "user.name", "t")
	g(clone, "remote", "add", "origin", servidor)
	os.WriteFile(filepath.Join(clone, "a.txt"), []byte("1\n"), 0o644)
	g(clone, "add", ".")
	g(clone, "commit", "-q", "-m", "um")
	g(clone, "push", "-q", "origin", "main")
	return servidor, clone, g
}

// O push publica SOMENTE a branch pedida: nenhuma outra branch local vai
// junto, nem por engano.
func TestPushPublicaSomenteABranchPedida(t *testing.T) {
	servidor, clone, g := cenario(t)
	g(clone, "branch", "experiencia-local") // não pode aparecer no servidor
	g(clone, "checkout", "-q", "-b", "BUG/CH101010_Desenvolvimento")
	os.WriteFile(filepath.Join(clone, "b.txt"), []byte("ok\n"), 0o644)
	g(clone, "add", ".")
	g(clone, "commit", "-q", "-m", "CH101010: trabalho")

	r := Repo{Dir: clone}
	res := r.Push("BUG/CH101010_Desenvolvimento")
	if !res.Done || res.Error != "" || res.Remote != "origin" {
		t.Fatalf("push falhou: %+v", res)
	}
	refs := g(servidor, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if !strings.Contains(refs, "BUG/CH101010_Desenvolvimento") {
		t.Fatalf("a branch não chegou ao servidor: %q", refs)
	}
	if strings.Contains(refs, "experiencia-local") {
		t.Fatalf("o push levou outra branch junto: %q", refs)
	}
	// repetir não é erro: o servidor já está no mesmo commit
	if res := r.Push("BUG/CH101010_Desenvolvimento"); !res.Done || !res.Already {
		t.Fatalf("segundo push: %+v", res)
	}
}

// Sem remoto configurado não é erro: não há onde publicar.
func TestPushSemRemoto(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command("git", "init", "-q", "-b", "main")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	if res := (Repo{Dir: dir}).Push("main"); !res.Skipped || res.Error != "" {
		t.Fatalf("esperava skipped, obtido %+v", res)
	}
}

// O push nunca reescreve o servidor: se a branch remota recebeu outro commit,
// o Git recusa e o histórico de lá continua intacto.
func TestPushNaoReescreveOServidor(t *testing.T) {
	servidor, clone, g := cenario(t)
	g(clone, "checkout", "-q", "-b", "BUG/CH1_Desenvolvimento")
	os.WriteFile(filepath.Join(clone, "b.txt"), []byte("ok\n"), 0o644)
	g(clone, "add", ".")
	g(clone, "commit", "-q", "-m", "CH1: trabalho")
	if res := (Repo{Dir: clone}).Push("BUG/CH1_Desenvolvimento"); !res.Done {
		t.Fatalf("primeiro push: %+v", res)
	}
	// outra pessoa publica por cima da mesma branch
	outro := filepath.Join(t.TempDir(), "outro")
	g(filepath.Dir(outro), "clone", "-q", "-b", "BUG/CH1_Desenvolvimento", servidor, outro)
	g(outro, "config", "user.email", "o@o")
	g(outro, "config", "user.name", "o")
	os.WriteFile(filepath.Join(outro, "c.txt"), []byte("deles\n"), 0o644)
	g(outro, "add", ".")
	g(outro, "commit", "-q", "-m", "trabalho de outra pessoa")
	g(outro, "push", "-q", "origin", "BUG/CH1_Desenvolvimento")
	deles := g(servidor, "rev-parse", "refs/heads/BUG/CH1_Desenvolvimento")

	// a demanda segue e tenta publicar de novo: tem de ser recusada
	os.WriteFile(filepath.Join(clone, "b.txt"), []byte("ok2\n"), 0o644)
	g(clone, "commit", "-qam", "CH1: mais")
	res := (Repo{Dir: clone}).Push("BUG/CH1_Desenvolvimento")
	if res.Done || res.Error == "" {
		t.Fatalf("o push deveria ter sido recusado: %+v", res)
	}
	if agora := g(servidor, "rev-parse", "refs/heads/BUG/CH1_Desenvolvimento"); agora != deles {
		t.Fatalf("o servidor foi alterado: %s -> %s", deles, agora)
	}
}
