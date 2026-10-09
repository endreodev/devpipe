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
	Errors []string `json:"errors"`
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
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = req.WorkDir
	cmd.Env = os.Environ()
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
				emit(Event{Kind: "sessao", Message: "Sessão do agente iniciada", Detail: "modelo: " + m.Model})
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
