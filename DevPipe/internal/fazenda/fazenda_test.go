package fazenda

import (
	"testing"
	"time"

	"devpipe/internal/domain"
)

func TestCalcular(t *testing.T) {
	loc := time.FixedZone("AMT", -4*3600)
	now := time.Date(2026, 10, 9, 17, 0, 0, 0, loc) // sexta-feira
	h := func(x float64) time.Time { return now.Add(-time.Duration(x * float64(time.Hour))) }
	ds := []domain.Demand{
		{ID: 1, Ticket: "A", Priority: 1, Stage: domain.StageDev, Status: domain.StatusRunning, CreatedAt: h(10)},             // SLA 8h estourado
		{ID: 2, Ticket: "B", Priority: 3, Stage: domain.StageReview, Status: domain.StatusAwaitingApproval, CreatedAt: h(30)}, // parado 30h > 24h
		{ID: 3, Ticket: "C", Priority: 3, Stage: domain.StageBlocked, BlockedFrom: domain.StageValidation, CreatedAt: h(1)},
		{ID: 4, Ticket: "D", Priority: 2, Stage: domain.StageDone, CreatedAt: h(20), CurrentRevision: 2},
		{ID: 5, Ticket: "E", Priority: 3, Stage: domain.StageDone, CreatedAt: h(200), CurrentRevision: 1}, // concluída ontem: some do mapa
	}
	in := Entrada{
		Demandas:      ds,
		EntrouNaEtapa: map[int64]time.Time{1: h(1), 2: h(30), 3: h(1), 4: h(2), 5: h(30)},
		SpecSizes:     map[int64]int{1: 100, 2: 3000, 3: 9000},
		Entregas: []Entrega{
			{DemandID: 4, At: h(2)}, {DemandID: 4, At: h(1.5)}, // duplicada no mesmo dia: conta uma
			{DemandID: 5, At: h(30)},
		},
	}
	e := Calcular(in, Default(1), now, loc)

	if len(e.Demandas) != 4 {
		t.Fatalf("esperava 4 no mapa (concluída de ontem sai), obtido %d", len(e.Demandas))
	}
	get := func(id int64) Demanda {
		for _, d := range e.Demandas {
			if d.ID == id {
				return d
			}
		}
		t.Fatalf("demanda %d ausente", id)
		return Demanda{}
	}
	if d := get(1); !d.SLAEstourado || !d.Executando || d.Tamanho != "P" || d.Responsavel != "Claude" {
		t.Fatalf("d1: %+v", d)
	}
	if d := get(2); d.Murcha != 1 || !d.AguardaHumano || d.Tamanho != "M" {
		t.Fatalf("d2: %+v", d)
	}
	if d := get(3); d.BloqueadaEm != domain.StageValidation || d.Tamanho != "G" {
		t.Fatalf("d3: %+v", d)
	}
	if d := get(4); d.Retrabalhos != 1 || d.PrazoSLA != nil {
		t.Fatalf("d4: %+v", d)
	}
	m := e.Metricas
	if m.EntregasHoje != 1 || m.EntregasSemana != 2 {
		t.Fatalf("entregas: %+v", m)
	}
	if m.LeadTimeMedioH != (18+170)/2.0 || m.TaxaRetrabalho != 0.5 {
		t.Fatalf("lead/retrabalho: %+v", m)
	}
	if m.SLAEstourados != 1 || m.Bloqueadas != 1 || m.Atrasadas != 3 || m.Clima != "nublado" {
		t.Fatalf("alertas/clima: %+v", m)
	}
	if e.Contagem[domain.StageDone] != 0 || e.Contagem[domain.StageDev] != 1 {
		t.Fatalf("contagem: %+v", e.Contagem)
	}
}

func TestClima(t *testing.T) {
	now := time.Now()
	sol := Calcular(Entrada{Demandas: []domain.Demand{{ID: 1, Priority: 4, Stage: domain.StageBacklog, CreatedAt: now}}}, Default(1), now, time.UTC)
	if sol.Metricas.Clima != "sol" {
		t.Fatal(sol.Metricas)
	}
	var ds []domain.Demand
	for i := int64(1); i <= 4; i++ {
		ds = append(ds, domain.Demand{ID: i, Priority: 4, Stage: domain.StageBlocked, CreatedAt: now})
	}
	if c := Calcular(Entrada{Demandas: ds}, Default(1), now, time.UTC).Metricas.Clima; c != "tempestade" {
		t.Fatal(c)
	}
}
