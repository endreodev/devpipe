// Package gitx encapsula as operações Git permitidas pela aplicação.
// Estratégia de isolamento: cada demanda recebe uma "git worktree" própria,
// em um diretório separado, numa branch de trabalho criada a partir da branch
// base. O checkout do usuário (e suas alterações não commitadas) nunca é tocado.
// Nenhuma operação de merge, push, reset ou descarte é executada.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Repo struct{ Dir string }

// Logger recebe cada comando executado para auditoria.
type Logger func(cmd string, out string, err error)

var Log Logger = func(string, string, error) {}

func (r Repo) git(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	s := strings.TrimRight(out.String(), "\r\n")
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		err = fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	Log("git "+strings.Join(args, " "), s, err)
	return s, err
}

// Available informa se o executável git existe.
func Available() error {
	_, err := exec.LookPath("git")
	if err != nil {
		return errors.New("git não encontrado no PATH")
	}
	return nil
}

// Validate confirma que Dir existe e é a raiz (ou parte) de um repositório Git.
func (r Repo) Validate() (top string, err error) {
	st, err := os.Stat(r.Dir)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("diretório não encontrado: %s", r.Dir)
	}
	top, err = r.git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("o diretório não é um repositório Git válido")
	}
	return filepath.FromSlash(top), nil
}

func (r Repo) CurrentBranch() (string, error) {
	b, err := r.git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if b == "HEAD" {
		return "", errors.New("HEAD destacado (detached); selecione uma branch base explícita")
	}
	return b, nil
}

type BranchInfo struct {
	Name    string `json:"name"`
	Remote  bool   `json:"remote"`
	Current bool   `json:"current"`
}

func (r Repo) Branches() ([]BranchInfo, error) {
	cur, _ := r.CurrentBranch()
	out, err := r.git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var list []BranchInfo
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "refs/heads/"):
			n := strings.TrimPrefix(l, "refs/heads/")
			seen[n] = true
			list = append(list, BranchInfo{Name: n, Current: n == cur})
		case strings.HasPrefix(l, "refs/remotes/"):
			n := strings.TrimPrefix(l, "refs/remotes/")
			if strings.HasSuffix(n, "/HEAD") {
				continue
			}
			list = append(list, BranchInfo{Name: n, Remote: true})
		}
	}
	// remove remotas que já existem localmente com o mesmo nome curto
	filtered := list[:0]
	for _, b := range list {
		if b.Remote {
			if i := strings.Index(b.Name, "/"); i > 0 && seen[b.Name[i+1:]] {
				continue
			}
		}
		filtered = append(filtered, b)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Remote != filtered[j].Remote {
			return !filtered[i].Remote
		}
		return filtered[i].Name < filtered[j].Name
	})
	return filtered, nil
}

// StatusPorcelain lista alterações não commitadas (vazio = limpo).
func (r Repo) StatusPorcelain() ([]string, error) {
	out, err := r.git("status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (r Repo) RefExists(ref string) bool {
	_, err := r.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

func (r Repo) LocalBranchExists(name string) bool {
	_, err := r.git("rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

var invalidBranchChars = regexp.MustCompile(`[^A-Za-z0-9._/\-]+`)

// SafeBranchName gera um nome de branch válido e previsível.
func SafeBranchName(prefix, ticket, title string) string {
	base := strings.ToLower(ticket)
	if t := strings.ToLower(title); t != "" {
		t = stripAccents(t)
		words := strings.Fields(invalidBranchChars.ReplaceAllString(t, " "))
		if len(words) > 5 {
			words = words[:5]
		}
		if len(words) > 0 {
			base += "-" + strings.Join(words, "-")
		}
	}
	base = invalidBranchChars.ReplaceAllString(base, "-")
	base = strings.Trim(regexp.MustCompile(`-+`).ReplaceAllString(base, "-"), "-./")
	if base == "" {
		base = "demanda"
	}
	return prefix + base
}

func stripAccents(s string) string {
	r := strings.NewReplacer("á", "a", "à", "a", "ã", "a", "â", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ü", "u", "ç", "c")
	return r.Replace(s)
}

// EnsureWorktree cria (ou reaproveita) a worktree isolada da demanda.
// Nunca remove, sobrescreve ou reseta nada.
func (r Repo) EnsureWorktree(wtPath, workBranch, base string) (created bool, err error) {
	if st, e := os.Stat(wtPath); e == nil && st.IsDir() {
		wr := Repo{Dir: wtPath}
		cur, e := wr.CurrentBranch()
		if e != nil {
			return false, fmt.Errorf("a worktree existente em %s está inválida: %v", wtPath, e)
		}
		if cur != workBranch {
			return false, fmt.Errorf("a worktree %s está na branch %q (esperado %q); intervenção manual necessária", wtPath, cur, workBranch)
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return false, err
	}
	r.git("worktree", "prune") // limpa apenas registros de worktrees cujo diretório não existe mais
	if r.LocalBranchExists(workBranch) {
		_, err = r.git("worktree", "add", wtPath, workBranch)
	} else {
		if !r.RefExists(base) {
			return false, fmt.Errorf("branch base %q não encontrada no repositório", base)
		}
		_, err = r.git("worktree", "add", "-b", workBranch, wtPath, base)
	}
	return err == nil, err
}

func (r Repo) HeadSHA() string {
	s, _ := r.git("rev-parse", "HEAD")
	return s
}

// ChangedSince devolve o diff-stat entre o merge-base com base e o estado atual (incluindo não commitado).
func (r Repo) DiffStat(base string) string {
	mb, err := r.git("merge-base", base, "HEAD")
	if err != nil {
		mb = base
	}
	r.git("add", "-N", ".") // torna arquivos novos visíveis no diff, sem stage de conteúdo
	s, _ := r.git("diff", "--stat", mb)
	return s
}

func (r Repo) Diff(base string, maxBytes int) string {
	mb, err := r.git("merge-base", base, "HEAD")
	if err != nil {
		mb = base
	}
	r.git("add", "-N", ".")
	s, _ := r.git("diff", mb)
	if maxBytes > 0 && len(s) > maxBytes {
		s = s[:maxBytes] + "\n... (diff truncado)"
	}
	return s
}

func (r Repo) ChangedFiles() []string {
	out, _ := r.git("status", "--porcelain")
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if len(l) > 3 {
			files = append(files, strings.TrimSpace(l[3:]))
		}
	}
	return files
}

// Autor dos commits automáticos (configurável em Configuração e segurança,
// gravado apenas no banco local). Vazio = usa user.name/user.email do Git do
// repositório. Nenhuma identidade fica fixa no código.
var CommitAuthorName = ""
var CommitAuthorEmail = ""

// CommitAll faz commit local de tudo na worktree da demanda (sem push).
func (r Repo) CommitAll(msg string) (string, error) {
	files := r.ChangedFiles()
	if len(files) == 0 {
		return "", nil
	}
	if _, err := r.git("add", "-A"); err != nil {
		return "", err
	}
	name, email := strings.TrimSpace(CommitAuthorName), strings.TrimSpace(CommitAuthorEmail)
	if email == "" {
		email, _ = r.git("config", "user.email")
	}
	if name == "" {
		name, _ = r.git("config", "user.name")
	}
	args := []string{}
	if name != "" {
		args = append(args, "-c", "user.name="+name)
	}
	if email != "" {
		args = append(args, "-c", "user.email="+email)
	}
	args = append(args, "commit", "--no-verify", "-m", msg)
	if _, err := r.git(args...); err != nil {
		return "", err
	}
	return r.HeadSHA(), nil
}

// DetectStack identifica linguagem e solução principal por heurística simples.
func DetectStack(dir string) (lang, solution string) {
	counts := map[string]int{}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "bin" || name == "obj" || name == "vendor" || name == "target" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		depth := strings.Count(rel, string(filepath.Separator))
		switch {
		case strings.HasSuffix(name, ".sln") && solution == "":
			solution = rel
		case name == "go.mod" && depth == 0:
			solution = rel
		case name == "package.json" && depth == 0 && solution == "":
			solution = rel
		case name == "Cargo.toml" && depth == 0 && solution == "":
			solution = rel
		case (name == "pom.xml" || name == "build.gradle") && depth == 0 && solution == "":
			solution = rel
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".cs":
			counts["C#"]++
		case ".go":
			counts["Go"]++
		case ".rs":
			counts["Rust"]++
		case ".ts", ".tsx":
			counts["TypeScript"]++
		case ".js", ".jsx":
			counts["JavaScript"]++
		case ".py":
			counts["Python"]++
		case ".java":
			counts["Java"]++
		case ".vb":
			counts["VB.NET"]++
		case ".php":
			counts["PHP"]++
		case ".sql":
			counts["SQL"]++
		}
		return nil
	})
	best := 0
	for k, v := range counts {
		if v > best || (v == best && k < lang) {
			lang, best = k, v
		}
	}
	return
}

// TrackedFiles lista os arquivos versionados (caminhos relativos, com "/").
func (r Repo) TrackedFiles() ([]string, error) {
	out, err := r.gitRaw("-c", "core.quotepath=false", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// ChangedPaths lista arquivos modificados, novos ou não rastreados (sem os removidos).
func (r Repo) ChangedPaths() ([]string, error) {
	out, err := r.gitRaw("-c", "core.quotepath=false", "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	entries := splitZ(out)
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		x, y, path := e[0], e[1], e[3:]
		if x == 'R' || x == 'C' {
			i++ // o próximo campo é o caminho de origem
		}
		if x == 'D' || y == 'D' {
			continue
		}
		files = append(files, path)
	}
	return files, nil
}

// FilesChangedSince lista arquivos alterados em commits desde a referência.
func (r Repo) FilesChangedSince(ref string) []string {
	out, err := r.gitRaw("-c", "core.quotepath=false", "diff", "--name-only", "-z", "--diff-filter=AMR", ref, "HEAD")
	if err != nil {
		return nil
	}
	return splitZ(out)
}

// MergeBase devolve o ancestral comum entre base e HEAD.
func (r Repo) MergeBase(base string) string {
	s, err := r.git("merge-base", base, "HEAD")
	if err != nil {
		return ""
	}
	return s
}

// ShowFile devolve o conteúdo de um arquivo em uma revisão (ok=false se não existir).
func (r Repo) ShowFile(rev, path string) ([]byte, bool) {
	if rev == "" {
		return nil, false
	}
	out, err := r.gitRaw("show", rev+":"+path)
	if err != nil {
		return nil, false
	}
	return out, true
}

// gitRaw executa sem aparar a saída (conteúdo binário/exato).
func (r Repo) gitRaw(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func splitZ(b []byte) []string {
	var out []string
	for _, p := range bytes.Split(b, []byte{0}) {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

// ---------- trabalho direto na branch selecionada ----------

// BranchTip devolve o SHA da ponta de uma branch local.
func (r Repo) BranchTip(branch string) (string, error) {
	return r.git("rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
}

// IsAncestor indica se a é ancestral de (ou igual a) b.
func (r Repo) IsAncestor(a, b string) bool {
	_, err := r.git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// ResolveLocalBranch converte a branch escolhida na tela em uma branch local.
// Se for remota (ex.: origin/release) e não existir localmente, cria a branch
// local de acompanhamento com o mesmo nome (release) — sem criar nome novo.
func (r Repo) ResolveLocalBranch(name string) (local string, created bool, err error) {
	if r.LocalBranchExists(name) {
		return name, false, nil
	}
	if r.RefExists("refs/remotes/" + name) {
		if i := strings.Index(name, "/"); i > 0 {
			local = name[i+1:]
			if r.LocalBranchExists(local) {
				return local, false, nil
			}
			if _, err := r.git("branch", "--track", local, "refs/remotes/"+name); err != nil {
				return "", false, err
			}
			return local, true, nil
		}
	}
	return "", false, fmt.Errorf("branch %q não encontrada no repositório", name)
}

// WorktreeOfBranch devolve o diretório onde a branch está em checkout
// (checkout principal ou outra worktree), ou "" se não estiver.
func (r Repo) WorktreeOfBranch(branch string) string {
	out, err := r.git("worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	dir := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "worktree ") {
			dir = strings.TrimPrefix(l, "worktree ")
		} else if l == "branch refs/heads/"+branch {
			return filepath.FromSlash(dir)
		}
	}
	return ""
}

// EnsureDetachedWorktree cria (ou reaproveita) uma worktree em HEAD destacado
// no commit informado. Ela não "prende" a branch: o checkout principal continua
// podendo usar a mesma branch normalmente.
func (r Repo) EnsureDetachedWorktree(wtPath, commit string) (created bool, err error) {
	if st, e := os.Stat(wtPath); e == nil && st.IsDir() {
		if _, e := (Repo{Dir: wtPath}).git("rev-parse", "HEAD"); e != nil {
			return false, fmt.Errorf("a worktree existente em %s está inválida: %v", wtPath, e)
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return false, err
	}
	r.git("worktree", "prune")
	_, err = r.git("worktree", "add", "--detach", wtPath, commit)
	return err == nil, err
}

// PublishToBranch avança a branch até newSHA somente por fast-forward
// (nunca reescreve histórico nem descarta commits de outras pessoas).
// Se a branch estiver em checkout (por exemplo, aberta no VS Code), faz
// "git merge --ff-only" naquele diretório para atualizar também os arquivos;
// o Git recusa se houver alterações locais conflitantes.
func (r Repo) PublishToBranch(branch, newSHA string) (onde string, err error) {
	tip, err := r.BranchTip(branch)
	if err != nil {
		return "", err
	}
	if tip == newSHA {
		return "a branch já estava neste commit", nil
	}
	if !r.IsAncestor(tip, newSHA) {
		return "", fmt.Errorf("a branch %s recebeu outros commits depois do início da demanda (não é possível avançar por fast-forward)", branch)
	}
	if dir := r.WorktreeOfBranch(branch); dir != "" {
		if _, err := (Repo{Dir: dir}).git("merge", "--ff-only", "--no-edit", newSHA); err != nil {
			return "", fmt.Errorf("não foi possível atualizar o checkout %s (há alterações locais nos mesmos arquivos?): %v", dir, err)
		}
		return "checkout " + dir + " atualizado (fast-forward)", nil
	}
	if _, err := r.git("update-ref", "-m", "DevPipe: fast-forward", "refs/heads/"+branch, newSHA, tip); err != nil {
		return "", err
	}
	return "branch atualizada (fast-forward)", nil
}

// DetachTo move uma worktree destacada para outro commit (somente se limpa).
func (r Repo) DetachTo(commit string) error {
	_, err := r.git("checkout", "--detach", commit)
	return err
}
