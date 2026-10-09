// Package domain contém os tipos centrais da esteira (sem dependências externas).
package domain

import "time"

// Stage representa a etapa da esteira (coluna do Kanban).
type Stage string

const (
	StageBacklog    Stage = "backlog"
	StageAnalysis   Stage = "analise"
	StagePlanning   Stage = "planejamento"
	StagePrep       Stage = "preparacao"
	StageDev        Stage = "desenvolvimento"
	StageValidation Stage = "validacao"
	StageReview     Stage = "revisao"
	StageDone       Stage = "concluido"
	StageBlocked    Stage = "bloqueado"
	StageReturned   Stage = "ajustes"
)

// FlowStages é a sequência "feliz" usada para calcular progresso real.
var FlowStages = []Stage{StageBacklog, StageAnalysis, StagePlanning, StagePrep, StageDev, StageValidation, StageReview, StageDone}

// AllStages na ordem de exibição do Kanban.
var AllStages = []Stage{StageBacklog, StageAnalysis, StagePlanning, StagePrep, StageDev, StageValidation, StageReview, StageDone, StageBlocked, StageReturned}

var StageLabels = map[Stage]string{
	StageBacklog: "Backlog", StageAnalysis: "Análise", StagePlanning: "Planejamento",
	StagePrep: "Preparação", StageDev: "Desenvolvimento", StageValidation: "Validação",
	StageReview: "Revisão", StageDone: "Concluído", StageBlocked: "Bloqueado", StageReturned: "Devolvido p/ ajustes",
}

// StageIndex devolve a posição da etapa no fluxo, ou -1.
func StageIndex(s Stage) int {
	for i, f := range FlowStages {
		if f == s {
			return i
		}
	}
	return -1
}

// Status de execução dentro da etapa.
const (
	StatusQueued           = "na_fila"
	StatusRunning          = "executando"
	StatusAwaitingApproval = "aguardando_aprovacao"
	StatusIdle             = "parado"
	StatusFailed           = "falhou"
	StatusCancelled        = "cancelado"
	StatusDone             = "concluido"
)

type Project struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	Language       string    `json:"language"`
	MainSolution   string    `json:"mainSolution"`
	BranchOptions  []string  `json:"branchOptions"`  // atalhos configuráveis (ex.: release, main, main_versao, stage)
	StandardsFiles []string  `json:"standardsFiles"` // arquivos de padrões relativos ao repositório
	ExtraRules     string    `json:"extraRules"`
	BuildCommand   string    `json:"buildCommand"`
	TestCommand    string    `json:"testCommand"`
	AgentBashAllow []string  `json:"agentBashAllow"` // prefixos de comando liberados ao agente (ex.: "dotnet build")
	BranchPrefix   string    `json:"branchPrefix"`
	AutoCommit     bool      `json:"autoCommit"`
	FileEncoding   string    `json:"fileEncoding"` // auto | windows-1252 | utf-8 | utf-8-bom (arquivos novos)
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type Demand struct {
	ID              int64      `json:"id"`
	Ticket          string     `json:"ticket"`
	Title           string     `json:"title"`
	Summary         string     `json:"summary"`
	ProjectID       int64      `json:"projectId"`
	ProjectName     string     `json:"projectName,omitempty"`
	Priority        int        `json:"priority"` // 1=crítica .. 4=baixa
	QueueOrder      int        `json:"queueOrder"`
	Notes           string     `json:"notes"`
	BaseBranch      string     `json:"baseBranch"` // "" = branch atual do repositório
	WorkBranch      string     `json:"workBranch"`
	WorktreePath    string     `json:"worktreePath"`
	Provider        string     `json:"provider"`
	Model           string     `json:"model"`
	ReviewSpec      bool       `json:"reviewSpec"` // pausar para aprovar a especificação
	Stage           Stage      `json:"stage"`
	BlockedFrom     Stage      `json:"blockedFrom"` // etapa em que estava quando bloqueou
	Status          string     `json:"status"`
	StatusMessage   string     `json:"statusMessage"`
	Progress        int        `json:"progress"`
	Attempts        int        `json:"attempts"`
	ValidationState string     `json:"validationState"` // "", "ok", "falhou", "nao_executada"
	CurrentRevision int        `json:"currentRevision"`
	LastActivity    string     `json:"lastActivity"`
	LastActivityAt  *time.Time `json:"lastActivityAt"`
	ElapsedSeconds  int64      `json:"elapsedSeconds"`
	StartedAt       *time.Time `json:"startedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// Revision guarda a solicitação original (nº 1) e cada devolução para ajustes.
type Revision struct {
	ID           int64     `json:"id"`
	DemandID     int64     `json:"demandId"`
	Number       int       `json:"number"`
	Kind         string    `json:"kind"` // original | ajuste
	Instructions string    `json:"instructions"`
	Spec         string    `json:"spec"` // especificação normalizada (markdown)
	SpecApproved bool      `json:"specApproved"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Attachment struct {
	ID            int64     `json:"id"`
	DemandID      int64     `json:"demandId"`
	RevisionID    int64     `json:"revisionId"`
	FileName      string    `json:"fileName"`
	StoredPath    string    `json:"-"`
	Mime          string    `json:"mime"`
	Size          int64     `json:"size"`
	Kind          string    `json:"kind"` // texto | pdf | docx | imagem | outro
	ExtractedPath string    `json:"-"`
	ExtractNote   string    `json:"extractNote"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Run struct {
	ID          int64      `json:"id"`
	DemandID    int64      `json:"demandId"`
	RevisionNum int        `json:"revisionNum"`
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	Stage       Stage      `json:"stage"`
	RulesHash   string     `json:"rulesHash"`
	RulesText   string     `json:"rulesText,omitempty"`
	SessionID   string     `json:"sessionId"`
	Summary     string     `json:"summary"`
	Error       string     `json:"error"`
	CostUSD     float64    `json:"costUsd"`
	DiffStat    string     `json:"diffStat"`
	CommitSHA   string     `json:"commitSha"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// Níveis de evento.
const (
	LevelInfo  = "info"
	LevelWarn  = "aviso"
	LevelError = "erro"
	LevelAuth  = "autorizacao"
	LevelOK    = "sucesso"
	LevelAgent = "agente"
)

type Event struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	DemandID  int64     `json:"demandId"`
	Ticket    string    `json:"ticket"`
	ProjectID int64     `json:"projectId"`
	Project   string    `json:"project"`
	RunID     int64     `json:"runId"`
	Stage     Stage     `json:"stage"`
	Level     string    `json:"level"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`
}

type Rule struct {
	ID        int64     `json:"id"`
	Scope     string    `json:"scope"` // global | projeto
	ProjectID int64     `json:"projectId"`
	Title     string    `json:"title"`
	Category  string    `json:"category"`
	Content   string    `json:"content"`
	PathGlob  string    `json:"pathGlob"` // aplica-se a diretórios/arquivos (opcional)
	Language  string    `json:"language"`
	Mandatory bool      `json:"mandatory"`
	Priority  int       `json:"priority"`
	Enabled   bool      `json:"enabled"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Settings gerais (persistidas como chave/valor JSON).
type Settings struct {
	AuthorizedRoots   []string        `json:"authorizedRoots"`
	MaxParallel       int             `json:"maxParallel"`
	DefaultProvider   string          `json:"defaultProvider"`
	MaxFixAttempts    int             `json:"maxFixAttempts"`
	CommandTimeoutMin int             `json:"commandTimeoutMin"`
	AgentTimeoutMin   int             `json:"agentTimeoutMin"`
	AutoStartQueue    bool            `json:"autoStartQueue"`
	Claude            ClaudeSettings  `json:"claude"`
	Generic           GenericSettings `json:"generic"`
}

type ClaudeSettings struct {
	Enabled        bool   `json:"enabled"`
	Binary         string `json:"binary"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
	ExtraArgs      string `json:"extraArgs"` // argumentos extras para a CLI (avançado)
	// SubscriptionOnly: usar apenas o limite da assinatura Pro/Max (padrão: sim).
	SubscriptionOnly bool    `json:"subscriptionOnly"`
	AnalysisModel    string  `json:"analysisModel"` // modelo da análise (vazio = padrão)
	Effort           string  `json:"effort"`        // low|medium|high|xhigh|max (vazio = padrão da CLI)
	MaxBudgetUSD     float64 `json:"maxBudgetUsd"`
}

type GenericSettings struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
	Command string `json:"command"` // executado no diretório da worktree; prompt via stdin
}

func DefaultSettings() Settings {
	return Settings{
		MaxParallel: 1, DefaultProvider: "claude", MaxFixAttempts: 2,
		CommandTimeoutMin: 15, AgentTimeoutMin: 45, AutoStartQueue: true,
		Claude:  ClaudeSettings{Enabled: true, Binary: "claude", PermissionMode: "acceptEdits", SubscriptionOnly: true},
		Generic: GenericSettings{Name: "CLI genérico"},
	}
}
