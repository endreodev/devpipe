//go:build windows

package procx

import (
	"os/exec"
	"strconv"
	"syscall"
)

func Prepare(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00000200}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(c.Process.Pid))
		k.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		return k.Run()
	}
}

func ShellCommand(script string) (string, []string) { return "cmd", []string{"/C", script} }
