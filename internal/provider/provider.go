// Package provider define a abstração de agentes de IA. Regras específicas de
// cada fornecedor ficam isoladas em suas implementações.
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// PlanUsage: consumo da assinatura (Pro/Max) informado pela CLI do Claude Code.
type PlanUsage struct {
	Status       string    `json:"status"` // allowed | allowed_warning | rejected
	LimitType    string    `json:"limitType"`
	FiveHourPct  float64   `json:"fiveHourPct"`
	FiveHourEnds time.Time `json:"fiveHourResetsAt"`
	SevenDayPct  float64   `json:"sevenDayPct"`
	SevenDayEnds time.Time `json:"sevenDayResetsAt"`
	ResetsAt     time.Time `json:"resetsAt"`
	UsingOverage bool      `json:"usingOverage"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ErrPlanLimit: o limite da assinatura foi atingido; a fila deve aguardar.
type ErrPlanLimit struct{ ResetsAt time.Time }

func (e *ErrPlanLimit) Error() string {
	if e.ResetsAt.IsZero() {
		return "limite de uso da assinatura atingido"
	}
	return "limite de uso da assinatura atingido; libera em " + e.ResetsAt.Local().Format("02/01 15:04")
}

// ErrPolicy: execução interrompida por violar a política de cobrança/segurança.
type ErrPolicy struct{ Msg string }

func (e *ErrPolicy) Error() string { return e.Msg }

// Phase indica o tipo de trabalho solicitado; cada provedor traduz isso em
// permissões concretas.
type Phase string

const (
	PhaseAnalysis Phase = "analise"         // somente leitura: gerar especificação + plano
	PhaseDevelop  Phase = "desenvolvimento" // ler/editar arquivos na worktree
	PhaseFix      Phase = "correcao"        // corrigir falhas de validação
	PhaseProbe    Phase = "teste_conexao"   // chamada mínima para testar autenticação
)

type Request struct {
	Phase         Phase
	WorkDir       string   // worktree isolada (cwd do agente)
	SystemPrompt  string   // política de segurança (anexada ao prompt de sistema)
	Prompt        string   // contexto + tarefa
	ExtraReadDirs []string // diretórios adicionais somente para leitura (anexos)
	BashAllow     []string // padrões de Bash liberados (validação do projeto)
	Model         string
	ResumeSession string // continuar sessão anterior (correções)
	// EncodingManifest: se preenchido, o provedor deve converter para UTF-8 cada
	// arquivo imediatamente antes de o agente lê-lo/editá-lo (hook), registrando
	// neste arquivo. Vazio = conversão feita antes da execução (modo completo).
	EncodingManifest string
}

// HookCapable: provedores que convertem a codificação sob demanda (hooks).
type HookCapable interface{ SupportsEncodingHook() bool }

// Event é um evento observado durante a execução do agente.
type Event struct {
	Kind    string // texto | arquivo_lido | arquivo_alterado | comando | resultado_ferramenta | permissao_negada | sessao | stderr
	Message string
	Detail  string
	IsError bool
	Usage   *PlanUsage // preenchido quando Kind == "uso_plano"
}

type Result struct {
	SessionID string
	Text      string
	CostUSD   float64
	Turns     int
	IsError   bool
	Denials   []string
	AuthMode  string // assinatura | chave_api | desconhecido
	Usage     *PlanUsage
}

type Availability struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Message string `json:"message"`
}

// Provider: contrato que todo agente de IA implementa.
// Operações conceituais mapeadas: verificar disponibilidade (Check), enviar
// contexto e iniciar (Run), receber eventos (emit), consultar resultado
// (Result), cancelar (ctx), solicitar correções (Run com PhaseFix/ResumeSession).
type Provider interface {
	ID() string
	Name() string
	Check(ctx context.Context) Availability
	Run(ctx context.Context, req Request, emit func(Event)) (Result, error)
	SupportsImages() bool
}

// Registry mantém os provedores disponíveis.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Provider
}

func NewRegistry() *Registry { return &Registry{m: map[string]Provider{}} }

func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[p.ID()] = p
}

func (r *Registry) Get(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[id]
	if !ok {
		return nil, fmt.Errorf("provedor de IA %q não configurado ou desabilitado", id)
	}
	return p, nil
}

func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.m))
	for _, p := range r.m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, id)
}
