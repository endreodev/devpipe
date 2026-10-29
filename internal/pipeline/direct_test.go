package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devpipe/internal/domain"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func emRevisao(e *Engine, rev int) func(domain.Demand) bool {
	return func(x domain.Demand) bool {
		return x.CurrentRevision == rev && (x.Stage == domain.StageReview || x.Stage == domain.StageBlocked) && !e.IsRunning(x.ID)
	}
}

// Branch selecionada em checkout (ex.: aberta no Visual Studio): ao aprovar,
// as alterações aparecem no checkout como alterações pendentes (Git Changes).
func TestDirectBranchCheckedOut(t *testing.T) {
	e, st, p := setup(t, fakeAgent)
	branchesAntes := gitOut(t, p.Path, "branch", "--list")
	headAntes := gitOut(t, p.Path, "rev-parse", "HEAD")
	// alteração local do usuário em outro arquivo: deve ser preservada
	os.WriteFile(filepath.Join(p.Path, "README.md"), []byte("demo\nminha alteração\n"), 0o644)

	d := domain.Demand{Ticket: "CH-7", Kind: domain.KindNR, Title: "Direto", ProjectID: p.ID, Provider: "generico", DirectBranch: true}
	st.CreateDemand(&d, "Criar feature.txt contendo ok")
	e.Enqueue(d.ID)
	got := waitFor(t, st, d.ID, emRevisao(e, 1))
	if got.Stage != domain.StageReview || got.WorkBranch != "main" || got.BaseCommit == "" {
		t.Fatalf("estado: %s %s branch=%s", got.Stage, got.StatusMessage, got.WorkBranch)
	}
	if _, err := os.Stat(filepath.Join(p.Path, "feature.txt")); err == nil {
		t.Fatal("antes da aprovação o checkout não deve mudar")
	}
	// ajuste antes de aprovar
	rev, _ := e.NewAdjustment(d.ID, "AJUSTE-EXTRA")
	e.FinishAdjustment(d.ID, rev, true)
	got = waitFor(t, st, d.ID, emRevisao(e, 2))
	if got.Stage != domain.StageReview {
		t.Fatalf("ajuste: %s", got.StatusMessage)
	}
	if _, err := e.ApproveReview(d.ID, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(p.Path, "feature.txt"))
	if !strings.Contains(string(b), "ok") || !strings.Contains(string(b), "extra") {
		t.Fatalf("alterações não aplicadas no checkout: %q", b)
	}
	if gitOut(t, p.Path, "rev-parse", "HEAD") != headAntes || gitOut(t, p.Path, "branch", "--list") != branchesAntes {
		t.Fatal("não deveria criar commit nem branch no checkout do usuário")
	}
	if st := gitOut(t, p.Path, "status", "--porcelain"); !strings.Contains(st, "?? feature.txt") || !strings.Contains(st, "M README.md") {
		t.Fatalf("esperava alterações pendentes (Git Changes): %q", st)
	}
	if r, _ := os.ReadFile(filepath.Join(p.Path, "README.md")); !strings.Contains(string(r), "minha alteração") {
		t.Fatal("alteração local do usuário perdida")
	}
	got, _ = st.GetDemand(d.ID)
	if got.Stage != domain.StageDone || !strings.Contains(got.StatusMessage, "alterações pendentes") {
		t.Fatalf("%s %s", got.Stage, got.StatusMessage)
	}
	// reaplicar é seguro
	if msg, err := e.ApplyToBranch(d.ID); err != nil || !strings.Contains(msg, "já estavam") {
		t.Fatalf("reaplicação: %q %v", msg, err)
	}
}

// Alteração local no mesmo arquivo/trecho: aplica mesmo assim (merge de 3
// vias), deixando a marcação de conflito no arquivo, e a demanda é concluída.
func TestDirectBranchConflict(t *testing.T) {
	e, st, p := setup(t, fakeAgent)
	headAntes := gitOut(t, p.Path, "rev-parse", "HEAD")
	d := domain.Demand{Ticket: "CH-9", Kind: domain.KindBug, Title: "Conflito", ProjectID: p.ID, Provider: "generico", DirectBranch: true}
	st.CreateDemand(&d, "Criar feature.txt contendo ok")
	e.Enqueue(d.ID)
	waitFor(t, st, d.ID, emRevisao(e, 1))
	os.WriteFile(filepath.Join(p.Path, "feature.txt"), []byte("meu\n"), 0o644)
	if _, err := e.ApproveReview(d.ID, ""); err != nil {
		t.Fatalf("deveria aplicar mesmo com alterações locais: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(p.Path, "feature.txt"))
	if s := string(b); !strings.Contains(s, "<<<<<<<") || !strings.Contains(s, "meu") || !strings.Contains(s, "ok") {
		t.Fatalf("esperava marcação de conflito com as duas versões: %q", s)
	}
	if gitOut(t, p.Path, "rev-parse", "HEAD") != headAntes {
		t.Fatal("não deveria criar commit no checkout do usuário")
	}
	got, _ := st.GetDemand(d.ID)
	if got.Stage != domain.StageDone || !strings.Contains(got.StatusMessage, "conflito") {
		t.Fatalf("%s %s", got.Stage, got.StatusMessage)
	}
}

// Branch fora de checkout: ao aprovar, commit + fast-forward da própria branch.
func TestDirectBranchNotCheckedOut(t *testing.T) {
	e, st, p := setup(t, fakeAgent)
	gitOut(t, p.Path, "branch", "release")
	d := domain.Demand{Ticket: "CH-8", Kind: domain.KindFix, Title: "Release", ProjectID: p.ID, Provider: "generico", DirectBranch: true, BaseBranch: "release"}
	st.CreateDemand(&d, "Criar feature.txt contendo ok")
	e.Enqueue(d.ID)
	got := waitFor(t, st, d.ID, emRevisao(e, 1))
	if got.Stage != domain.StageReview || got.WorkBranch != "release" {
		t.Fatalf("%s %s %s", got.Stage, got.WorkBranch, got.StatusMessage)
	}
	if _, err := e.ApproveReview(d.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Path, "feature.txt")); err == nil {
		t.Fatal("o checkout de main não deveria mudar")
	}
	if out := gitOut(t, p.Path, "show", "release:feature.txt"); out != "ok" {
		t.Fatalf("release não recebeu o commit: %q", out)
	}
	if b := gitOut(t, p.Path, "branch", "--list"); strings.Count(b, "\n") != 1 {
		t.Fatalf("branches inesperadas: %q", b)
	}
	gitOut(t, p.Path, "checkout", "-q", "release")
}
