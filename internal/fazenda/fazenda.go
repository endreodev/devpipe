// Package fazenda calcula o estado do painel "Fazenda da Esteira": cada
// demanda do DevPipe é uma plantação, a IA e o revisor humano são os
// fazendeiros e as etapas da esteira são o ciclo de vida da planta.
// Somente leitura: nada aqui altera demandas.
package fazenda

import (
	"sort"
	"time"

	"devpipe/internal/domain"
)

// Regras configuráveis (valores padrão em Default).
type Regras struct {
	SLAHoras        map[int]float64          `json:"slaHoras"`        // por prioridade (1..4), a partir do cadastro
	LimiteParadoH   map[domain.Stage]float64 `json:"limiteParadoH"`   // a partir daqui: corvos
	WIP             map[domain.Stage]int     `json:"wip"`             // limite por etapa (0 = sem limite)
	TamanhoP        int                      `json:"tamanhoP"`        // especificação até N caracteres = morango
	TamanhoM        int                      `json:"tamanhoM"`        // até N = milho; acima = abóbora
	ClimaTempestade int                      `json:"climaTempestade"` // nº de alertas (SLA + bloqueios) para tempestade
}

func Default(maxParallel int) Regras {
	if maxParallel < 1 {
		maxParallel = 1
	}
	return Regras{
		SLAHoras: map[int]float64{1: 8, 2: 24, 3: 72, 4: 168},
		LimiteParadoH: map[domain.Stage]float64{
			domain.StageBacklog: 72, domain.StageAnalysis: 2, domain.StagePlanning: 24, domain.StagePrep: 2,
			domain.StageDev: 3, domain.StageValidation: 2, domain.StageReview: 24, domain.StageBlocked: 8, domain.StageReturned: 24,
		},
		WIP: map[domain.Stage]int{
			domain.StageBacklog: 20, domain.StageDev: maxParallel, domain.StageReview: 5, domain.StageBlocked: 3, domain.StageReturned: 5,
		},
		TamanhoP: 2500, TamanhoM: 6000, ClimaTempestade: 4,
	}
}

type Demanda struct {
	ID            int64        `json:"id"`
	Ticket        string       `json:"ticket"`
	Titulo        string       `json:"titulo"`
	Etapa         domain.Stage `json:"etapa"`
	BloqueadaEm   domain.Stage `json:"bloqueadaEm,omitempty"`
	Status        string       `json:"status"`
	Mensagem      string       `json:"mensagem"`
	Prioridade    int          `json:"prioridade"`
	Tamanho       string       `json:"tamanho"` // P | M | G
	Responsavel   string       `json:"responsavel"`
	Executando    bool         `json:"executando"`
	AguardaHumano bool         `json:"aguardaHumano"`
	EntrouNaEtapa time.Time    `json:"entrouNaEtapa"`
	ParadoH       float64      `json:"paradoH"`
	Murcha        float64      `json:"murcha"` // 0..1 (1 = limite atingido: corvos)
	PrazoSLA      *time.Time   `json:"prazoSla,omitempty"`
	SLAEstourado  bool         `json:"slaEstourado"`
	Retrabalhos   int          `json:"retrabalhos"`
	Tentativas    int          `json:"tentativas"`
	Progresso     int          `json:"progresso"`
	Validacao     string       `json:"validacao"`
}

type Metricas struct {
	EntregasHoje   int     `json:"entregasHoje"`
	EntregasSemana int     `json:"entregasSemana"`
	LeadTimeMedioH float64 `json:"leadTimeMedioH"`
	TaxaRetrabalho float64 `json:"taxaRetrabalho"`
	Clima          string  `json:"clima"` // sol | nublado | tempestade
	SLAEstourados  int     `json:"slaEstourados"`
	Bloqueadas     int     `json:"bloqueadas"`
	Atrasadas      int     `json:"atrasadas"`
}

type Estado struct {
	TS       time.Time               `json:"ts"`
	Demandas []Demanda               `json:"demandas"`
	Metricas Metricas                `json:"metricas"`
	Contagem map[domain.Stage]int    `json:"contagem"`
	WIP      map[domain.Stage]int    `json:"wip"`
	Etapas   []domain.Stage          `json:"etapas"`
	Rotulos  map[domain.Stage]string `json:"rotulos"`
	Regras   Regras                  `json:"regras"`
}

// Entrada reúne os dados brutos vindos do banco.
type Entrada struct {
	Demandas      []domain.Demand
	EntrouNaEtapa map[int64]time.Time
	SpecSizes     map[int64]int
	Entregas      []Entrega // últimos 7 dias
	Running       func(id int64) bool
}

type Entrega struct {
	DemandID int64
	At       time.Time
}

// Calcular monta o estado do painel. now e loc tornam o cálculo testável.
func Calcular(in Entrada, r Regras, now time.Time, loc *time.Location) Estado {
	if loc == nil {
		loc = time.Local
	}
	est := Estado{TS: now, Contagem: map[domain.Stage]int{}, WIP: r.WIP, Etapas: domain.AllStages, Rotulos: domain.StageLabels, Regras: r}
	byID := map[int64]domain.Demand{}
	for _, d := range in.Demandas {
		byID[d.ID] = d
		fd := Demanda{
			ID: d.ID, Ticket: d.Ticket, Titulo: d.Title, Etapa: d.Stage, Status: d.Status, Mensagem: d.StatusMessage,
			Prioridade: d.Priority, Responsavel: provedorNome(d.Provider), Retrabalhos: max(d.CurrentRevision-1, 0),
			Tentativas: d.Attempts, Progresso: d.Progress, Validacao: d.ValidationState,
		}
		if d.Stage == domain.StageBlocked {
			fd.BloqueadaEm = d.BlockedFrom
		}
		fd.Executando = d.Status == domain.StatusRunning || (in.Running != nil && in.Running(d.ID))
		fd.AguardaHumano = d.Status == domain.StatusAwaitingApproval

		// tamanho pela complexidade da especificação
		switch n := in.SpecSizes[d.ID]; {
		case n == 0 || n <= r.TamanhoP:
			fd.Tamanho = "P"
		case n <= r.TamanhoM:
			fd.Tamanho = "M"
		default:
			fd.Tamanho = "G"
		}

		// tempo na etapa
		entrou := d.CreatedAt
		if t, ok := in.EntrouNaEtapa[d.ID]; ok && t.After(entrou) {
			entrou = t
		}
		fd.EntrouNaEtapa = entrou
		fd.ParadoH = now.Sub(entrou).Hours()
		if lim := r.LimiteParadoH[d.Stage]; lim > 0 && d.Stage != domain.StageDone {
			fd.Murcha = min(fd.ParadoH/lim, 1)
		}

		// SLA a partir do cadastro, por prioridade
		if h := r.SLAHoras[d.Priority]; h > 0 && d.Stage != domain.StageDone {
			prazo := d.CreatedAt.Add(time.Duration(h * float64(time.Hour)))
			fd.PrazoSLA = &prazo
			fd.SLAEstourado = now.After(prazo)
		}

		if d.Stage != domain.StageDone {
			est.Contagem[d.Stage]++
		}
		if fd.SLAEstourado {
			est.Metricas.SLAEstourados++
		}
		if d.Stage == domain.StageBlocked {
			est.Metricas.Bloqueadas++
		}
		if fd.SLAEstourado || fd.Murcha >= 1 || d.Stage == domain.StageBlocked {
			est.Metricas.Atrasadas++
		}
		est.Demandas = append(est.Demandas, fd)
	}

	// entregas exibidas: concluídas hoje continuam na caixa de envio; as mais
	// antigas saem do mapa (só métricas).
	hoje := startOfDay(now.In(loc))
	semana := startOfWeek(now.In(loc))
	filtered := est.Demandas[:0]
	for _, fd := range est.Demandas {
		if fd.Etapa == domain.StageDone && fd.EntrouNaEtapa.Before(hoje) {
			continue
		}
		filtered = append(filtered, fd)
	}
	est.Demandas = filtered
	sort.SliceStable(est.Demandas, func(i, j int) bool {
		a, b := est.Demandas[i], est.Demandas[j]
		if a.Prioridade != b.Prioridade {
			return a.Prioridade < b.Prioridade
		}
		return a.ID < b.ID
	})

	// métricas de entrega (uma entrega por demanda por dia)
	seen := map[[2]int64]bool{}
	var leadSum float64
	var leadN, comRetrabalho int
	for _, e := range in.Entregas {
		day := startOfDay(e.At.In(loc)).Unix()
		key := [2]int64{e.DemandID, day}
		if seen[key] {
			continue
		}
		seen[key] = true
		if !e.At.Before(hoje) {
			est.Metricas.EntregasHoje++
		}
		if !e.At.Before(semana) {
			est.Metricas.EntregasSemana++
		}
		if now.Sub(e.At) <= 7*24*time.Hour {
			if d, ok := byID[e.DemandID]; ok {
				leadSum += e.At.Sub(d.CreatedAt).Hours()
				leadN++
				if d.CurrentRevision > 1 {
					comRetrabalho++
				}
			}
		}
	}
	if leadN > 0 {
		est.Metricas.LeadTimeMedioH = round1(leadSum / float64(leadN))
		est.Metricas.TaxaRetrabalho = float64(comRetrabalho) / float64(leadN)
	}

	alertas := est.Metricas.SLAEstourados + est.Metricas.Bloqueadas
	for st, lim := range r.WIP {
		if lim > 0 && est.Contagem[st] > lim {
			alertas++
		}
	}
	switch {
	case alertas == 0:
		est.Metricas.Clima = "sol"
	case alertas >= r.ClimaTempestade:
		est.Metricas.Clima = "tempestade"
	default:
		est.Metricas.Clima = "nublado"
	}
	if est.Demandas == nil {
		est.Demandas = []Demanda{}
	}
	return est
}

func provedorNome(id string) string {
	switch id {
	case "claude", "":
		return "Claude"
	case "generico":
		return "Agente"
	}
	return id
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// semana de segunda a domingo
func startOfWeek(t time.Time) time.Time {
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	return startOfDay(t).AddDate(0, 0, -(wd - 1))
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
