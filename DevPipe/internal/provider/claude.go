package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"devpipe/internal/procx"
	"devpipe/internal/security"
)

// Claude executa o Claude Code CLI em modo não interativo (-p) com saída
// stream-json. O Claude Code é quem lê, edita e valida arquivos na worktree;
// a aplicação controla permissões, diretório, tempo limite e cancelamento.
type Claude struct {
	Binary         string
	DefaultModel   string
	PermissionMode string // acceptEdits (padrão) | dontAsk | default
	MaxBudgetUSD   float64
	ExtraArgs      []string
	// SubscriptionOnly: usar somente o login da assinatura (Pro/Max). Remove do
	// ambiente as variáveis de chave de API, aborta se a CLI reportar outra
	// fonte de autenticação e aborta se o "uso extra" pago for acionado.
	SubscriptionOnly bool
}

// apiEnvVars direcionariam a CLI para cobrança fora da assinatura.
var apiEnvVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// FilterEnv remove variáveis de cobrança por API (exportado para testes).
func FilterEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		skip := false
		for _, v := range apiEnvVars {
			if strings.EqualFold(name, v) {
				skip = true
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

type rateLimitInfo struct {
	Status         string `json:"status"`
	ResetsAt       int64  `json:"resetsAt"`
	RateLimitType  string `json:"rateLimitType"`
	IsUsingOverage bool   `json:"isUsingOverage"`
	Windows        map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

func (r rateLimitInfo) usage() *PlanUsage {
	u := &PlanUsage{Status: r.Status, LimitType: r.RateLimitType, UsingOverage: r.IsUsingOverage, UpdatedAt: time.Now().UTC()}
	if r.ResetsAt > 0 {
		u.ResetsAt = time.Unix(r.ResetsAt, 0).UTC()
	}
	if w, ok := r.Windows["five_hour"]; ok {
		u.FiveHourPct, u.FiveHourEnds = w.Utilization*100, time.Unix(w.ResetsAt, 0).UTC()
	}
	if w, ok := r.Windows["seven_day"]; ok {
		u.SevenDayPct, u.SevenDayEnds = w.Utilization*100, time.Unix(w.ResetsAt, 0).UTC()
	}
	return u
}

func (c *Claude) ID() string           { return "claude" }
func (c *Claude) Name() string         { return "Claude (Claude Code CLI)" }
func (c *Claude) SupportsImages() bool { return true }

func (c *Claude) bin() string {
	if c.Binary == "" {
		return "claude"
	}
	return c.Binary
}

func (c *Claude) Check(ctx context.Context) Availability {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Availability{Message: "CLI do Claude Code não encontrada. Instale com: npm install -g @anthropic-ai/claude-code e execute 'claude' uma vez para autenticar."}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	procx.Prepare(cmd)
	out, err := cmd.Output()
	if err != nil {
		return Availability{Message: "falha ao executar 'claude --version': " + err.Error()}
	}
	return Availability{OK: true, Version: strings.TrimSpace(string(out)), Message: "CLI encontrada em " + path + ". A autenticação é verificada no botão 'Testar conexão'."}
}

// BuildArgs monta os argumentos da CLI (exportado para testes).
func (c *Claude) BuildArgs(req Request) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	var allowed []string
	perm := c.PermissionMode
	switch req.Phase {
	case PhaseAnalysis, PhaseProbe:
		allowed = append(allowed, security.AgentReadTools...)
		perm = "default" // nada além de leitura; tudo que pedir permissão é negado em -p
	default:
		allowed = append(allowed, security.AgentEditTools...)
		allowed = append(allowed, req.BashAllow...)
		if perm == "" || perm == "bypassPermissions" {
			perm = "acceptEdits" // bypass nunca é usado pela esteira
		}
	}
	args = append(args, "--permission-mode", perm)
	args = append(args, "--allowedTools", strings.Join(allowed, ","))
	args = append(args, "--disallowedTools", strings.Join(security.AgentDisallowedTools, ","))
	if req.SystemPrompt != "" {
		// linha única: wrappers .cmd no Windows truncam argumentos com quebras de linha.
		// O texto completo também vai no início do prompt (stdin).
		args = append(args, "--append-system-prompt", strings.Join(strings.Fields(req.SystemPrompt), " "))
	}
	model := req.Model
	if model == "" {
		model = c.DefaultModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	for _, d := range req.ExtraReadDirs {
		args = append(args, "--add-dir", d)
	}
	if req.ResumeSession != "" {
		args = append(args, "--resume", req.ResumeSession)
	}
	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", c.MaxBudgetUSD))
	}
	args = append(args, c.ExtraArgs...)
	return args
}

type streamMsg struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	Cost      float64         `json:"total_cost_usd"`
	NumTurns  int             `json:"num_turns"`
	Denials   []struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
	Errors        []string        `json:"errors"`
	APIKeySource  string          `json:"apiKeySource"`
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

func (c *Claude) Run(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Result{}, errors.New("CLI do Claude Code não encontrada no PATH")
	}
	exe, args := resolveLauncher(path, c.BuildArgs(req))
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.Dir = req.WorkDir
	cmd.Env = os.Environ()
	if c.SubscriptionOnly {
		cmd.Env = FilterEnv(cmd.Env)
	}
	var policyErr error
	abort := func(err error) {
		if policyErr == nil {
			policyErr = err
			emit(Event{Kind: "politica_cobranca", Message: err.Error(), IsError: true})
			stop()
		}
	}
	cmd.WaitDelay = 5 * time.Second
	procx.Prepare(cmd)
	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + prompt
	}
	cmd.Stdin = strings.NewReader(prompt) // prompt via stdin evita limite de linha de comando
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("falha ao iniciar o Claude Code: %w", err)
	}

	var stderrBuf strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b, _ := io.ReadAll(io.LimitReader(stderr, 64<<10))
		stderrBuf.Write(b)
	}()

	var res Result
	gotResult := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		var m streamMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "system":
			if m.Subtype == "init" {
				res.SessionID = m.SessionID
				res.AuthMode = authMode(m.APIKeySource)
				emit(Event{Kind: "sessao", Message: "Sessão do agente iniciada — autenticação: " + authLabel(res.AuthMode), Detail: "modelo: " + m.Model + "\napiKeySource: " + m.APIKeySource})
				if c.SubscriptionOnly && res.AuthMode != "assinatura" {
					abort(&ErrPolicy{Msg: "Execução interrompida: o Claude Code está autenticado por chave de API (" + m.APIKeySource + "), mas a esteira está configurada para usar SOMENTE a assinatura Pro/Max. Rode 'claude' no terminal, use /logout e /login com a conta Pro, ou desative a opção em Provedores de IA."})
				}
			}
		case "rate_limit_event":
			var rl rateLimitInfo
			if json.Unmarshal(m.RateLimitInfo, &rl) == nil {
				u := rl.usage()
				res.Usage = u
				emit(Event{Kind: "uso_plano", Message: fmt.Sprintf("Uso da assinatura — janela 5h: %.0f%% · semana: %.0f%%", u.FiveHourPct, u.SevenDayPct), Usage: u})
				if c.SubscriptionOnly && u.UsingOverage {
					abort(&ErrPolicy{Msg: "Execução interrompida: o limite do plano acabou e a conta começou a usar 'uso extra' pago. A esteira está configurada para usar somente o limite da assinatura."})
				}
				if u.Status == "rejected" {
					abort(&ErrPlanLimit{ResetsAt: u.ResetsAt})
				}
			}
		case "assistant":
			var msg struct{ Content []contentBlock }
			json.Unmarshal(m.Message, &msg)
			for _, b := range msg.Content {
				switch b.Type {
				case "text":
					if t := strings.TrimSpace(b.Text); t != "" {
						emit(Event{Kind: "texto", Message: truncate(firstLine(t), 220), Detail: truncate(t, 4000)})
					}
				case "tool_use":
					emit(describeTool(b.Name, b.Input))
				}
			}
		case "user":
			var msg struct{ Content []contentBlock }
			json.Unmarshal(m.Message, &msg)
			for _, b := range msg.Content {
				if b.Type == "tool_result" && b.IsError {
					emit(Event{Kind: "resultado_ferramenta", Message: "Ferramenta retornou erro", Detail: truncate(rawText(b.Content), 2000), IsError: true})
				}
			}
		case "result":
			gotResult = true
			if m.SessionID != "" {
				res.SessionID = m.SessionID
			}
			res.Text, res.CostUSD, res.Turns = m.Result, m.Cost, m.NumTurns
			res.IsError = m.IsError || (m.Subtype != "" && m.Subtype != "success")
			if res.IsError && res.Text == "" {
				res.Text = m.Subtype + " " + strings.Join(m.Errors, "; ")
			}
			for _, d := range m.Denials {
				desc := d.ToolName + " " + truncate(string(d.ToolInput), 300)
				res.Denials = append(res.Denials, desc)
				emit(Event{Kind: "permissao_negada", Message: "Ação bloqueada pela política: " + d.ToolName, Detail: desc, IsError: true})
			}
		}
	}
	scanErr := sc.Err()
	wg.Wait()
	waitErr := cmd.Wait()

	if policyErr != nil {
		return res, policyErr
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("execução cancelada ou tempo limite excedido: %w", ctx.Err())
	}
	if !gotResult {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		if scanErr != nil {
			msg += " (leitura: " + scanErr.Error() + ")"
		}
		return res, fmt.Errorf("o Claude Code terminou sem resultado: %s", truncate(msg, 1500))
	}
	if res.IsError {
		return res, fmt.Errorf("o agente reportou erro: %s", truncate(res.Text, 1500))
	}
	return res, nil
}

func authMode(src string) string {
	switch src {
	case "none":
		return "assinatura" // login OAuth da conta claude.ai (Pro/Max/Team/Enterprise)
	case "":
		return "desconhecido"
	}
	return "chave_api" // ANTHROPIC_API_KEY, apiKeyHelper ou login do Console
}

func authLabel(m string) string {
	switch m {
	case "assinatura":
		return "assinatura (Pro/Max) — sem cobrança por token"
	case "chave_api":
		return "chave de API — cobrança por token"
	}
	return "não identificada"
}

// resolveLauncher: no Windows, a instalação via npm cria "claude.cmd". Executar
// um .cmd passa os argumentos pelo cmd.exe, que interpreta parênteses, "*" e
// quebras de linha. Quando possível, executamos o cli.js diretamente com o node.
// (O instalador nativo, que gera claude.exe, não precisa disso.)
func resolveLauncher(path string, args []string) (string, []string) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return path, args
	}
	cli := filepath.Join(filepath.Dir(path), "node_modules", "@anthropic-ai", "claude-code", "cli.js")
	if _, err := os.Stat(cli); err != nil {
		return path, args
	}
	node := filepath.Join(filepath.Dir(path), "node.exe")
	if _, err := os.Stat(node); err != nil {
		if n, err := exec.LookPath("node"); err == nil {
			node = n
		} else {
			return path, args
		}
	}
	return node, append([]string{cli}, args...)
}

func describeTool(name string, raw json.RawMessage) Event {
	var in map[string]any
	json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch name {
	case "Read":
		return Event{Kind: "arquivo_lido", Message: "Arquivo analisado: " + str("file_path")}
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		p := str("file_path")
		if p == "" {
			p = str("notebook_path")
		}
		return Event{Kind: "arquivo_alterado", Message: "Arquivo alterado (" + name + "): " + p}
	case "Bash":
		return Event{Kind: "comando", Message: "Comando do agente: " + truncate(str("command"), 200), Detail: str("description")}
	case "Glob", "Grep", "LS":
		q := str("pattern")
		if q == "" {
			q = str("path")
		}
		return Event{Kind: "busca", Message: name + ": " + truncate(q, 160)}
	case "TodoWrite":
		return Event{Kind: "plano", Message: "Agente atualizou sua lista de tarefas", Detail: truncate(string(raw), 3000)}
	}
	return Event{Kind: "ferramenta", Message: "Ferramenta: " + name, Detail: truncate(string(raw), 500)}
}

func rawText(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	var parts []struct{ Text string }
	if json.Unmarshal(r, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(r)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
