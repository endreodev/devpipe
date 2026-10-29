package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"devpipe/internal/cryptx"
	"devpipe/internal/domain"
	"devpipe/internal/gittest"
	"devpipe/internal/pipeline"
	"devpipe/internal/provider"
	"devpipe/internal/store"
)

func init() { gittest.SemAutoCRLF() }

func newTestServer(t *testing.T) (http.Handler, string) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	k, err := cryptx.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	st.UseKeeper(k)
	reg := provider.NewRegistry()
	eng := pipeline.New(st, reg, pipeline.NewBus(), tmp)
	repo := filepath.Join(tmp, "repos", "r")
	os.MkdirAll(repo, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}} {
		c := exec.Command("git", a...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	s := domain.DefaultSettings()
	s.AuthorizedRoots = []string{filepath.Join(tmp, "repos")}
	s.WorktreeRoot = filepath.Join(tmp, "worktrees") // hermético: nunca a raiz real da máquina
	st.SaveSettings(s)
	return New(st, eng, reg, tmp, 0).Handler(), repo
}

func do(h http.Handler, method, url string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, url, bytes.NewReader(body))
	r.Host = "localhost:7420"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGuards(t *testing.T) {
	h, _ := newTestServer(t)
	if w := do(h, "POST", "/api/projects", []byte(`{}`), nil); w.Code != 403 {
		t.Fatalf("sem X-DevPipe deveria ser 403, obtido %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/meta", nil)
	r.Host = "atacante.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("DNS rebinding não bloqueado")
	}
	if w := do(h, "POST", "/api/projects", []byte(`{}`), map[string]string{"X-DevPipe": "1", "Origin": "https://evil.com"}); w.Code != 403 {
		t.Fatal("origem externa não bloqueada")
	}
	if w := do(h, "GET", "/", nil, nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("DEVPIPE")) {
		t.Fatal("interface não servida")
	}
}

func TestProjectAndDemandFlow(t *testing.T) {
	h, repo := newTestServer(t)
	H := map[string]string{"X-DevPipe": "1", "Content-Type": "application/json"}
	out := do(h, "POST", "/api/projects", []byte(`{"name":"x","path":"/etc"}`), H)
	if out.Code != 400 {
		t.Fatal("caminho fora da raiz deveria falhar")
	}
	body, _ := json.Marshal(map[string]any{"name": "R", "path": repo, "testCommand": "git push"})
	if do(h, "POST", "/api/projects", body, H).Code != 400 {
		t.Fatal("comando destrutivo aceito")
	}
	body, _ = json.Marshal(map[string]any{"name": "R", "path": repo})
	w := do(h, "POST", "/api/projects", body, H)
	if w.Code != 200 {
		t.Fatalf("criar projeto: %s", w.Body)
	}
	var p domain.Project
	json.Unmarshal(w.Body.Bytes(), &p)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	data, _ := json.Marshal(map[string]any{"ticket": "T-1", "kind": domain.KindBug, "title": "Teste", "projectId": p.ID, "instructions": "fazer"})
	mw.WriteField("data", string(data))
	fw, _ := mw.CreateFormFile("files", "req.md")
	fw.Write([]byte("# req"))
	ew, _ := mw.CreateFormFile("files", "x.exe")
	ew.Write([]byte("MZ"))
	mw.Close()
	w = do(h, "POST", "/api/demands", buf.Bytes(), map[string]string{"X-DevPipe": "1", "Content-Type": mw.FormDataContentType()})
	if w.Code != 400 {
		t.Fatalf("executável deveria ser rejeitado: %d %s", w.Code, w.Body)
	}
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	mw.WriteField("data", string(data))
	fw, _ = mw.CreateFormFile("files", "../../req.md")
	fw.Write([]byte("# req"))
	mw.Close()
	w = do(h, "POST", "/api/demands", buf.Bytes(), map[string]string{"X-DevPipe": "1", "Content-Type": mw.FormDataContentType()})
	if w.Code != 200 {
		t.Fatalf("criar demanda: %s", w.Body)
	}
	var d domain.Demand
	json.Unmarshal(w.Body.Bytes(), &d)
	w = do(h, "GET", "/api/demands/"+itoa(d.ID), nil, nil)
	var det struct {
		Attachments []domain.Attachment
		Revisions   []domain.Revision
	}
	json.Unmarshal(w.Body.Bytes(), &det)
	if len(det.Attachments) != 1 || det.Attachments[0].FileName != "req.md" || len(det.Revisions) != 1 {
		t.Fatalf("detalhes: %s", w.Body)
	}
	if do(h, "POST", "/api/demands/"+itoa(d.ID)+"/approve-review", []byte(`{}`), H).Code != 400 {
		t.Fatal("não pode aprovar revisão de demanda no backlog")
	}
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }

// Uma execução ilegível no banco precisa virar erro na API: antes o erro era
// descartado, a resposta saía com "runs": null e a tela quebrava no front.
func TestGetDemandReportsRunsError(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := provider.NewRegistry()
	h := New(st, pipeline.New(st, reg, pipeline.NewBus(), tmp), reg, tmp, 0).Handler()
	p := domain.Project{Name: "R", Path: filepath.Join(tmp, "r")}
	if err := st.SaveProject(&p); err != nil {
		t.Fatal(err)
	}
	d := domain.Demand{Ticket: "T-1", Title: "Teste", ProjectID: p.ID, Provider: "claude"}
	if _, err := st.CreateDemand(&d, ""); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{DemandID: d.ID, RevisionNum: 1, Provider: "claude", Status: domain.StatusRunning, Stage: domain.StageAnalysis}
	if err := st.CreateRun(&run); err != nil {
		t.Fatal(err)
	}
	if w := do(h, "GET", "/api/demands/"+itoa(d.ID), nil, nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"runs":[{`)) {
		t.Fatalf("demanda íntegra: %d %s", w.Code, w.Body)
	}
	st.DB().Exec(`UPDATE runs SET started_at=char(0,0,0,0) WHERE id=?`, run.ID)
	w := do(h, "GET", "/api/demands/"+itoa(d.ID), nil, nil)
	if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("execuções da demanda")) {
		t.Fatalf("esperava erro das execuções, obtido %d %s", w.Code, w.Body)
	}
}

func TestFazendaEndpoint(t *testing.T) {
	h, _ := newTestServer(t)
	w := do(h, "GET", "/api/fazenda", nil, nil)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"clima":"sol"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"demandas":[]`)) {
		t.Fatalf("fazenda: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/fazenda.html", nil, nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("phaser.min.js")) {
		t.Fatal("página da fazenda não servida")
	}
	if w := do(h, "GET", "/vendor/phaser.min.js", nil, nil); w.Code != 200 || w.Body.Len() < 500000 {
		t.Fatal("Phaser não embutido")
	}
}
