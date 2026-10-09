package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestClaudeArgsByPhase(t *testing.T) {
	c := &Claude{PermissionMode: "bypassPermissions", DefaultModel: "sonnet"}
	an := c.BuildArgs(Request{Phase: PhaseAnalysis, ExtraReadDirs: []string{"/anexos"}})
	if argValue(an, "--permission-mode") != "default" {
		t.Fatal("análise deve rodar sem auto-aceite")
	}
	allowed := argValue(an, "--allowedTools")
	if strings.Contains(allowed, "Edit") || strings.Contains(allowed, "Write") || strings.Contains(allowed, "Bash") {
		t.Fatalf("análise não pode editar: %s", allowed)
	}
	if argValue(an, "--add-dir") != "/anexos" || argValue(an, "--model") != "sonnet" {
		t.Fatal("add-dir/model")
	}
	dev := c.BuildArgs(Request{Phase: PhaseDevelop, BashAllow: []string{"Bash(dotnet build:*)"}, ResumeSession: "abc"})
	if argValue(dev, "--permission-mode") != "acceptEdits" {
		t.Fatal("bypassPermissions nunca pode ser usado")
	}
	if !strings.Contains(argValue(dev, "--allowedTools"), "Bash(dotnet build:*)") || argValue(dev, "--resume") != "abc" {
		t.Fatal("bash liberado/resume")
	}
	dis := argValue(dev, "--disallowedTools")
	for _, must := range []string{"Bash(git push:*)", "Bash(git commit:*)", "WebFetch"} {
		if !strings.Contains(dis, must) {
			t.Fatalf("faltou bloquear %s", must)
		}
	}
	for _, a := range dev {
		if a == "--dangerously-skip-permissions" {
			t.Fatal("flag perigosa")
		}
	}
}

func TestDescribeTool(t *testing.T) {
	e := describeTool("Edit", []byte(`{"file_path":"src/a.cs"}`))
	if e.Kind != "arquivo_alterado" || !strings.Contains(e.Message, "src/a.cs") {
		t.Fatal(e)
	}
}

func TestSystemPromptSingleLineAndLauncher(t *testing.T) {
	c := &Claude{}
	a := c.BuildArgs(Request{Phase: PhaseDevelop, SystemPrompt: "linha 1\nlinha 2"})
	if v := argValue(a, "--append-system-prompt"); strings.Contains(v, "\n") || v != "linha 1 linha 2" {
		t.Fatalf("prompt de sistema deve ser linha única: %q", v)
	}
	exe, args := resolveLauncher("/usr/bin/claude", []string{"-p"})
	if exe != "/usr/bin/claude" || len(args) != 1 {
		t.Fatal("binário nativo não deve ser alterado")
	}
}

func TestHookSettingsUseExecFormAndEffort(t *testing.T) {
	dir := t.TempDir()
	c := &Claude{HookExe: `C:\Program Files\DevPipe\devpipe.exe`, Effort: "low", AnalysisModel: "sonnet", DefaultModel: "opus"}
	man := filepath.Join(dir, "m.txt")
	a := c.BuildArgs(Request{Phase: PhaseAnalysis, WorkDir: dir, EncodingManifest: man})
	if argValue(a, "--effort") != "low" || argValue(a, "--model") != "sonnet" {
		t.Fatalf("args: %v", a)
	}
	if m := argValue(c.BuildArgs(Request{Phase: PhaseDevelop, WorkDir: dir}), "--model"); m != "opus" {
		t.Fatalf("desenvolvimento deve usar o modelo padrão: %s", m)
	}
	b, err := os.ReadFile(argValue(a, "--settings"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string
				Hooks   []struct {
					Command string
					Args    []string
				}
			}
		}
	}
	json.Unmarshal(b, &cfg)
	h := cfg.Hooks.PreToolUse[0]
	if !strings.Contains(h.Matcher, "Read") || !strings.Contains(h.Matcher, "Edit") || h.Hooks[0].Command != c.HookExe ||
		h.Hooks[0].Args[0] != "hook-encoding" || h.Hooks[0].Args[4] != man {
		t.Fatalf("hook: %s", b)
	}
}
