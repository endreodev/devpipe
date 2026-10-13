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

// Branch selecionada = branch em checkout no repositório principal:
// nenhuma branch nova e o checkout é atualizado por fast-forward.
func TestDirectBranchCheckedOut(t *testing.T) {
	e, st, p := setup(t, fakeAgent)
	branchesAntes := gitOut(t, p.Path, "branch", "--list")
	d := domain.Demand{Ticket: "CH-7", Title: "Direto", ProjectID: p.ID, Provider: "generico", DirectBranch: true}
	if _, err := st.CreateDemand(&d, "Criar feature.txt contendo ok"); err != nil {
		t.Fatal(err)
	}
	e.Enqueue(d.ID)
	got := waitFor(t, st, d.ID, func(x domain.Demand) bool {
		return (x.Stage == domain.StageReview || x.Stage == domain.StageBlocked) && !e.IsRunning(x.ID)
	})
	if got.Stage != domain.StageReview || got.WorkBranch != "main" || !got.DirectBranch || got.BaseCommit == "" {
		t.Fatalf("estado: %s %s branch=%s base=%s", got.Stage, got.StatusMessage, got.WorkBranch, got.BaseCommit)
	}
	if b := gitOut(t, p.Path, "branch", "--list"); b != branchesAntes {
		t.Fatalf("branch nova criada: %q", b)
	}
	b, err := os.ReadFile(filepath.Join(p.Path, "feature.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("o commit não chegou ao checkout da branch selecionada: %q %v", b, err)
	}
	if gitOut(t, p.Path, "status", "--porcelain") != "" {
		t.Fatal("checkout principal ficou sujo após o fast-forward")
	}
	runs, _ := st.ListRuns(d.ID)
	if !strings.Contains(runs[0].DiffStat, "feature.txt") {
		t.Fatalf("diff deveria comparar com o commit inicial: %q", runs[0].DiffStat)
	}
	// ajuste: novo commit na mesma branch
	rev, _ := e.NewAdjustment(d.ID, "AJUSTE-EXTRA")
	e.FinishAdjustment(d.ID, rev, true)
	got = waitFor(t, st, d.ID, func(x domain.Demand) bool {
		return x.CurrentRevision == 2 && (x.Stage == domain.StageReview || x.Stage == domain.StageBlocked) && !e.IsRunning(x.ID)
	})
	b, _ = os.ReadFile(filepath.Join(p.Path, "feature.txt"))
	if got.Stage != domain.StageReview || !strings.Contains(string(b), "extra") {
		t.Fatalf("ajuste não aplicado na branch: %s %q", got.StatusMessage, b)
	}
	if n := gitOut(t, p.Path, "rev-list", "--count", got.BaseCommit+"..main"); n != "2" {
		t.Fatalf("esperava 2 commits da demanda em main, obtido %s", n)
	}
	e.ApproveReview(d.ID, "")
	got, _ = st.GetDemand(d.ID)
	if !strings.Contains(got.StatusMessage, "aplicadas na branch main") {
		t.Fatal(got.StatusMessage)
	}
}

// Branch selecionada que não está em checkout: avança a ref; checkout intacto.
func TestDirectBranchNotCheckedOut(t *testing.T) {
	e, st, p := setup(t, fakeAgent)
	gitOut(t, p.Path, "branch", "release")
	d := domain.Demand{Ticket: "CH-8", Title: "Release", ProjectID: p.ID, Provider: "generico", DirectBranch: true, BaseBranch: "release"}
	st.CreateDemand(&d, "Criar feature.txt contendo ok")
	e.Enqueue(d.ID)
	got := waitFor(t, st, d.ID, func(x domain.Demand) bool {
		return (x.Stage == domain.StageReview || x.Stage == domain.StageBlocked) && !e.IsRunning(x.ID)
	})
	if got.Stage != domain.StageReview || got.WorkBranch != "release" {
		t.Fatalf("%s %s %s", got.Stage, got.WorkBranch, got.StatusMessage)
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
	// a branch continua livre para checkout normal pelo usuário
	gitOut(t, p.Path, "checkout", "-q", "release")
}
