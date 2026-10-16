package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Projeto sem repositório Git: a pasta (existente ou não) é aceita e o DevPipe
// cria um repositório local com branch main e commit inicial.
func TestSaveProjectWithoutRepo(t *testing.T) {
	h, repo := newTestServer(t)
	root := filepath.Dir(repo)
	hdr := map[string]string{"X-DevPipe": "1", "Content-Type": "application/json"}

	existente := filepath.Join(root, "sem-git")
	os.MkdirAll(existente, 0o755)
	os.WriteFile(filepath.Join(existente, "leia.txt"), []byte("oi"), 0o644)
	nova := filepath.Join(root, "pasta-nova")

	for i, dir := range []string{existente, nova} {
		w := do(h, "POST", "/api/projects/inspect", mustJSON(map[string]string{"path": dir}), hdr)
		var ins inspectResult
		json.Unmarshal(w.Body.Bytes(), &ins)
		if ins.Valid || !ins.NeedsInit {
			t.Fatalf("inspect %s: esperado needsInit: %s", dir, w.Body.String())
		}
		w = do(h, "POST", "/api/projects", mustJSON(map[string]any{"name": "P" + string(rune('A'+i)), "path": dir}), hdr)
		if w.Code != 200 {
			t.Fatalf("salvar projeto %s: %d %s", dir, w.Code, w.Body.String())
		}
		out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "main" {
			t.Fatalf("repositório não criado em %s: %v %s", dir, err, out)
		}
	}
	if out, _ := exec.Command("git", "-C", existente, "ls-files").Output(); !strings.Contains(string(out), "leia.txt") {
		t.Fatalf("arquivos existentes deveriam entrar no commit inicial: %q", out)
	}
	// fora dos diretórios autorizados continua bloqueado e nada é criado
	fora := filepath.Join(filepath.Dir(root), "fora")
	if w := do(h, "POST", "/api/projects", mustJSON(map[string]any{"name": "X", "path": fora}), hdr); w.Code == 200 {
		t.Fatal("caminho fora dos autorizados deveria ser recusado")
	}
	if _, err := os.Stat(fora); err == nil {
		t.Fatal("não deveria criar pasta fora dos autorizados")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
