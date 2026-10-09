//go:build !windows

// Package procx cuida de iniciar e encerrar árvores de processos com segurança.
package procx

import (
	"os/exec"
	"syscall"
)

// Prepare coloca o processo em um grupo próprio, para que o cancelamento
// encerre também os subprocessos (build, testes, agente).
func Prepare(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}

// ShellCommand devolve o interpretador para comandos configurados pelo usuário.
func ShellCommand(script string) (string, []string) { return "sh", []string{"-c", script} }
