package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A convenção da equipe é <TIPO>/<TICKET>_Desenvolvimento e o ticket vai como
// foi digitado — só os caracteres que o Git recusa viram "-".
func TestBranchName(t *testing.T) {
	casos := []struct{ kind, ticket, quer string }{
		{"BUG", "CH101010", "BUG/CH101010_Desenvolvimento"},
		{"FIX", "CH-101010", "FIX/CH-101010_Desenvolvimento"},
		{"NR", "ch 101010", "NR/ch-101010_Desenvolvimento"},
		{"BUG", "101010", "BUG/101010_Desenvolvimento"},
		{"NR", "CH/101010", "NR/CH-101010_Desenvolvimento"},      // barra não cria subnível
		{"BUG", "CH~1010:1?", "BUG/CH-1010-1_Desenvolvimento"},   // caracteres recusados pelo Git
		{"FIX", "ação 55", "FIX/acao-55_Desenvolvimento"},        // acento
		{"BUG", "", "BUG/demanda_Desenvolvimento"},               // ticket vazio
		{"", "CH101010", "CH101010_Desenvolvimento"},             // sem tipo (demanda legada)
		{"BUG", "CH..1010", "BUG/CH-1010_Desenvolvimento"},       // ".." é proibido em refs
		{"BUG", "CH101010.lock", "BUG/CH101010_Desenvolvimento"}, // sufixo reservado
	}
	for _, c := range casos {
		if got := BranchName(c.kind, c.ticket); got != c.quer {
			t.Errorf("BranchName(%q, %q) = %q; queria %q", c.kind, c.ticket, got, c.quer)
		}
	}
}

// O nome gerado precisa ser aceito pelo próprio Git, não só pelo nosso regex.
func TestBranchNameAceitaPeloGit(t *testing.T) {
	for _, tk := range []string{"CH101010", "CH-101010", "ch 101010", "CH~1010:1?", "ação 55", "CH..1010"} {
		name := BranchName("BUG", tk)
		c := exec.Command("git", "check-ref-format", "--branch", name)
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("git recusou %q (de %q): %v %s", name, tk, err, out)
		}
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

func TestDefaultBasePreferindoMain(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "desenvolvimento")
	run("config", "user.name", "Teste")
	run("config", "user.email", "teste@exemplo.local")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1"), 0o644)
	run("add", "-A")
	run("commit", "-qm", "inicial")
	r := Repo{Dir: dir}

	// Sem main nem master: cai na branch em checkout.
	if b, err := r.DefaultBase(); err != nil || b != "desenvolvimento" {
		t.Fatalf("sem main deveria usar a branch atual, obtido %q (%v)", b, err)
	}
	// master existe: é a segunda preferência.
	run("branch", "master")
	if b, _ := r.DefaultBase(); b != "master" {
		t.Fatalf("esperava master, obtido %q", b)
	}
	// main existe: sempre ganha, mesmo sem estar em checkout.
	run("branch", "main")
	if b, _ := r.DefaultBase(); b != "main" {
		t.Fatalf("esperava main, obtido %q", b)
	}
	if cur, _ := r.CurrentBranch(); cur != "desenvolvimento" {
		t.Fatalf("o checkout do usuário não pode mudar: %q", cur)
	}
}

// Fetch atualiza as refs remotas sem encostar na branch local nem na árvore de
// trabalho — é o que substitui o `git pull` proibido pela política da esteira.
func TestFetchAtualizaRefsSemTocarNoCheckout(t *testing.T) {
	tmp := t.TempDir()
	origem := filepath.Join(tmp, "origem")
	clone := filepath.Join(tmp, "clone")
	os.MkdirAll(origem, 0o755)
	g := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	g(origem, "init", "-q", "-b", "main")
	g(origem, "config", "user.email", "t@t")
	g(origem, "config", "user.name", "t")
	os.WriteFile(filepath.Join(origem, "a.txt"), []byte("1\n"), 0o644)
	g(origem, "add", ".")
	g(origem, "commit", "-q", "-m", "um")
	g(tmp, "clone", "-q", origem, clone)

	r := Repo{Dir: clone}
	// o clone está em dia
	if bs, _ := r.Branches(); len(bs) == 0 || bs[0].Behind != 0 {
		t.Fatalf("clone recém-feito deveria estar em dia: %+v", bs)
	}
	// alteração local pendente: o fetch não pode encostar nela
	os.WriteFile(filepath.Join(clone, "local.txt"), []byte("meu trabalho\n"), 0o644)

	// a origem avança
	os.WriteFile(filepath.Join(origem, "a.txt"), []byte("1\n2\n"), 0o644)
	g(origem, "commit", "-qam", "dois")

	if res := r.Fetch(); !res.Done {
		t.Fatalf("fetch falhou: %+v", res)
	}
	bs, err := r.Branches()
	if err != nil {
		t.Fatal(err)
	}
	var main BranchInfo
	for _, b := range bs {
		if b.Name == "main" {
			main = b
		}
	}
	if main.Behind != 1 || main.Ahead != 0 || main.Upstream != "origin/main" {
		t.Fatalf("esperava 1 commit atrás de origin/main, obtido %+v", main)
	}
	// a branch local NÃO avançou e o arquivo local continua intacto
	if b, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(b) != "1\n" {
		t.Fatalf("o fetch alterou a árvore de trabalho: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "local.txt")); string(b) != "meu trabalho\n" {
		t.Fatalf("o fetch mexeu em alteração local: %q", b)
	}
}

// Sem remoto configurado o fetch não é erro: só não há o que atualizar.
func TestFetchSemRemoto(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command("git", "init", "-q", "-b", "main")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	if res := (Repo{Dir: dir}).Fetch(); !res.Skipped || res.Error != "" {
		t.Fatalf("esperava skipped, obtido %+v", res)
	}
}

// Quando a branch local está atrasada, a remota correspondente PRECISA
// continuar na lista: é ela que permite iniciar a demanda do código atual do
// servidor sem alterar o checkout. Em dia, a remota é omitida (redundante).
func TestBranchesMantemRemotaQuandoLocalAtrasada(t *testing.T) {
	tmp := t.TempDir()
	origem := filepath.Join(tmp, "origem")
	clone := filepath.Join(tmp, "clone")
	os.MkdirAll(origem, 0o755)
	g := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	g(origem, "init", "-q", "-b", "main")
	g(origem, "config", "user.email", "t@t")
	g(origem, "config", "user.name", "t")
	os.WriteFile(filepath.Join(origem, "a.txt"), []byte("1\n"), 0o644)
	g(origem, "add", ".")
	g(origem, "commit", "-q", "-m", "um")
	g(tmp, "clone", "-q", origem, clone)
	r := Repo{Dir: clone}

	tem := func(nome string) bool {
		bs, err := r.Branches()
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bs {
			if b.Name == nome {
				return true
			}
		}
		return false
	}
	if tem("origin/main") {
		t.Fatal("em dia, origin/main é redundante e não deveria aparecer")
	}
	os.WriteFile(filepath.Join(origem, "a.txt"), []byte("1\n2\n"), 0o644)
	g(origem, "commit", "-qam", "dois")
	if res := r.Fetch(); !res.Done {
		t.Fatalf("fetch: %+v", res)
	}
	if !tem("origin/main") {
		t.Fatal("com a local atrasada, origin/main precisa estar disponível como base")
	}
}
