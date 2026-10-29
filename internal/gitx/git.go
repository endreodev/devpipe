// Package gitx encapsula as operações Git permitidas pela aplicação.
// Estratégia de isolamento: cada demanda recebe uma "git worktree" própria,
// em um diretório separado, numa branch de trabalho criada a partir da branch
// base. O checkout do usuário (e suas alterações não commitadas) nunca é tocado.
// Nenhuma operação de merge, reset, rebase ou descarte é executada. A única
// escrita fora da máquina é Push, que publica a branch da demanda por avanço
// (fast-forward), nunca com --force, e só é chamada após aprovação humana.
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
	"strconv"
	"strings"
	"time"
)

type Repo struct{ Dir string }

// Logger recebe cada comando executado para auditoria.
type Logger func(cmd string, out string, err error)

var Log Logger = func(string, string, error) {}

func (r Repo) git(args ...string) (string, error) {
	return r.gitTimeout(2*time.Minute, args...)
}

// gitQuiet roda um comando cuja falha é esperada e não interessa à auditoria
// (consultas em que "não existe" é uma resposta válida). Sucesso continua
// sendo registrado; só o erro é omitido do log.
func (r Repo) gitQuiet(args ...string) (string, error) {
	out, err := r.run(2*time.Minute, args...)
	if err == nil {
		Log("git "+strings.Join(args, " "), out, nil)
	}
	return out, err
}

func (r Repo) gitTimeout(d time.Duration, args ...string) (string, error) {
	out, err := r.run(d, args...)
	Log("git "+strings.Join(args, " "), out, err)
	return out, err
}

func (r Repo) run(d time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
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
	return s, err // quem registra na auditoria é gitTimeout/gitQuiet
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

// defaultGitignore evita versionar saídas de build e dependências quando o
// DevPipe cria o repositório de um projeto novo (só é gravado se não existir).
const defaultGitignore = `bin/
obj/
node_modules/
.vs/
*.user
*.suo
`

// Init cria um repositório Git LOCAL (sem remoto) em r.Dir para projetos que
// ainda não têm repositório: cria a pasta se preciso, define a branch "main"
// e faz um commit inicial com os arquivos já existentes (ou vazio, se a pasta
// estiver vazia). Retorna o caminho raiz do repositório criado.
func (r Repo) Init() (string, error) {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return "", fmt.Errorf("não foi possível criar a pasta %s: %w", r.Dir, err)
	}
	if top, err := r.Validate(); err == nil {
		return top, nil // já é um repositório (ou está dentro de um)
	}
	if _, err := r.git("init"); err != nil {
		return "", err
	}
	if _, err := r.git("symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return "", err
	}
	gi := filepath.Join(r.Dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		os.WriteFile(gi, []byte(defaultGitignore), 0o644)
	}
	if _, err := r.git("add", "-A"); err != nil {
		return "", err
	}
	args := []string{"commit", "--allow-empty", "-q", "-m", "Commit inicial"}
	if email, _ := r.git("config", "user.email"); strings.TrimSpace(email) == "" {
		// Sem identidade Git na máquina: usa uma só para este commit. O nome
		// fica gravado no histórico do repositório, então não pode identificar
		// a ferramenta — se o autor foi configurado, é ele que vale.
		nome, email := strings.TrimSpace(CommitAuthorName), strings.TrimSpace(CommitAuthorEmail)
		if nome == "" {
			nome = "Desenvolvimento"
		}
		if email == "" {
			email = "desenvolvimento@localhost"
		}
		args = append([]string{"-c", "user.name=" + nome, "-c", "user.email=" + email}, args...)
	}
	if _, err := r.git(args...); err != nil {
		return "", err
	}
	return r.Validate()
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
	// Upstream, Behind e Ahead comparam a branch local com o que o último
	// fetch trouxe do remoto. Behind > 0 significa que desenvolver a partir
	// dela começaria com código desatualizado.
	Upstream string `json:"upstream,omitempty"`
	Behind   int    `json:"behind,omitempty"`
	Ahead    int    `json:"ahead,omitempty"`
}

// FetchResult resume a atualização das referências remotas.
type FetchResult struct {
	Done    bool   `json:"done"`    // o fetch rodou com sucesso
	Skipped bool   `json:"skipped"` // repositório sem remoto configurado
	Error   string `json:"error,omitempty"`
}

// PushResult resume a publicação da branch de trabalho no servidor.
type PushResult struct {
	Done    bool   `json:"done"`              // o push rodou com sucesso
	Skipped bool   `json:"skipped"`           // repositório sem remoto configurado
	Already bool   `json:"already,omitempty"` // o servidor já estava neste commit
	Remote  string `json:"remote,omitempty"`  // remoto usado (origin, quando existe)
	Branch  string `json:"branch,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Fetch atualiza as referências remotas (refs/remotes/*) e remove as que não
// existem mais no servidor.
//
// É a forma SEGURA de "atualizar os códigos": não altera nenhuma branch local,
// não mexe na árvore de trabalho e não pode gerar conflito — ao contrário de
// `git pull`, que faz merge no checkout do usuário e é proibido pela política
// da esteira. Com o fetch em dia, Branches() consegue dizer quantos commits
// cada branch local está atrás do remoto, e uma demanda pode usar a própria
// ref remota (ex.: origin/main) como base.
//
// Nunca pede credencial (GIT_TERMINAL_PROMPT=0) e tem tempo limite curto: a
// tela do cadastro espera por ele.
func (r Repo) Fetch() FetchResult {
	remotes, err := r.git("remote")
	if err != nil {
		return FetchResult{Error: err.Error()}
	}
	if strings.TrimSpace(remotes) == "" {
		return FetchResult{Skipped: true} // repositório local, sem servidor
	}
	if _, err := r.gitTimeout(45*time.Second, "fetch", "--all", "--prune", "--no-tags", "--quiet"); err != nil {
		return FetchResult{Error: err.Error()}
	}
	return FetchResult{Done: true}
}

// aheadBehind conta os commits que separam a branch local do seu upstream.
//
// Branch sem upstream é o caso NORMAL (toda branch local criada aqui começa
// assim), não um erro: a consulta usa gitQuiet para não encher o devpipe.log
// com um "fatal: no upstream configured" por branch a cada vez que a tela de
// cadastro lista as branches.
func (r Repo) aheadBehind(branch string) (upstream string, ahead, behind int) {
	upstream, err := r.gitQuiet("rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil || upstream == "" {
		return "", 0, 0
	}
	out, err := r.git("rev-list", "--left-right", "--count", branch+"..."+upstream)
	if err != nil {
		return upstream, 0, 0
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return upstream, 0, 0
	}
	ahead, _ = strconv.Atoi(f[0])
	behind, _ = strconv.Atoi(f[1])
	return upstream, ahead, behind
}

func (r Repo) Branches() ([]BranchInfo, error) {
	cur, _ := r.CurrentBranch()
	out, err := r.git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	atrasada := map[string]bool{} // local que ficou para trás do seu upstream
	var list []BranchInfo
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "refs/heads/"):
			n := strings.TrimPrefix(l, "refs/heads/")
			seen[n] = true
			up, ahead, behind := r.aheadBehind(n)
			if behind > 0 {
				atrasada[n] = true
			}
			list = append(list, BranchInfo{Name: n, Current: n == cur, Upstream: up, Ahead: ahead, Behind: behind})
		case strings.HasPrefix(l, "refs/remotes/"):
			n := strings.TrimPrefix(l, "refs/remotes/")
			if strings.HasSuffix(n, "/HEAD") {
				continue
			}
			list = append(list, BranchInfo{Name: n, Remote: true})
		}
	}
	// Remove as remotas redundantes: quando já existe uma local de mesmo nome
	// curto E ela está em dia, as duas apontam para o mesmo commit e listar as
	// duas só confunde. Se a local está ATRASADA, a remota permanece na lista —
	// é ela que permite começar a demanda a partir do código atual do servidor
	// sem alterar nada no checkout do usuário.
	filtered := list[:0]
	for _, b := range list {
		if b.Remote {
			if i := strings.Index(b.Name, "/"); i > 0 && seen[b.Name[i+1:]] && !atrasada[b.Name[i+1:]] {
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

// DefaultBaseNames é a ordem de preferência da branch base quando a demanda
// não escolhe nenhuma: main sempre primeiro.
var DefaultBaseNames = []string{"main", "master"}

// DefaultBase devolve a branch base padrão do repositório: "main" quando
// existe (local ou em origin), depois "master" e, só então, a branch que
// estiver em checkout. Nunca altera o checkout do usuário.
func (r Repo) DefaultBase() (string, error) {
	for _, n := range DefaultBaseNames {
		if r.LocalBranchExists(n) {
			return n, nil
		}
	}
	for _, n := range DefaultBaseNames {
		if r.RefExists("refs/remotes/origin/" + n) {
			return "origin/" + n, nil
		}
	}
	return r.CurrentBranch()
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
// DevSuffix identifica a branch em que a esteira desenvolve, conforme a
// convenção da equipe.
const DevSuffix = "_Desenvolvimento"

// BranchName monta o nome da branch de trabalho no padrão da equipe:
//
//	<TIPO>/<TICKET>_Desenvolvimento   ex.: BUG/CH101010_Desenvolvimento
//
// O tipo (BUG, FIX, NR) vem do cadastro da demanda. O ticket é usado como foi
// digitado — só os caracteres que o Git não aceita em um nome de referência
// são trocados por "-", para que o nome continue reconhecível no Azure DevOps.
func BranchName(kind, ticket string) string {
	return branchWithSuffix(kind, ticket, DevSuffix)
}

// TargetBranchName monta o nome da branch de teste, que segue a mesma convenção
// trocando o sufixo pelo alvo escolhido:
//
//	<TIPO>/<TICKET>_<alvo>   ex.: BUG/CH101010_stage, BUG/CH101010_release
//
// O alvo é o nome da branch de origem (sem o remoto: origin/release -> release).
func TargetBranchName(kind, ticket, alvo string) string {
	a := sanitizeRefPart(TargetName(alvo))
	if a == "" {
		a = "teste"
	}
	return branchWithSuffix(kind, ticket, "_"+a)
}

// TargetName reduz a branch de origem escolhida ao nome usado no sufixo:
// "origin/release" -> "release".
func TargetName(alvo string) string {
	alvo = strings.TrimSpace(alvo)
	if i := strings.LastIndex(alvo, "/"); i >= 0 {
		alvo = alvo[i+1:]
	}
	return alvo
}

// NormalizeBranchName limpa um nome de branch digitado na tela, preservando a
// convenção <TIPO>/<TICKET>_<sufixo>: cada segmento passa pelas mesmas regras
// de BranchName (acento removido, caractere recusado pelo Git vira "-") e as
// barras separadoras são mantidas. Devolve "" quando não sobra nada válido.
func NormalizeBranchName(s string) string {
	var partes []string
	for _, p := range strings.Split(strings.TrimSpace(s), "/") {
		if p = sanitizeRefPart(p); p != "" {
			partes = append(partes, p)
		}
	}
	return strings.Join(partes, "/")
}

func branchWithSuffix(kind, ticket, sufixo string) string {
	t := sanitizeRefPart(ticket)
	if t == "" {
		t = "demanda"
	}
	k := sanitizeRefPart(kind)
	if k == "" {
		return t + sufixo
	}
	return k + "/" + t + sufixo
}

var repeatedDashes = regexp.MustCompile(`-+`)

// invalidRefPart: o que não pode aparecer DENTRO de um segmento. Ao contrário
// de invalidBranchChars, a barra também é trocada — a convenção tem exatamente
// uma, entre o tipo e o ticket.
var invalidRefPart = regexp.MustCompile(`[^A-Za-z0-9._\-]+`)

// sanitizeRefPart deixa um segmento válido para git check-ref-format,
// preservando maiúsculas, minúsculas e os separadores que o Git aceita.
func sanitizeRefPart(s string) string {
	s = stripAccents(strings.TrimSpace(s))
	s = invalidRefPart.ReplaceAllString(s, "-")
	s = repeatedDashes.ReplaceAllString(s, "-")
	s = strings.ReplaceAll(s, "..", "-") // ".." é proibido em nome de referência
	s = strings.Trim(s, "-./")
	return strings.TrimSuffix(s, ".lock") // sufixo reservado pelo Git
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
	args := r.identityArgs()
	args = append(args, "commit", "--no-verify", "-m", msg)
	if _, err := r.git(args...); err != nil {
		return "", err
	}
	return r.HeadSHA(), nil
}

// identityArgs devolve o prefixo "-c user.name=... -c user.email=..." do
// commit. Vazio quando o Git da máquina já tem identidade e nenhum autor foi
// configurado na aplicação.
func (r Repo) identityArgs() []string {
	name, email := strings.TrimSpace(CommitAuthorName), strings.TrimSpace(CommitAuthorEmail)
	if email == "" {
		email, _ = r.git("config", "user.email")
	}
	if name == "" {
		name, _ = r.git("config", "user.name")
	}
	var args []string
	if name != "" {
		args = append(args, "-c", "user.name="+name)
	}
	if email != "" {
		args = append(args, "-c", "user.email="+email)
	}
	return args
}

// CommitPaths commita SOMENTE os caminhos indicados.
//
// Usado quando a branch da demanda está aberta no checkout do usuário: o que
// ele tiver alterado em OUTROS arquivos continua pendente, exatamente como
// estava — "git commit --only" não encosta no resto do índice. Devolve ""
// quando não há nada a commitar nesses caminhos.
func (r Repo) CommitPaths(paths []string, msg string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	if _, err := r.git(append([]string{"add", "--"}, paths...)...); err != nil {
		return "", err
	}
	out, err := r.git(append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	args := append(r.identityArgs(), "commit", "--no-verify", "--only", "-m", msg, "--")
	if _, err := r.git(append(args, paths...)...); err != nil {
		return "", err
	}
	return r.HeadSHA(), nil
}

// FilesSince lista os arquivos que a demanda tocou desde base: os dos commits
// da área de trabalho e os que ainda estiverem pendentes nela.
func (r Repo) FilesSince(base string) []string {
	vistos := map[string]bool{}
	var out []string
	add := func(fs []string) {
		for _, f := range fs {
			if f = strings.TrimSpace(f); f != "" && !vistos[f] {
				vistos[f] = true
				out = append(out, f)
			}
		}
	}
	if raw, err := r.gitRaw("-c", "core.quotepath=false", "diff", "--name-only", "-z", base, "HEAD"); err == nil {
		add(splitZ(raw))
	}
	if pend, err := r.ChangedPaths(); err == nil {
		add(pend)
	}
	sort.Strings(out)
	return out
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
	if _, err := r.git("update-ref", "-m", "fast-forward", "refs/heads/"+branch, newSHA, tip); err != nil {
		return "", err
	}
	return "branch atualizada (fast-forward)", nil
}

// Push publica a branch de trabalho no servidor.
//
// É a ÚNICA operação da aplicação que escreve fora da máquina do usuário e
// por isso nunca é automática: só roda depois da aprovação humana da revisão
// e apenas se o projeto tiver a opção habilitada. As restrições são fixas:
//
//   - refspec explícita refs/heads/<branch>:refs/heads/<branch> — publica só
//     a branch da demanda, nunca outra ref, nunca tags, nunca "--all";
//   - sem --force, --force-with-lease ou qualquer variante: se o servidor
//     recusar por não ser avanço (fast-forward), o push falha e o histórico
//     remoto fica exatamente como estava — nada é reescrito nem descartado;
//   - GIT_TERMINAL_PROMPT=0 (herdado de run): sem credencial configurada o
//     comando falha em vez de travar esperando digitação.
//
// Sem remoto configurado não é erro: não há onde publicar (Skipped).
func (r Repo) Push(branch string) PushResult {
	res := PushResult{Branch: branch}
	remotes, err := r.git("remote")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Remote = pickRemote(remotes)
	if res.Remote == "" {
		res.Skipped = true // repositório local, sem servidor
		return res
	}
	tip, err := r.BranchTip(branch)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if rtip, err := r.gitQuiet("rev-parse", "--verify", "refs/remotes/"+res.Remote+"/"+branch+"^{commit}"); err == nil && rtip == tip {
		res.Done, res.Already = true, true
		return res
	}
	ref := "refs/heads/" + branch
	if _, err := r.gitTimeout(3*time.Minute, "push", "--set-upstream", res.Remote, ref+":"+ref); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Done = true
	return res
}

// pickRemote escolhe o remoto do push: origin quando existe, senão o primeiro
// configurado. Repositório sem remoto devolve "".
func pickRemote(out string) string {
	first := ""
	for _, n := range strings.Fields(out) {
		if n == "origin" {
			return n
		}
		if first == "" {
			first = n
		}
	}
	return first
}

// DetachTo move uma worktree destacada para outro commit (somente se limpa).
func (r Repo) DetachTo(commit string) error {
	_, err := r.git("checkout", "--detach", commit)
	return err
}

// PatchSince devolve o patch (binário) de tudo o que mudou na worktree desde
// base: commits da demanda + alterações ainda não commitadas + arquivos novos.
func (r Repo) PatchSince(base string) ([]byte, error) {
	if _, err := r.git("add", "-A"); err != nil { // índice da worktree da demanda (não do usuário)
		return nil, err
	}
	return r.gitRaw("diff", "--cached", "--binary", "--full-index", base)
}

// PatchFiles lista os arquivos tocados pelo patch.
func (r Repo) PatchFiles(base string) []string {
	out, err := r.git("diff", "--cached", "--name-only", base)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (r Repo) applyFile(patchPath string, extra ...string) error {
	args := append([]string{"apply", "--binary", "--whitespace=nowarn"}, extra...)
	_, err := r.git(append(args, patchPath)...)
	return err
}

// ApplyPatchToWorkingTree aplica o patch nos arquivos do checkout como
// alterações pendentes (não faz commit, não mexe no índice). Antes confere
// se aplica sem conflito; se o patch já estiver aplicado, não faz nada.
func (r Repo) ApplyPatchToWorkingTree(patchPath string) (jaAplicado bool, err error) {
	if r.applyFile(patchPath, "--check") == nil {
		return false, r.applyFile(patchPath)
	}
	if r.applyFile(patchPath, "--reverse", "--check") == nil {
		return true, nil
	}
	return false, r.applyFile(patchPath, "--check")
}

// FileChange descreve um arquivo alterado em relação à base.
type FileChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A (novo) | M (alterado) | D (excluído) | T (tipo)
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
}

func (r Repo) diffBase(base string) string {
	if mb, err := r.git("merge-base", base, "HEAD"); err == nil {
		return mb
	}
	return base
}

// DiffFiles lista os arquivos alterados (inclui arquivos novos não commitados).
func (r Repo) DiffFiles(base string) ([]FileChange, error) {
	mb := r.diffBase(base)
	r.git("add", "-N", ".")
	ns, err := r.git("-c", "core.quotepath=false", "diff", "--no-renames", "--name-status", mb)
	if err != nil {
		return nil, err
	}
	nums, _ := r.git("-c", "core.quotepath=false", "diff", "--no-renames", "--numstat", mb)
	cont := map[string][3]int{} // added, removed, binário
	for _, l := range strings.Split(nums, "\n") {
		p := strings.SplitN(l, "\t", 3)
		if len(p) != 3 {
			continue
		}
		if p[0] == "-" {
			cont[p[2]] = [3]int{0, 0, 1}
			continue
		}
		a, _ := strconv.Atoi(p[0])
		d, _ := strconv.Atoi(p[1])
		cont[p[2]] = [3]int{a, d, 0}
	}
	var out []FileChange
	for _, l := range strings.Split(ns, "\n") {
		p := strings.SplitN(l, "\t", 2)
		if len(p) != 2 {
			continue
		}
		c := cont[p[1]]
		out = append(out, FileChange{Path: p[1], Status: p[0][:1], Added: c[0], Removed: c[1], Binary: c[2] == 1})
	}
	return out, nil
}

// FileDiff devolve o diff de um arquivo com o arquivo inteiro como contexto
// (usado pelo comparador lado a lado).
func (r Repo) FileDiff(base, path string) ([]byte, error) {
	mb := r.diffBase(base)
	r.git("add", "-N", ".")
	return r.gitRaw("-c", "core.quotepath=false", "diff", "--no-renames", "--no-color", "-U1000000", mb, "--", path)
}
