// Package provider define a abstração de agentes de IA. Regras específicas de
// cada fornecedor ficam isoladas em suas implementações.
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

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
}

// Event é um evento observado durante a execução do agente.
type Event struct {
	Kind    string // texto | arquivo_lido | arquivo_alterado | comando | resultado_ferramenta | permissao_negada | sessao | stderr
	Message string
	Detail  string
	IsError bool
}

type Result struct {
	SessionID string
	Text      string
	CostUSD   float64
	Turns     int
	IsError   bool
	Denials   []string
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
