package provider

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCLI cria um executável que imita a saída stream-json do Claude Code
// e grava em arquivo se ANTHROPIC_API_KEY chegou ao processo.
func fakeCLI(t *testing.T, apiKeySource, rateLimit string) (bin, envFile string) {
	if runtime.GOOS == "windows" {
		t.Skip("usa sh")
	}
	dir := t.TempDir()
	envFile = filepath.Join(dir, "env.txt")
	script := `#!/bin/sh
cat >/dev/null
echo "key=${ANTHROPIC_API_KEY:-}" > ` + envFile + `
echo '{"type":"system","subtype":"init","session_id":"s1","model":"m","apiKeySource":"` + apiKeySource + `"}'
echo '{"type":"rate_limit_event","rate_limit_info":` + rateLimit + `}'
sleep 0.2
echo '{"type":"result","subtype":"success","is_error":false,"result":"PRONTO","session_id":"s1","total_cost_usd":0.05,"num_turns":1}'
`
	bin = filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte(script), 0o755)
	return
}

const okLimit = `{"status":"allowed","resetsAt":1791568800,"isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.57,"resetsAt":1791568800},"seven_day":{"utilization":0.24,"resetsAt":1791766800}}}`

func run(t *testing.T, c *Claude) (Result, error) {
	return c.Run(context.Background(), Request{Phase: PhaseProbe, WorkDir: t.TempDir(), Prompt: "x"}, func(Event) {})
}

func TestSubscriptionOK(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secreta")
	bin, envFile := fakeCLI(t, "none", okLimit)
	res, err := run(t, &Claude{Binary: bin, SubscriptionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthMode != "assinatura" || res.Usage == nil || math.Round(res.Usage.FiveHourPct) != 57 || math.Round(res.Usage.SevenDayPct) != 24 {
		t.Fatalf("resultado: %+v %+v", res, res.Usage)
	}
	b, _ := os.ReadFile(envFile)
	if strings.Contains(string(b), "sk-ant") {
		t.Fatal("ANTHROPIC_API_KEY não pode chegar à CLI no modo somente assinatura")
	}
}

func TestAPIKeyAuthIsAborted(t *testing.T) {
	bin, _ := fakeCLI(t, "ANTHROPIC_API_KEY", okLimit)
	_, err := run(t, &Claude{Binary: bin, SubscriptionOnly: true})
	var pol *ErrPolicy
	if !errors.As(err, &pol) {
		t.Fatalf("esperava bloqueio por chave de API, obtido %v", err)
	}
	// com a opção desligada, segue normalmente
	if _, err := run(t, &Claude{Binary: bin}); err != nil {
		t.Fatal(err)
	}
}

func TestOverageIsAborted(t *testing.T) {
	bin, _ := fakeCLI(t, "none", `{"status":"allowed","isUsingOverage":true}`)
	_, err := run(t, &Claude{Binary: bin, SubscriptionOnly: true})
	var pol *ErrPolicy
	if !errors.As(err, &pol) || !strings.Contains(err.Error(), "uso extra") {
		t.Fatalf("esperava bloqueio de uso extra, obtido %v", err)
	}
}

func TestLimitReached(t *testing.T) {
	bin, _ := fakeCLI(t, "none", `{"status":"rejected","resetsAt":1791568800}`)
	_, err := run(t, &Claude{Binary: bin, SubscriptionOnly: true})
	var lim *ErrPlanLimit
	if !errors.As(err, &lim) || lim.ResetsAt.Unix() != 1791568800 {
		t.Fatalf("esperava ErrPlanLimit, obtido %v", err)
	}
}

func TestFilterEnv(t *testing.T) {
	out := FilterEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=x", "anthropic_auth_token=y", "CLAUDE_CODE_USE_BEDROCK=1", "HOME=/h"})
	if len(out) != 2 {
		t.Fatalf("%v", out)
	}
}
