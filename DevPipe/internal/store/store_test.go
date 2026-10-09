package store

import (
	"path/filepath"
	"testing"

	"devpipe/internal/domain"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProjectDemandRevisionLifecycle(t *testing.T) {
	s := newStore(t)
	p := domain.Project{Name: "Demo", Path: "/tmp/demo", BranchOptions: []string{"main", "stage"}, AutoCommit: true}
	if err := s.SaveProject(&p); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProject(&domain.Project{Name: "Dup", Path: "/tmp/demo"}); err == nil {
		t.Fatal("esperava erro de caminho duplicado")
	}
	got, err := s.GetProject(p.ID)
	if err != nil || got.BranchOptions[1] != "stage" || !got.AutoCommit || got.CreatedAt.IsZero() {
		t.Fatalf("projeto inválido: %+v %v", got, err)
	}

	d := domain.Demand{Ticket: "T-1", Title: "Teste", ProjectID: p.ID, Provider: "claude", Priority: 2}
	rev, err := s.CreateDemand(&d, "faça X")
	if err != nil || rev.Number != 1 || d.Stage != domain.StageBacklog {
		t.Fatalf("criação: %+v %v", rev, err)
	}
	d2 := domain.Demand{Ticket: "T-2", Title: "Outra", ProjectID: p.ID, Provider: "claude", Priority: 1}
	s.CreateDemand(&d2, "")
	list, _ := s.ListDemands(DemandFilter{})
	if len(list) != 2 || list[0].Ticket != "T-2" {
		t.Fatalf("ordenação por prioridade falhou: %+v", list)
	}
	list, _ = s.ListDemands(DemandFilter{Query: "Teste"})
	if len(list) != 1 {
		t.Fatalf("busca falhou")
	}

	r2, _ := s.AddRevision(d.ID, "ajuste Y")
	if r2.Number != 2 || r2.Kind != "ajuste" {
		t.Fatalf("revisão: %+v", r2)
	}
	s.SetRevisionSpec(rev.ID, "# spec", true)
	revs, _ := s.ListRevisions(d.ID)
	if len(revs) != 2 || revs[0].Spec != "# spec" || !revs[0].SpecApproved || revs[0].Instructions != "faça X" {
		t.Fatalf("revisões: %+v", revs)
	}

	run := domain.Run{DemandID: d.ID, RevisionNum: 1, Provider: "claude", Status: domain.StatusRunning, Stage: domain.StageDev}
	s.CreateRun(&run)
	ids, _ := s.MarkInterruptedRuns()
	if len(ids) != 1 || ids[0] != d.ID {
		t.Fatalf("recuperação: %v", ids)
	}
	runs, _ := s.ListRuns(d.ID)
	if runs[0].Status != domain.StatusCancelled || runs[0].FinishedAt == nil {
		t.Fatalf("run: %+v", runs[0])
	}

	e := domain.Event{DemandID: d.ID, Level: domain.LevelInfo, Kind: "x", Message: "olá"}
	s.AddEvent(&e)
	evs, _ := s.ListEvents(EventFilter{DemandID: d.ID})
	if len(evs) != 1 || evs[0].Time.IsZero() {
		t.Fatalf("eventos: %+v", evs)
	}
	if _, err := s.Stats(); err != nil {
		t.Fatal(err)
	}
}

func TestRuleVersioningAndSettings(t *testing.T) {
	s := newStore(t)
	r := domain.Rule{Scope: "global", Title: "Nomes", Content: "PascalCase", Enabled: true}
	s.SaveRule(&r)
	r.Content = "PascalCase em classes"
	s.SaveRule(&r)
	if r.Version != 2 {
		t.Fatalf("versão esperada 2, obtida %d", r.Version)
	}
	st, _ := s.GetSettings()
	st.MaxParallel = 3
	st.AuthorizedRoots = []string{"C:/repos"}
	s.SaveSettings(st)
	st2, _ := s.GetSettings()
	if st2.MaxParallel != 3 || st2.AuthorizedRoots[0] != "C:/repos" || st2.Claude.Binary != "claude" {
		t.Fatalf("settings: %+v", st2)
	}
}
