package web

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"devpipe/internal/domain"
)

// A senha vai cifrada para o banco, nunca volta nas listagens e só sai pela
// ação explícita de revelar.
func TestDBServerSenhaNuncaVoltaNaListagem(t *testing.T) {
	h, _ := newTestServer(t)
	H := map[string]string{"X-DevPipe": "1", "Content-Type": "application/json"}
	const senha = "S3nh@-secreta"

	body, _ := json.Marshal(map[string]any{"name": "SISPREV_MS", "kind": "sqlserver",
		"host": "srv-sql01", "port": 1433, "database": "SISPREV_MS", "username": "svc_app", "password": senha})
	w := do(h, "POST", "/api/db-servers", body, H)
	if w.Code != 200 {
		t.Fatalf("criar: %d %s", w.Code, w.Body)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(senha)) {
		t.Fatalf("a resposta da criação devolveu a senha: %s", w.Body)
	}
	var d domain.DBServer
	json.Unmarshal(w.Body.Bytes(), &d)
	if !d.HasPassword {
		t.Fatal("hasPassword deveria ser true")
	}

	w = do(h, "GET", "/api/db-servers", nil, nil)
	if bytes.Contains(w.Body.Bytes(), []byte(senha)) {
		t.Fatalf("a listagem devolveu a senha: %s", w.Body)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"hasPassword":true`)) {
		t.Fatalf("listagem sem hasPassword: %s", w.Body)
	}

	// revelar é o único caminho de saída
	w = do(h, "POST", "/api/db-servers/"+itoa(d.ID)+"/reveal", []byte(`{}`), H)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(senha)) {
		t.Fatalf("revelar: %d %s", w.Code, w.Body)
	}
}

// Editar sem informar senha mantém a que já estava guardada.
func TestDBServerEdicaoSemSenhaMantemASenha(t *testing.T) {
	h, _ := newTestServer(t)
	H := map[string]string{"X-DevPipe": "1", "Content-Type": "application/json"}
	const senha = "original123"
	body, _ := json.Marshal(map[string]any{"name": "C1", "password": senha, "username": "u1"})
	w := do(h, "POST", "/api/db-servers", body, H)
	var d domain.DBServer
	json.Unmarshal(w.Body.Bytes(), &d)

	body, _ = json.Marshal(map[string]any{"name": "C1", "username": "u2", "password": ""})
	if w := do(h, "PUT", "/api/db-servers/"+itoa(d.ID), body, H); w.Code != 200 {
		t.Fatalf("editar: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/api/db-servers/"+itoa(d.ID)+"/reveal", []byte(`{}`), H)
	if !bytes.Contains(w.Body.Bytes(), []byte(senha)) {
		t.Fatalf("a senha foi perdida na edição: %s", w.Body)
	}
	// e trocar a senha realmente troca
	body, _ = json.Marshal(map[string]any{"name": "C1", "username": "u2", "password": "nova456"})
	do(h, "PUT", "/api/db-servers/"+itoa(d.ID), body, H)
	w = do(h, "POST", "/api/db-servers/"+itoa(d.ID)+"/reveal", []byte(`{}`), H)
	if !bytes.Contains(w.Body.Bytes(), []byte("nova456")) || bytes.Contains(w.Body.Bytes(), []byte(senha)) {
		t.Fatalf("a senha não foi trocada: %s", w.Body)
	}
}

// Nome duplicado e exclusão com projeto vinculado precisam de erro claro.
func TestDBServerRegrasDeIntegridade(t *testing.T) {
	h, repo := newTestServer(t)
	H := map[string]string{"X-DevPipe": "1", "Content-Type": "application/json"}
	body, _ := json.Marshal(map[string]any{"name": "DUP"})
	if do(h, "POST", "/api/db-servers", body, H).Code != 200 {
		t.Fatal("criar primeira")
	}
	w := do(h, "POST", "/api/db-servers", body, H)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "já existe") {
		t.Fatalf("nome duplicado deveria falhar: %d %s", w.Code, w.Body)
	}
	var lista []domain.DBServer
	json.Unmarshal(do(h, "GET", "/api/db-servers", nil, nil).Body.Bytes(), &lista)
	id := lista[0].ID

	pb, _ := json.Marshal(map[string]any{"name": "R", "path": repo, "dbServerId": id})
	if w := do(h, "POST", "/api/projects", pb, H); w.Code != 200 {
		t.Fatalf("criar projeto: %s", w.Body)
	}
	w = do(h, "DELETE", "/api/db-servers/"+itoa(id), nil, H)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "vinculada") {
		t.Fatalf("excluir conexão em uso deveria falhar: %d %s", w.Code, w.Body)
	}
	// o vínculo volta no cadastro do projeto
	var ps []domain.Project
	json.Unmarshal(do(h, "GET", "/api/projects", nil, nil).Body.Bytes(), &ps)
	if len(ps) != 1 || ps[0].DBServerID != id {
		t.Fatalf("vínculo não persistiu: %+v", ps)
	}
}

func TestDBServerTipoInvalido(t *testing.T) {
	h, _ := newTestServer(t)
	H := map[string]string{"X-DevPipe": "1", "Content-Type": "application/json"}
	body, _ := json.Marshal(map[string]any{"name": "X", "kind": "mongodb"})
	if w := do(h, "POST", "/api/db-servers", body, H); w.Code != 400 {
		t.Fatalf("tipo inválido deveria falhar: %d %s", w.Code, w.Body)
	}
}
