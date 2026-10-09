// Package pipeline orquestra a esteira: fila, prioridades, etapas, execução do
// agente, validação, revisões, cancelamento e recuperação após falhas.
package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"devpipe/internal/docs"
	"devpipe/internal/domain"
	"devpipe/internal/gitx"
	"devpipe/internal/procx"
	"devpipe/internal/provider"
	"devpipe/internal/rules"
	"devpipe/internal/security"
	"devpipe/internal/store"
)

type Engine struct {
	st      *store.Store
	reg     *provider.Registry
	bus     *Bus
	dataDir string

	mu        sync.Mutex
	running   map[int64]context.CancelFunc
	repoLocks map[string]int64
	wake      chan struct{}
}

func New(st *store.Store, reg *provider.Registry, bus *Bus, dataDir string) *Engine {
	return &Engine{st: st, reg: reg, bus: bus, dataDir: dataDir,
		running: map[int64]context.CancelFunc{}, repoLocks: map[string]int64{}, wake: make(chan struct{}, 1)}
}

func (e *Engine) Bus() *Bus { return e.bus }

// AttachmentDir: onde ficam os originais e extrações de uma demanda.
func (e *Engine) AttachmentDir(demandID int64, rev int) string {
	return filepath.Join(e.dataDir, "anexos", fmt.Sprint(demandID), fmt.Sprintf("rev%d", rev))
}

func (e *Engine) worktreePath(p domain.Project, d domain.Demand) string {
	return filepath.Join(e.dataDir, "worktrees", fmt.Sprintf("p%d", p.ID), fmt.Sprintf("%d-%s", d.ID, sanitize(d.Ticket)))
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	s = unsafeChars.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-.")
}

// ---------------------------------------------------------------- ciclo

// Start recupera execuções interrompidas e inicia o agendador.
func (e *Engine) Start(ctx context.Context) {
	e.recover()
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			e.schedule()
			select {
			case <-ctx.Done():
				e.mu.Lock()
				for _, c := range e.running {
					c()
				}
				e.mu.Unlock()
				return
			case <-t.C:
			case <-e.wake:
			}
		}
	}()
}

func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) recover() {
	ids, err := e.st.MarkInterruptedRuns()
	if err != nil {
		log.Printf("recuperação: %v", err)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	all, _ := e.st.ListDemands(store.DemandFilter{})
	for _, d := range all {
		if d.Status == domain.StatusRunning || seen[d.ID] {
			from := d.Stage
			if from == domain.StageBlocked {
				from = d.BlockedFrom
			}
			d.BlockedFrom, d.Stage, d.Status = from, domain.StageBlocked, domain.StatusFailed
			d.StatusMessage = "Execução interrompida porque a aplicação foi encerrada. Use 'Retomar' para continuar de onde parou."
			e.touch(&d, d.StatusMessage)
			e.save(&d)
			e.log(d, 0, domain.LevelWarn, "recuperacao", "Execução interrompida detectada na inicialização", "")
		}
	}
}

func (e *Engine) schedule() {
	st, _ := e.st.GetSettings()
	max := st.MaxParallel
	if max < 1 {
		max = 1
	}
	queued, err := e.st.ListDemands(store.DemandFilter{})
	if err != nil {
		return
	}
	for _, d := range queued {
		if d.Status != domain.StatusQueued {
			continue
		}
		p, err := e.st.GetProject(d.ProjectID)
		if err != nil {
			continue
		}
		key := repoKey(p.Path)
		e.mu.Lock()
		if len(e.running) >= max {
			e.mu.Unlock()
			return
		}
		if _, busy := e.repoLocks[key]; busy {
			e.mu.Unlock()
			continue // outra demanda já trabalha neste repositório
		}
		if _, already := e.running[d.ID]; already {
			e.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		e.running[d.ID] = cancel
		e.repoLocks[key] = d.ID
		e.mu.Unlock()
		go func(d domain.Demand) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("pânico na demanda %d: %v", d.ID, r)
					cur, _ := e.st.GetDemand(d.ID)
					e.block(&cur, nil, cur.Stage, fmt.Errorf("erro interno inesperado: %v", r))
				}
				e.mu.Lock()
				delete(e.running, d.ID)
				delete(e.repoLocks, key)
				e.mu.Unlock()
				cancel()
				e.Wake()
			}()
			e.process(ctx, d)
		}(d)
	}
}

func repoKey(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		a = p
	}
	return strings.ToLower(filepath.Clean(a))
}

func (e *Engine) IsRunning(id int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.running[id]
	return ok
}

// ---------------------------------------------------------------- persistência/eventos

func progressFor(s domain.Stage, prev int) int {
	i := domain.StageIndex(s)
	if i < 0 {
		return prev
	}
	return i * 100 / (len(domain.FlowStages) - 1)
}

func (e *Engine) touch(d *domain.Demand, activity string) {
	t := time.Now().UTC()
	d.LastActivity = truncate(activity, 300)
	d.LastActivityAt = &t
}

func (e *Engine) save(d *domain.Demand) {
	if err := e.st.UpdateDemandState(*d); err != nil {
		log.Printf("salvar demanda %d: %v", d.ID, err)
	}
	if fresh, err := e.st.GetDemand(d.ID); err == nil {
		e.bus.Publish(Message{Type: "demand", Data: fresh})
	}
}

func (e *Engine) log(d domain.Demand, runID int64, level, kind, msg, detail string) {
	ev := domain.Event{DemandID: d.ID, Ticket: d.Ticket, ProjectID: d.ProjectID, Project: d.ProjectName,
		RunID: runID, Stage: d.Stage, Level: level, Kind: kind,
		Message: security.Redact(msg), Detail: security.Redact(truncate(detail, 20000))}
	if err := e.st.AddEvent(&ev); err != nil {
		log.Printf("evento: %v", err)
		return
	}
	e.bus.Publish(Message{Type: "event", Data: ev})
}

// SystemLog registra eventos que não pertencem a uma demanda.
func (e *Engine) SystemLog(level, kind, msg string) {
	e.log(domain.Demand{}, 0, level, kind, msg, "")
}

// ---------------------------------------------------------------- execução

type runState struct {
	d        *domain.Demand
	run      *domain.Run
	started  time.Time
	baseSecs int64
}

func (e *Engine) setStage(rs *runState, s domain.Stage, status, msg string) {
	d := rs.d
	d.Stage, d.Status, d.StatusMessage = s, status, msg
	d.Progress = progressFor(s, d.Progress)
	d.ElapsedSeconds = rs.baseSecs + int64(time.Since(rs.started).Seconds())
	e.touch(d, msg)
	e.save(d)
	if rs.run != nil {
		rs.run.Stage = s
		e.st.UpdateRun(*rs.run)
	}
	e.log(*d, runID(rs.run), domain.LevelInfo, "etapa", "Etapa: "+domain.StageLabels[s]+" — "+msg, "")
}

func runID(r *domain.Run) int64 {
	if r == nil {
		return 0
	}
	return r.ID
}

func (e *Engine) block(d *domain.Demand, run *domain.Run, from domain.Stage, err error) {
	if from == domain.StageBlocked {
		from = d.BlockedFrom
	}
	status, level := domain.StatusFailed, domain.LevelError
	msg := err.Error()
	if errors.Is(err, context.Canceled) || strings.Contains(msg, "cancelad") {
		status, level = domain.StatusCancelled, domain.LevelWarn
		msg = "Execução cancelada pelo usuário. " + msg
	}
	d.BlockedFrom, d.Stage, d.Status, d.StatusMessage = from, domain.StageBlocked, status, truncate(msg, 1500)
	e.touch(d, d.StatusMessage)
	e.save(d)
	if run != nil {
		t := time.Now().UTC()
		run.Status, run.Error, run.FinishedAt = status, truncate(msg, 4000), &t
		e.st.UpdateRun(*run)
	}
	e.log(*d, runID(run), level, "bloqueio", "Demanda bloqueada em "+domain.StageLabels[from], msg)
}

func (e *Engine) process(ctx context.Context, d domain.Demand) {
	settings, _ := e.st.GetSettings()
	p, err := e.st.GetProject(d.ProjectID)
	if err != nil {
		e.block(&d, nil, d.Stage, fmt.Errorf("projeto não encontrado"))
		return
	}
	resumeFrom := d.Stage
	if d.Stage == domain.StageBlocked {
		resumeFrom = d.BlockedFrom
	}
	rs := &runState{d: &d, started: time.Now(), baseSecs: d.ElapsedSeconds}
	now := time.Now().UTC()
	if d.StartedAt == nil {
		d.StartedAt = &now
	}
	d.Attempts++

	provID := d.Provider
	if provID == "" {
		provID = settings.DefaultProvider
	}
	prov, err := e.reg.Get(provID)
	if err != nil {
		e.block(&d, nil, resumeFrom, err)
		return
	}

	rev, err := e.st.GetRevision(d.ID, d.CurrentRevision)
	if err != nil {
		e.block(&d, nil, resumeFrom, fmt.Errorf("revisão %d não encontrada", d.CurrentRevision))
		return
	}

	// --- segurança do caminho
	if !security.IsPathAuthorized(p.Path, settings.AuthorizedRoots) {
		e.block(&d, nil, resumeFrom, fmt.Errorf("o caminho do projeto %s não está na lista de diretórios autorizados (Configurações > Segurança)", p.Path))
		return
	}

	wt := e.worktreePath(p, d)
	wtExists := dirExists(wt)
	standardsRoot := p.Path
	if wtExists {
		standardsRoot = wt
	}
	allRules, _ := e.st.ListRules(p.ID, true)
	composed := rules.Compose(allRules, p, standardsRoot)

	run := &domain.Run{DemandID: d.ID, RevisionNum: rev.Number, Provider: prov.ID(), Status: domain.StatusRunning,
		Stage: resumeFrom, RulesHash: composed.Hash, RulesText: composed.Text}
	if err := e.st.CreateRun(run); err != nil {
		e.block(&d, nil, resumeFrom, err)
		return
	}
	rs.run = run
	d.Status = domain.StatusRunning
	e.log(d, run.ID, domain.LevelInfo, "inicio", fmt.Sprintf("Demanda iniciada — revisão %d, tentativa %d, provedor %s", rev.Number, d.Attempts, prov.Name()),
		"Regras utilizadas: versão "+composed.Hash)

	fail := func(stage domain.Stage, err error) { e.block(&d, run, stage, err) }
	agentTimeout := time.Duration(max(settings.AgentTimeoutMin, 5)) * time.Minute

	// --- documentos
	atts, err := e.prepareAttachments(rs)
	if err != nil {
		fail(domain.StageAnalysis, err)
		return
	}

	pc := promptCtx{D: d, P: p, Rev: rev, Rules: composed.Text, Attachments: atts}
	if rev.Number > 1 {
		revs, _ := e.st.ListRevisions(d.ID)
		for _, r := range revs {
			if r.Number < rev.Number {
				pc.Previous = append(pc.Previous, r)
			}
		}
		pc.PrevSummary = e.lastSummary(d.ID)
		if wtExists && d.BaseBranch != "" {
			pc.DiffStat = gitx.Repo{Dir: wt}.DiffStat(d.BaseBranch)
		}
	}
	attDirs := []string{filepath.Join(e.dataDir, "anexos", fmt.Sprint(d.ID))}

	// --- ANÁLISE + PLANEJAMENTO
	if strings.TrimSpace(rev.Spec) == "" {
		e.setStage(rs, domain.StageAnalysis, domain.StatusRunning, "IA interpretando requisitos, anexos e o projeto")
		workdir := p.Path
		if wtExists {
			workdir = wt
		}
		actx, cancel := context.WithTimeout(ctx, agentTimeout)
		res, err := prov.Run(actx, provider.Request{Phase: provider.PhaseAnalysis, WorkDir: workdir, SystemPrompt: security.SecurityPreamble,
			Prompt: analysisPrompt(pc), ExtraReadDirs: attDirs, Model: d.Model}, e.agentEmitter(rs))
		cancel()
		run.CostUSD += res.CostUSD
		if err != nil {
			fail(domain.StageAnalysis, fmt.Errorf("análise falhou: %w", err))
			return
		}
		spec := extractSpec(res.Text)
		if strings.TrimSpace(spec) == "" {
			fail(domain.StageAnalysis, errors.New("o agente não devolveu uma especificação"))
			return
		}
		approved := !d.ReviewSpec
		e.st.SetRevisionSpec(rev.ID, spec, approved)
		rev.Spec, rev.SpecApproved = spec, approved
		e.log(d, run.ID, domain.LevelOK, "especificacao", "Especificação normalizada e plano de execução elaborados", spec)
		if q := countOpenQuestions(spec); q > 0 {
			e.log(d, run.ID, domain.LevelWarn, "duvidas", fmt.Sprintf("A especificação registrou %d ponto(s) que precisam de esclarecimento", q), "")
		}
		e.setStage(rs, domain.StagePlanning, domain.StatusRunning, "Plano de implementação elaborado")
	} else {
		e.log(d, run.ID, domain.LevelInfo, "especificacao", "Reaproveitando especificação já gerada para esta revisão", "")
	}

	if d.ReviewSpec && !rev.SpecApproved {
		t := time.Now().UTC()
		run.Status, run.Summary, run.FinishedAt = domain.StatusDone, "Especificação gerada; aguardando aprovação humana.", &t
		e.st.UpdateRun(*run)
		e.setStage(rs, domain.StagePlanning, domain.StatusAwaitingApproval, "Especificação pronta — revise e aprove para continuar")
		e.log(d, run.ID, domain.LevelAuth, "aprovacao", "Aprovação necessária: revise a especificação antes do desenvolvimento", "")
		return
	}

	// --- PREPARAÇÃO (Git)
	e.setStage(rs, domain.StagePrep, domain.StatusRunning, "Validando repositório e preparando branch isolada")
	if err := e.prepareGit(rs, p, wt); err != nil {
		fail(domain.StagePrep, err)
		return
	}
	repo := gitx.Repo{Dir: wt}
	pc.D = d
	if rev.Number > 1 && pc.DiffStat == "" {
		pc.DiffStat = repo.DiffStat(d.BaseBranch)
	}
	if ctx.Err() != nil {
		fail(domain.StagePrep, ctx.Err())
		return
	}

	// --- DESENVOLVIMENTO
	bashAllow, rejected := security.BashAllowFromPrefixes(p.AgentBashAllow)
	for _, a := range bashAllow {
		pc.AllowedCmds = append(pc.AllowedCmds, strings.TrimSuffix(strings.TrimPrefix(a, "Bash("), ":*)"))
	}
	for _, r := range rejected {
		e.log(d, run.ID, domain.LevelWarn, "seguranca", "Comando liberado ao agente foi rejeitado pela política: "+r, "")
	}
	var devSummary string
	skipDev := resumeFrom == domain.StageValidation && rev.Number == d.CurrentRevision && e.hasDevRun(d.ID, rev.Number)
	if skipDev {
		e.log(d, run.ID, domain.LevelInfo, "retomada", "Retomando pela validação: a implementação desta revisão já existe na worktree", "")
		devSummary = e.lastSummary(d.ID)
	} else {
		e.setStage(rs, domain.StageDev, domain.StatusRunning, "IA implementando as alterações")
		dctx, cancel := context.WithTimeout(ctx, agentTimeout)
		res, err := prov.Run(dctx, provider.Request{Phase: provider.PhaseDevelop, WorkDir: wt, SystemPrompt: security.SecurityPreamble,
			Prompt: developPrompt(pc, rev.Spec), ExtraReadDirs: attDirs, BashAllow: bashAllow, Model: d.Model}, e.agentEmitter(rs))
		cancel()
		run.CostUSD += res.CostUSD
		run.SessionID = res.SessionID
		e.st.UpdateRun(*run)
		if err != nil {
			fail(domain.StageDev, fmt.Errorf("desenvolvimento falhou: %w", err))
			return
		}
		devSummary = res.Text
		changed := repo.ChangedFiles()
		if len(changed) == 0 && rev.Number == 1 {
			e.log(d, run.ID, domain.LevelWarn, "sem_alteracoes", "O agente concluiu sem alterar nenhum arquivo", devSummary)
		} else {
			e.log(d, run.ID, domain.LevelOK, "implementacao", fmt.Sprintf("Implementação concluída — %d arquivo(s) com alterações pendentes de commit", len(changed)), devSummary)
		}
	}
	run.Summary = devSummary
	e.st.UpdateRun(*run)

	// --- VALIDAÇÃO (+ correções)
	e.setStage(rs, domain.StageValidation, domain.StatusRunning, "Executando build e testes")
	cmdTimeout := time.Duration(max(settings.CommandTimeoutMin, 1)) * time.Minute
	ok, output, executed := e.validate(ctx, rs, p, wt, cmdTimeout)
	for attempt := 1; executed && !ok && attempt <= settings.MaxFixAttempts && ctx.Err() == nil; attempt++ {
		e.log(d, run.ID, domain.LevelError, "erro_validacao", fmt.Sprintf("Validação falhou — solicitando correção à IA (tentativa %d de %d)", attempt, settings.MaxFixAttempts), output)
		d.StatusMessage = fmt.Sprintf("Corrigindo falhas de validação (tentativa %d)", attempt)
		e.touch(&d, d.StatusMessage)
		e.save(&d)
		fctx, cancel := context.WithTimeout(ctx, agentTimeout)
		res, err := prov.Run(fctx, provider.Request{Phase: provider.PhaseFix, WorkDir: wt, SystemPrompt: security.SecurityPreamble,
			Prompt: fixPrompt(pc, rev.Spec, output, attempt), ExtraReadDirs: attDirs, BashAllow: bashAllow, Model: d.Model,
			ResumeSession: run.SessionID}, e.agentEmitter(rs))
		cancel()
		run.CostUSD += res.CostUSD
		if res.SessionID != "" {
			run.SessionID = res.SessionID
		}
		if err != nil {
			e.log(d, run.ID, domain.LevelError, "correcao", "A tentativa de correção falhou", err.Error())
			break
		}
		run.Summary += "\n\n---\n### Correção (tentativa " + fmt.Sprint(attempt) + ")\n" + res.Text
		e.log(d, run.ID, domain.LevelOK, "correcao", "Correção aplicada pela IA — revalidando", res.Text)
		ok, output, executed = e.validate(ctx, rs, p, wt, cmdTimeout)
	}
	if ctx.Err() != nil {
		fail(domain.StageValidation, ctx.Err())
		return
	}
	switch {
	case !executed:
		d.ValidationState = "nao_executada"
		e.log(d, run.ID, domain.LevelWarn, "validacao", "Validação NÃO executada: nenhum comando de build/teste configurado para o projeto", "Configure em Projetos > comandos de build e teste.")
	case ok:
		d.ValidationState = "ok"
		e.log(d, run.ID, domain.LevelOK, "validacao", "Validação concluída com sucesso", "")
	default:
		d.ValidationState = "falhou"
		e.commitIfConfigured(rs, p, repo, rev)
		run.DiffStat = repo.DiffStat(d.BaseBranch)
		e.st.UpdateRun(*run)
		fail(domain.StageValidation, errors.New("a validação continua falhando após as tentativas de correção; o trabalho foi preservado na branch "+d.WorkBranch+" para análise humana"))
		return
	}

	// --- REVISÃO
	e.commitIfConfigured(rs, p, repo, rev)
	run.DiffStat = repo.DiffStat(d.BaseBranch)
	t := time.Now().UTC()
	run.Status, run.FinishedAt = domain.StatusDone, &t
	e.st.UpdateRun(*run)
	e.setStage(rs, domain.StageReview, domain.StatusAwaitingApproval, "Alterações prontas para inspeção humana")
	e.log(d, run.ID, domain.LevelAuth, "revisao", "Revisão disponibilizada: inspecione o diff e aprove ou devolva para ajustes",
		"Branch: "+d.WorkBranch+"\nWorktree: "+wt+"\n\n"+run.DiffStat)
}

func (e *Engine) commitIfConfigured(rs *runState, p domain.Project, repo gitx.Repo, rev domain.Revision) {
	d := rs.d
	if !p.AutoCommit {
		e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Commit automático desativado: alterações mantidas sem commit na worktree", "")
		return
	}
	msg := fmt.Sprintf("%s: %s (revisão %d)\n\nGerado pela esteira DevPipe com %s. Revisão humana pendente.", d.Ticket, d.Title, rev.Number, rs.run.Provider)
	sha, err := repo.CommitAll(msg)
	if err != nil {
		e.log(*d, rs.run.ID, domain.LevelError, "git", "Falha ao criar commit local", err.Error())
		return
	}
	if sha != "" {
		rs.run.CommitSHA = sha
		e.log(*d, rs.run.ID, domain.LevelOK, "git", "Commit local criado na branch "+d.WorkBranch+" ("+sha[:min(10, len(sha))]+") — nenhum push realizado", "")
	}
}

func (e *Engine) prepareGit(rs *runState, p domain.Project, wt string) error {
	d := rs.d
	if err := gitx.Available(); err != nil {
		return err
	}
	main := gitx.Repo{Dir: p.Path}
	if _, err := main.Validate(); err != nil {
		return err
	}
	e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Repositório validado: "+p.Path, "")
	if d.BaseBranch == "" {
		cur, err := main.CurrentBranch()
		if err != nil {
			return err
		}
		d.BaseBranch = cur
		e.st.UpdateDemandFields(*d)
		e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Branch base definida pela branch atualmente selecionada: "+cur, "")
	}
	if !main.RefExists(d.BaseBranch) {
		return fmt.Errorf("branch base %q não existe no repositório", d.BaseBranch)
	}
	if dirty, err := main.StatusPorcelain(); err == nil && len(dirty) > 0 {
		e.log(*d, rs.run.ID, domain.LevelWarn, "git", fmt.Sprintf("O checkout principal possui %d alteração(ões) não commitada(s). Elas NÃO serão tocadas nem incluídas: a demanda usa uma worktree isolada.", len(dirty)), strings.Join(dirty, "\n"))
	}
	if d.WorkBranch == "" {
		name := gitx.SafeBranchName(p.BranchPrefix, d.Ticket, d.Title)
		if main.LocalBranchExists(name) {
			name = fmt.Sprintf("%s-%d", name, d.ID) // nunca reutiliza uma branch que não foi criada por esta demanda
		}
		d.WorkBranch = name
	}
	created, err := main.EnsureWorktree(wt, d.WorkBranch, d.BaseBranch)
	if err != nil {
		return err
	}
	d.WorktreePath = wt
	e.save(d)
	if created {
		e.log(*d, rs.run.ID, domain.LevelOK, "git", fmt.Sprintf("Branch de trabalho %s criada a partir de %s em worktree isolada", d.WorkBranch, d.BaseBranch), wt)
	} else {
		e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Reutilizando a worktree existente da demanda (branch "+d.WorkBranch+")", wt)
		if ch := (gitx.Repo{Dir: wt}).ChangedFiles(); len(ch) > 0 {
			e.log(*d, rs.run.ID, domain.LevelInfo, "git", fmt.Sprintf("A worktree contém %d alteração(ões) não commitada(s) de execução anterior; serão preservadas", len(ch)), strings.Join(ch, "\n"))
		}
	}
	return nil
}

// validate executa os comandos oficiais de build e teste do projeto.
func (e *Engine) validate(ctx context.Context, rs *runState, p domain.Project, wt string, timeout time.Duration) (ok bool, output string, executed bool) {
	type step struct{ name, cmd string }
	var steps []step
	if strings.TrimSpace(p.BuildCommand) != "" {
		steps = append(steps, step{"Build", p.BuildCommand})
	}
	if strings.TrimSpace(p.TestCommand) != "" {
		steps = append(steps, step{"Testes", p.TestCommand})
	}
	if len(steps) == 0 {
		return true, "", false
	}
	d := rs.d
	var all strings.Builder
	for _, s := range steps {
		if err := security.CheckValidationCommand(s.cmd); err != nil {
			e.log(*d, rs.run.ID, domain.LevelError, "seguranca", err.Error(), "")
			return false, err.Error(), true
		}
		e.log(*d, rs.run.ID, domain.LevelInfo, "comando", s.name+" iniciado: "+s.cmd, "classe: "+string(security.Classify(s.cmd)))
		start := time.Now()
		out, err := runShell(ctx, wt, s.cmd, timeout)
		dur := time.Since(start).Round(time.Second)
		fmt.Fprintf(&all, "$ %s\n%s\n", s.cmd, tail(out, 12000))
		if err != nil {
			e.log(*d, rs.run.ID, domain.LevelError, "comando", fmt.Sprintf("%s falhou em %s: %v", s.name, dur, err), tail(out, 12000))
			return false, tail(all.String(), 16000), true
		}
		e.log(*d, rs.run.ID, domain.LevelOK, "comando", fmt.Sprintf("%s concluído em %s", s.name, dur), tail(out, 6000))
	}
	return true, all.String(), true
}

func runShell(ctx context.Context, dir, script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sh, args := procx.ShellCommand(script)
	cmd := exec.CommandContext(ctx, sh, args...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	procx.Prepare(cmd)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("tempo limite de %s excedido", timeout)
	}
	return buf.String(), err
}

func (e *Engine) agentEmitter(rs *runState) func(provider.Event) {
	return func(ev provider.Event) {
		level := domain.LevelAgent
		switch {
		case ev.Kind == "permissao_negada":
			level = domain.LevelAuth
		case ev.IsError:
			level = domain.LevelWarn
		}
		e.log(*rs.d, rs.run.ID, level, ev.Kind, ev.Message, ev.Detail)
		if ev.Kind == "arquivo_alterado" || ev.Kind == "comando" || ev.Kind == "arquivo_lido" {
			e.touch(rs.d, ev.Message)
			rs.d.ElapsedSeconds = rs.baseSecs + int64(time.Since(rs.started).Seconds())
			e.save(rs.d)
		}
	}
}

func (e *Engine) prepareAttachments(rs *runState) ([]attachmentCtx, error) {
	d := rs.d
	list, err := e.st.ListAttachments(d.ID)
	if err != nil {
		return nil, err
	}
	revNum := map[int64]int{}
	revs, _ := e.st.ListRevisions(d.ID)
	for _, r := range revs {
		revNum[r.ID] = r.Number
	}
	var out []attachmentCtx
	processed := 0
	for _, a := range list {
		if a.ExtractNote == "" {
			text, note := docs.Extract(a.StoredPath)
			path := ""
			if text != "" {
				path = a.StoredPath + ".extraido.txt"
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					path = ""
				}
			}
			a.ExtractedPath, a.ExtractNote = path, note
			e.st.UpdateAttachmentExtraction(a.ID, path, note)
			processed++
		}
		ac := attachmentCtx{Name: a.FileName, Kind: a.Kind, AbsPath: a.StoredPath, Note: a.ExtractNote, Revision: revNum[a.RevisionID]}
		if a.ExtractedPath != "" {
			if b, err := os.ReadFile(a.ExtractedPath); err == nil {
				ac.Text = string(b)
			}
		}
		out = append(out, ac)
	}
	if processed > 0 {
		e.log(*d, rs.run.ID, domain.LevelOK, "documentos", fmt.Sprintf("%d documento(s) processado(s); %d anexo(s) no total", processed, len(list)), "")
	}
	return out, nil
}

func (e *Engine) lastSummary(demandID int64) string {
	runs, _ := e.st.ListRuns(demandID)
	for _, r := range runs {
		if r.Status == domain.StatusDone && strings.TrimSpace(r.Summary) != "" && !strings.HasPrefix(r.Summary, "Especificação gerada") {
			return truncate(r.Summary, 12000)
		}
	}
	return ""
}

func (e *Engine) hasDevRun(demandID int64, rev int) bool {
	runs, _ := e.st.ListRuns(demandID)
	for _, r := range runs {
		if r.RevisionNum == rev && r.SessionID != "" && strings.TrimSpace(r.Summary) != "" {
			return true
		}
	}
	return false
}

// extractSpec remove preâmbulos e mantém o documento a partir do título.
func extractSpec(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "# Especifica"); i > 0 {
		s = s[i:]
	}
	s = strings.TrimPrefix(s, "```markdown")
	s = strings.TrimPrefix(s, "```md")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

func countOpenQuestions(spec string) int {
	i := strings.Index(spec, "## Pontos que precisam de esclarecimento")
	if i < 0 {
		return 0
	}
	sec := spec[i+len("## Pontos que precisam de esclarecimento"):]
	if j := strings.Index(sec, "\n## "); j >= 0 {
		sec = sec[:j]
	}
	n := 0
	for _, l := range strings.Split(sec, "\n") {
		l = strings.TrimSpace(l)
		if (strings.HasPrefix(l, "-") || strings.HasPrefix(l, "*") || (len(l) > 2 && l[0] >= '0' && l[0] <= '9')) &&
			!strings.Contains(strings.ToLower(l), "nenhum") && !strings.Contains(strings.ToLower(l), "não se aplica") {
			n++
		}
	}
	return n
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && (s[i]&0xC0) == 0x80 {
		i++
	}
	return "…" + s[i:]
}
