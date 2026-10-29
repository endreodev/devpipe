package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devpipe/internal/domain"
	"devpipe/internal/gitx"
	"devpipe/internal/provider"
	"devpipe/internal/store"
)

// cenarioPub monta servidor + clone com main e stage, e uma demanda já
// desenvolvida numa worktree própria — sem passar pelo agente, para que o
// teste cubra só a publicação e o envio para teste.
func cenarioPub(t *testing.T) (e *Engine, st *store.Store, p domain.Project, d domain.Demand, servidor string, g func(dir string, args ...string) string) {
	t.Helper()
	tmp := t.TempDir()
	servidor = filepath.Join(tmp, "servidor.git")
	repo := filepath.Join(tmp, "repo")
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
	g(tmp, "init", "-q", "-b", "main", repo)
	g(repo, "config", "user.email", "t@t")
	g(repo, "config", "user.name", "t")
	g(repo, "remote", "add", "origin", servidor)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\n"), 0o644)
	g(repo, "add", ".")
	g(repo, "commit", "-q", "-m", "um")
	g(repo, "push", "-q", "origin", "main")
	// stage evolui a mesma linha que a demanda vai mexer
	g(repo, "checkout", "-q", "-b", "stage")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\nda release\n"), 0o644)
	g(repo, "commit", "-qam", "stage: ajuste")
	g(repo, "push", "-q", "origin", "stage")
	g(repo, "checkout", "-q", "main")

	var err error
	st, err = store.Open(filepath.Join(tmp, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := domain.DefaultSettings()
	s.WorktreeRoot = filepath.Join(tmp, "worktrees")
	st.SaveSettings(s)
	e = New(st, provider.NewRegistry(), NewBus(), filepath.Join(tmp, "data"))

	p = domain.Project{Name: "Repo", Path: repo, AutoCommit: true, AutoPush: true}
	if err := st.SaveProject(&p); err != nil {
		t.Fatal(err)
	}
	d = domain.Demand{Ticket: "CH101010", Kind: domain.KindBug, Title: "Ajuste", ProjectID: p.ID, Provider: "generico", BaseBranch: "main"}
	if _, err := st.CreateDemand(&d, "faça"); err != nil {
		t.Fatal(err)
	}
	// o que a esteira teria feito: worktree na branch de trabalho + commit
	d.WorkBranch = gitx.BranchName(d.Kind, d.Ticket)
	d.WorktreePath = filepath.Join(s.WorktreeRoot, "wt")
	d.BaseCommit = g(repo, "rev-parse", "main")
	if _, err := (gitx.Repo{Dir: repo}).EnsureWorktree(d.WorktreePath, d.WorkBranch, "main"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d.WorktreePath, "a.txt"), []byte("1\nda demanda\n"), 0o644)
	if _, err := (gitx.Repo{Dir: d.WorktreePath}).CommitAll("CH101010: Ajuste"); err != nil {
		t.Fatal(err)
	}
	d.Stage, d.Status = domain.StageReview, domain.StatusIdle
	if err := st.UpdateDemandState(d); err != nil {
		t.Fatal(err)
	}
	return e, st, p, d, servidor, g
}

// Aprovar a revisão com "publicar ao aprovar" ligado leva a branch de
// desenvolvimento ao servidor — e nada mais: o checkout do usuário e a branch
// dele continuam onde estavam.
func TestAprovarPublicaABranchDeDesenvolvimento(t *testing.T) {
	e, st, p, d, servidor, g := cenarioPub(t)
	headAntes := g(p.Path, "rev-parse", "HEAD")

	msg, err := e.ApproveReview(d.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "publicada") {
		t.Fatalf("mensagem da aprovação: %q", msg)
	}
	got, _ := st.GetDemand(d.ID)
	if got.Stage != domain.StageDone || !got.Pushed {
		t.Fatalf("estado após aprovar: %s pushed=%v (%s)", got.Stage, got.Pushed, got.StatusMessage)
	}
	if refs := g(servidor, "for-each-ref", "--format=%(refname:short)", "refs/heads"); !strings.Contains(refs, d.WorkBranch) {
		t.Fatalf("a branch de desenvolvimento não chegou ao servidor: %q", refs)
	}
	if h := g(p.Path, "rev-parse", "HEAD"); h != headAntes {
		t.Fatalf("o checkout do usuário mudou: %s -> %s", headAntes, h)
	}
	if conteudo := g(p.Path, "show", "main:a.txt"); strings.Contains(conteudo, "da demanda") {
		t.Fatal("a branch main recebeu o trabalho da demanda — merge não é automático")
	}
}

// Depois de publicada, o envio para teste cria <TIPO>/<TICKET>_stage a partir
// da stage do servidor, com o código dos dois lados.
func TestEnvioParaTesteCriaBranchComOsDoisLados(t *testing.T) {
	e, st, p, d, servidor, g := cenarioPub(t)
	if _, err := e.ApproveReview(d.ID, ""); err != nil {
		t.Fatal(err)
	}
	msg, err := e.SendToTest(d.ID, "stage", "")
	if err != nil {
		t.Fatal(err)
	}
	nome := "BUG/CH101010_stage"
	if !strings.Contains(msg, nome) {
		t.Fatalf("mensagem do envio: %q", msg)
	}
	got, _ := st.GetDemand(d.ID)
	if got.TestBranch != nome || got.TestTarget != "stage" {
		t.Fatalf("demanda não registrou a branch de teste: %+v", got.TestBranch)
	}
	conteudo := g(p.Path, "show", nome+":a.txt")
	for _, quer := range []string{"da release", "da demanda"} {
		if !strings.Contains(conteudo, quer) {
			t.Fatalf("a branch de teste perdeu %q: %q", quer, conteudo)
		}
	}
	if strings.Contains(conteudo, "<<<<<<<") {
		t.Fatalf("a branch de teste ficou com marcação de conflito: %q", conteudo)
	}
	if refs := g(servidor, "for-each-ref", "--format=%(refname:short)", "refs/heads"); !strings.Contains(refs, nome) {
		t.Fatalf("a branch de teste não foi publicada: %q", refs)
	}
	// a stage de origem continua intacta no servidor
	if s := g(servidor, "show", "stage:a.txt"); strings.Contains(s, "da demanda") {
		t.Fatalf("a stage do servidor foi alterada: %q", s)
	}
}

// O nome da branch de teste pode ser trocado na tela; o trabalho da demanda
// entra como um commit único.
func TestEnvioParaTesteAceitaNomeEscolhido(t *testing.T) {
	e, st, p, d, _, g := cenarioPub(t)
	if _, err := e.ApproveReview(d.ID, ""); err != nil {
		t.Fatal(err)
	}
	// um segundo commit na demanda, para o squash ter o que reunir
	os.WriteFile(filepath.Join(d.WorktreePath, "extra.txt"), []byte("mais\n"), 0o644)
	if _, err := (gitx.Repo{Dir: d.WorktreePath}).CommitAll("CH101010: mais um"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PushWorkBranch(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SendToTest(d.ID, "stage", "NR/CH101010 teste homologação"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetDemand(d.ID)
	// o nome digitado é normalizado pelas regras de nome de referência
	if got.TestBranch != "NR/CH101010-teste-homologacao" {
		t.Fatalf("nome da branch de teste: %q", got.TestBranch)
	}
	if n := g(p.Path, "rev-list", "--count", "stage.."+got.TestBranch); n != "1" {
		t.Fatalf("esperava um commit único na branch de teste, obtido %s", n)
	}
	if msg := g(p.Path, "log", "-1", "--format=%s", got.TestBranch); msg != "CH101010: Ajuste" {
		t.Fatalf("mensagem do commit único: %q", msg)
	}
	if arqs := g(p.Path, "show", "--name-only", "--format=", got.TestBranch); !strings.Contains(arqs, "extra.txt") || !strings.Contains(arqs, "a.txt") {
		t.Fatalf("o commit único não reuniu os dois commits: %q", arqs)
	}
}

// Sem publicar não se envia para teste: ninguém mais enxerga o commit.
func TestEnvioParaTesteExigePublicacao(t *testing.T) {
	e, _, _, d, _, _ := cenarioPub(t)
	if _, err := e.SendToTest(d.ID, "stage", ""); err == nil || !strings.Contains(err.Error(), "Publique") && !strings.Contains(err.Error(), "publique") {
		t.Fatalf("esperava recusa por falta de publicação, obtido: %v", err)
	}
}

// Origem inexistente é recusada antes de criar qualquer coisa.
func TestEnvioParaTesteRecusaOrigemDesconhecida(t *testing.T) {
	e, _, _, d, _, _ := cenarioPub(t)
	if _, err := e.ApproveReview(d.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SendToTest(d.ID, "nao-existe", ""); err == nil {
		t.Fatal("esperava erro para branch de origem inexistente")
	}
}

// Modo "direto na branch" com a branch aberta no checkout: ao publicar, o
// DevPipe commita SOMENTE os arquivos da demanda. O que o usuário alterou em
// outros arquivos continua pendente, intocado.
func TestPublicarCommitaSoOsArquivosDaDemandaNoCheckout(t *testing.T) {
	e, st, p, _, servidor, g := cenarioPub(t)
	repo := p.Path
	// demanda direto na branch main, que está em checkout no repositório
	d := domain.Demand{Ticket: "CH777", Kind: domain.KindBug, Title: "Direto", ProjectID: p.ID, Provider: "generico", BaseBranch: "main", DirectBranch: true}
	if _, err := st.CreateDemand(&d, "faça"); err != nil {
		t.Fatal(err)
	}
	d.WorkBranch = "main"
	d.BaseCommit = g(repo, "rev-parse", "main")
	d.WorktreePath = filepath.Join(filepath.Dir(p.Path), "wt777")
	if _, err := (gitx.Repo{Dir: repo}).EnsureDetachedWorktree(d.WorktreePath, d.BaseCommit); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d.WorktreePath, "demanda.txt"), []byte("da demanda\n"), 0o644)
	if _, err := (gitx.Repo{Dir: d.WorktreePath}).CommitAll("CH777: Direto"); err != nil {
		t.Fatal(err)
	}
	d.Stage, d.Status = domain.StageReview, domain.StatusIdle
	if err := st.UpdateDemandState(d); err != nil {
		t.Fatal(err)
	}
	// alteração local do usuário em OUTRO arquivo: não pode entrar no commit
	os.WriteFile(filepath.Join(repo, "meu.txt"), []byte("trabalho do usuario\n"), 0o644)

	msg, err := e.ApproveReview(d.ID, "") // o projeto publica ao aprovar
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "publicada") {
		t.Fatalf("mensagem: %q", msg)
	}
	// o commit levou só o arquivo da demanda
	if arquivos := g(repo, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(arquivos) != "demanda.txt" {
		t.Fatalf("o commit levou arquivos demais: %q", arquivos)
	}
	// a alteração do usuário continua pendente
	if st := g(repo, "status", "--porcelain"); !strings.Contains(st, "meu.txt") {
		t.Fatalf("a alteração do usuário sumiu do pendente: %q", st)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "meu.txt")); string(b) != "trabalho do usuario\n" {
		t.Fatalf("a alteração do usuário foi alterada: %q", b)
	}
	if refs := g(servidor, "for-each-ref", "--format=%(refname:short)", "refs/heads"); !strings.Contains(refs, "main") {
		t.Fatalf("a branch não foi publicada: %q", refs)
	}
	// republicar não é erro: o trabalho já está na branch
	if _, err := e.PushWorkBranch(d.ID); err != nil {
		t.Fatalf("republicar: %v", err)
	}
}
