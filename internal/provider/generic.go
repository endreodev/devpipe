package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"devpipe/internal/procx"
)

// Generic executa qualquer CLI de agente configurada pelo usuário (ex.: outro
// assistente de código). O comando roda na worktree, recebe o prompt via stdin
// e variáveis DEVPIPE_PHASE / DEVPIPE_WORKDIR. Cada linha da saída vira um
// evento; o final da saída é usado como resumo.
//
// Limitação documentada: a aplicação não consegue impor permissões por
// ferramenta a uma CLI desconhecida — o isolamento vem da worktree e da
// revisão humana obrigatória.
type Generic struct {
	DisplayName string
	Command     string
}

func (g *Generic) ID() string           { return "generico" }
func (g *Generic) SupportsImages() bool { return false }
func (g *Generic) Name() string {
	if g.DisplayName == "" {
		return "CLI genérico"
	}
	return g.DisplayName
}

func (g *Generic) Check(ctx context.Context) Availability {
	if strings.TrimSpace(g.Command) == "" {
		return Availability{Message: "nenhum comando configurado"}
	}
	return Availability{OK: true, Message: "comando configurado: " + g.Command}
}

func (g *Generic) Run(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	if strings.TrimSpace(g.Command) == "" {
		return Result{}, fmt.Errorf("provedor genérico sem comando configurado")
	}
	sh, args := procx.ShellCommand(g.Command)
	cmd := exec.CommandContext(ctx, sh, args...)
	cmd.Dir = req.WorkDir
	cmd.Env = append(os.Environ(), "DEVPIPE_PHASE="+string(req.Phase), "DEVPIPE_WORKDIR="+req.WorkDir)
	cmd.WaitDelay = 5 * time.Second
	procx.Prepare(cmd)
	cmd.Stdin = strings.NewReader(req.SystemPrompt + "\n\n" + req.Prompt)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); pw.Close() }()

	var tail []string
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		emit(Event{Kind: "texto", Message: truncate(line, 220)})
		tail = append(tail, line)
		if len(tail) > 200 {
			tail = tail[1:]
		}
	}
	err := <-done
	res := Result{Text: strings.Join(tail, "\n")}
	if ctx.Err() != nil {
		return res, fmt.Errorf("execução cancelada ou tempo limite excedido")
	}
	if err != nil {
		res.IsError = true
		return res, fmt.Errorf("comando do agente falhou: %v", err)
	}
	return res, nil
}
