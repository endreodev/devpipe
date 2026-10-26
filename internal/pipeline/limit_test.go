package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"devpipe/internal/domain"
	"devpipe/internal/provider"
)

type limitProvider struct{ calls atomic.Int32 }

func (l *limitProvider) ID() string           { return "limite" }
func (l *limitProvider) Name() string         { return "limite" }
func (l *limitProvider) SupportsImages() bool { return false }
func (l *limitProvider) Check(context.Context) provider.Availability {
	return provider.Availability{OK: true}
}
func (l *limitProvider) Run(ctx context.Context, r provider.Request, emit func(provider.Event)) (provider.Result, error) {
	l.calls.Add(1)
	return provider.Result{}, &provider.ErrPlanLimit{ResetsAt: time.Now().Add(2 * time.Hour)}
}

func TestPlanLimitPausesQueueAndRequeues(t *testing.T) {
	e, st, p := setup(t, "true")
	lp := &limitProvider{}
	e.reg.Register(lp)
	d := domain.Demand{Ticket: "L-1", Kind: domain.KindBug, Title: "limite", ProjectID: p.ID, Provider: "limite"}
	st.CreateDemand(&d, "x")
	e.Enqueue(d.ID)
	got := waitFor(t, st, d.ID, func(x domain.Demand) bool {
		return lp.calls.Load() == 1 && !e.IsRunning(x.ID) && x.Status == domain.StatusQueued
	})
	if got.Stage != domain.StageAnalysis {
		t.Fatalf("deveria aguardar na etapa em que parou: %s", got.Stage)
	}
	_, paused := e.Usage()
	if !paused.After(time.Now().Add(time.Hour)) {
		t.Fatal("fila deveria estar pausada até a liberação")
	}
	time.Sleep(4 * time.Second) // agendador roda a cada 3s: não pode reexecutar durante a pausa
	if lp.calls.Load() != 1 {
		t.Fatalf("executou durante a pausa: %d chamadas", lp.calls.Load())
	}
	e.ResumeQueue()
	waitFor(t, st, d.ID, func(x domain.Demand) bool { return lp.calls.Load() >= 2 })
}
