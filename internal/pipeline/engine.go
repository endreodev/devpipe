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
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"devpipe/internal/docs"
	"devpipe/internal/domain"
	"devpipe/internal/encx"
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

	usage       *provider.PlanUsage // último uso da assinatura informado pela CLI
	pausedUntil time.Time           // fila pausada até a liberação do limite do plano
}

// Usage devolve o último consumo conhecido da assinatura e a pausa da fila.
func (e *Engine) Usage() (*provider.PlanUsage, time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.usage, e.pausedUntil
}

func (e *Engine) SetUsage(u *provider.PlanUsage) {
	if u == nil {
		return
	}
	e.mu.Lock()
	e.usage = u
	e.mu.Unlock()
	e.bus.Publish(Message{Type: "usage", Data: u})
}

// ResumeQueue remove a pausa por limite do plano (ação manual).
func (e *Engine) ResumeQueue() {
	e.mu.Lock()
	e.pausedUntil = time.Time{}
	e.mu.Unlock()
	e.Wake()
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

// worktreePath monta o caminho da área de trabalho de uma demanda. A raiz vem
// das configurações (Configurações > Áreas de trabalho); vazia, cai na pasta
// de dados. Demandas que já começaram guardam o caminho em Demand.WorktreePath
// e continuam onde estão — mudar a raiz só vale para demandas novas.
func (e *Engine) worktreePath(st domain.Settings, p domain.Project, d domain.Demand) string {
	if d.WorktreePath != "" {
		return d.WorktreePath
	}
	root := strings.TrimSpace(st.WorktreeRoot)
	if root == "" {
		root = filepath.Join(e.dataDir, "worktrees")
	}
	return filepath.Join(root, fmt.Sprintf("p%d", p.ID), fmt.Sprintf("%d-%s", d.ID, sanitize(d.Ticket)))
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
	e.mu.Lock()
	paused := time.Now().Before(e.pausedUntil)
	e.mu.Unlock()
	if paused {
		return // aguardando liberação do limite da assinatura
	}
	st, _ := e.st.GetSettings()
	max := st.MaxParallel
	if max < 1 {
		max = 1
	}
	queued, err := e.st.ListDemands(store.DemandFilter{Status: domain.StatusQueued})
	if err != nil {
		return
	}
	for _, d := range queued {
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
					// a pilha vai para o devpipe.log: sem ela, um pânico dentro
					// do SQLite/wasm chega ao usuário como mensagem solta e não
					// há como saber qual consulta o provocou.
					log.Printf("pânico na demanda %d: %v\n%s", d.ID, r, debug.Stack())
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
	d         *domain.Demand
	run       *domain.Run
	started   time.Time
	baseSecs  int64
	encIssues []encx.Issue // problemas de codificação pendentes (viram falha de validação)
	encSeq    int
	lastSave  time.Time // última gravação do estado da demanda (ver activityInterval)
}

// activityInterval limita a gravação do campo "última atividade" enquanto o
// agente trabalha. Cada ferramenta que o agente usa gera um evento; sem o
// limite, uma demanda que lê 300 arquivos dispara 300 UPDATEs de 28 colunas na
// conexão única do SQLite (SetMaxOpenConns(1)), concorrendo com as consultas da
// interface. O evento em si continua indo para o banco e para o SSE a cada
// ocorrência — o que é adiado é apenas o resumo exibido no cartão.
const activityInterval = time.Second

func (e *Engine) setStage(rs *runState, s domain.Stage, status, msg string) {
	d := rs.d
	d.Stage, d.Status, d.StatusMessage = s, status, msg
	d.Progress = progressFor(s, d.Progress)
	d.ElapsedSeconds = rs.baseSecs + int64(time.Since(rs.started).Seconds())
	e.touch(d, msg)
	rs.lastSave = time.Now()
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

	wt := e.worktreePath(settings, p, d)
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

	fail := func(stage domain.Stage, err error) {
		var lim *provider.ErrPlanLimit
		if errors.As(err, &lim) {
			e.waitPlanLimit(&d, run, stage, lim)
			return
		}
		e.block(&d, run, stage, err)
	}
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
			pc.DiffStat = gitx.Repo{Dir: wt}.DiffStat(d.DiffBase())
		}
	}
	attDirs := []string{filepath.Join(e.dataDir, "anexos", fmt.Sprint(d.ID))}

	// --- ANÁLISE + PLANEJAMENTO
	analysisSession := ""
	pendingEnc := "" // manifesto da análise mantido para o desenvolvimento (evita reconverter)
	defer func() {
		if pendingEnc != "" { // saída antecipada: nunca deixar arquivos convertidos
			e.encRestore(rs, p, wt, nil, pendingEnc)
		}
	}()
	if strings.TrimSpace(rev.Spec) == "" {
		e.setStage(rs, domain.StageAnalysis, domain.StatusRunning, "IA interpretando requisitos, anexos e o projeto")
		// a análise já roda na branch isolada: vê exatamente o código que será
		// alterado (sem as alterações locais do usuário) e permite que o
		// desenvolvimento continue a mesma sessão, sem reler o projeto.
		if err := e.prepareGit(rs, p, wt); err != nil {
			fail(domain.StagePrep, err)
			return
		}
		pc.D = d
		res, kept, err := e.runAgent(ctx, rs, agentRun{
			prov: prov, proj: p, workDir: wt, phase: provider.PhaseAnalysis,
			model: d.Model, timeout: agentTimeout, readDirs: attDirs,
			prompt: func(bool) string { return analysisPrompt(pc) },
			// Sem aprovação humana pendente, o desenvolvimento continua a mesma
			// sessão e reaproveita os arquivos já convertidos.
			keep: func(_ provider.Result, err error) bool { return err == nil && !d.ReviewSpec },
		})
		pendingEnc = kept
		if kept == "" {
			rs.encIssues = nil // a análise é somente leitura: não gera problema de codificação
		}
		analysisSession = res.SessionID
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
	if analysisSession == "" || !dirExists(wt) { // na análise a branch já foi preparada
		if err := e.prepareGit(rs, p, wt); err != nil {
			fail(domain.StagePrep, err)
			return
		}
	}
	repo := gitx.Repo{Dir: wt}
	pc.D = d
	if rev.Number > 1 && pc.DiffStat == "" {
		pc.DiffStat = repo.DiffStat(d.DiffBase())
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
		if pendingEnc != "" {
			e.encRestore(rs, p, wt, nil, pendingEnc)
			pendingEnc = ""
		}
		e.encRestore(rs, p, wt, nil, "") // repara branches afetadas por versões anteriores
	} else {
		e.setStage(rs, domain.StageDev, domain.StatusRunning, "IA implementando as alterações")
		if analysisSession != "" {
			e.log(d, run.ID, domain.LevelInfo, "sessao", "Desenvolvimento continua a sessão da análise (o agente já conhece os arquivos analisados)", "")
		}
		res, _, err := e.runAgent(ctx, rs, agentRun{
			prov: prov, proj: p, workDir: wt, phase: provider.PhaseDevelop,
			model: d.Model, timeout: agentTimeout, readDirs: attDirs, bash: bashAllow,
			session: analysisSession, inherit: pendingEnc,
			// Sessão continuada não reenvia regras nem especificação: já estão
			// no histórico. Sessão nova precisa do contexto completo.
			prompt:  func(resumado bool) string { pc.Resumed = resumado; return developPrompt(pc, rev.Spec) },
			encNote: func(n string) { pc.EncodingNote = n },
		})
		pendingEnc = "" // consumido por inherit e restaurado pelo runAgent
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
		res, _, err := e.runAgent(ctx, rs, agentRun{
			prov: prov, proj: p, workDir: wt, phase: provider.PhaseFix,
			model: d.Model, timeout: agentTimeout, readDirs: attDirs, bash: bashAllow,
			session: run.SessionID, // continua a sessão do desenvolvimento
			prompt: func(resumado bool) string {
				pc.Resumed = resumado
				return fixPrompt(pc, rev.Spec, output, attempt)
			},
			encNote: func(n string) { pc.EncodingNote = n },
		})
		if res.SessionID != "" {
			run.SessionID = res.SessionID
		}
		if err != nil {
			var lim *provider.ErrPlanLimit
			var pol *provider.ErrPolicy
			if errors.As(err, &lim) || errors.As(err, &pol) {
				fail(domain.StageValidation, err)
				return
			}
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
		run.DiffStat = repo.DiffStat(d.DiffBase())
		e.st.UpdateRun(*run)
		fail(domain.StageValidation, errors.New("a validação continua falhando após as tentativas de correção; o trabalho foi preservado em "+wt+" (branch "+d.WorkBranch+") para análise humana"))
		return
	}

	// --- REVISÃO
	e.commitIfConfigured(rs, p, repo, rev)
	run.DiffStat = repo.DiffStat(d.DiffBase())
	t := time.Now().UTC()
	run.Status, run.FinishedAt = domain.StatusDone, &t
	e.st.UpdateRun(*run)
	e.setStage(rs, domain.StageReview, domain.StatusAwaitingApproval, "Alterações prontas para inspeção humana")
	e.log(d, run.ID, domain.LevelAuth, "revisao", "Revisão disponibilizada: inspecione o diff e aprove ou devolva para ajustes",
		"Branch: "+d.WorkBranch+"\nWorktree: "+wt+"\n\n"+run.DiffStat)
}

// agentRun descreve uma chamada ao agente dentro de uma execução. Existe para
// reunir num lugar só tudo que toda chamada precisa fazer e que é fácil
// esquecer ao acrescentar um caminho novo em process: proteger a codificação
// dos arquivos antes e desfazê-la depois (inclusive quando o agente falha),
// aplicar o tempo limite, somar o custo na execução e, quando a sessão anterior
// não existe mais no provedor, repetir uma vez em sessão nova.
type agentRun struct {
	prov     provider.Provider
	proj     domain.Project
	workDir  string
	phase    provider.Phase
	model    string
	timeout  time.Duration
	session  string   // sessão do agente a continuar ("" = sessão nova)
	bash     []string // prefixos de comando liberados ao agente
	readDirs []string // pastas extras de leitura (anexos)

	// prompt recebe se a chamada está de fato continuando uma sessão: ao cair
	// para uma sessão nova, o prompt precisa voltar a carregar o contexto
	// completo (regras, solicitação, especificação).
	prompt func(resumado bool) string
	// encNote, quando informado, recebe o aviso de codificação a incluir no
	// prompt. É chamado depois da preparação e antes de montar o prompt.
	encNote func(string)
	// inherit: manifesto de uma chamada anterior cujos arquivos continuam
	// convertidos para UTF-8 — a análise entrega os arquivos já convertidos ao
	// desenvolvimento, que continua a mesma sessão sem reconverter nada.
	inherit string
	// keep é avaliado depois da chamada (só faz sentido no modo sob demanda):
	// se devolver true, os arquivos ficam convertidos e o manifesto volta para
	// o chamador, que passa a responder pela restauração.
	keep func(provider.Result, error) bool
}

// runAgent executa a chamada descrita por a. Devolve o resultado e, quando
// keep autoriza, o manifesto de codificação que ficou pendente de restauração.
func (e *Engine) runAgent(ctx context.Context, rs *runState, a agentRun) (res provider.Result, pendente string, err error) {
	man, path := e.encPrepare(rs, a.workDir, a.prov)
	if a.inherit != "" && path != "" {
		man, path = nil, a.inherit // a chamada anterior já converteu estes arquivos
	}
	if a.encNote != nil {
		a.encNote(encodingNote(man, path != ""))
	}
	defer func() {
		if path != "" && a.keep != nil && a.keep(res, err) {
			pendente = path
			return
		}
		e.encRestore(rs, a.proj, a.workDir, man, path)
	}()

	req := provider.Request{Phase: a.phase, WorkDir: a.workDir, SystemPrompt: security.SecurityPreamble,
		ExtraReadDirs: a.readDirs, BashAllow: a.bash, Model: a.model,
		ResumeSession: a.session, EncodingManifest: path}
	res, err = e.callAgent(ctx, rs, a, req, a.session != "")
	if err == nil || a.session == "" || res.SessionID != "" || ctx.Err() != nil {
		return res, "", err
	}
	// Falhou sem sequer abrir uma sessão: a sessão anterior não existe mais no
	// provedor. Repetir do zero resolve — exceto quando a falha foi de política
	// de cobrança ou de limite do plano, em que repetir só agrava.
	var lim *provider.ErrPlanLimit
	var pol *provider.ErrPolicy
	if errors.As(err, &lim) || errors.As(err, &pol) {
		return res, "", err
	}
	e.log(*rs.d, rs.run.ID, domain.LevelWarn, "sessao", "Não foi possível continuar a sessão anterior; iniciando uma nova", err.Error())
	req.ResumeSession = ""
	res, err = e.callAgent(ctx, rs, a, req, false)
	return res, "", err
}

// callAgent monta o prompt, aplica o tempo limite e soma o custo na execução.
// O custo é somado em TODA tentativa: uma sessão que falhou antes de começar
// também pode ter consumido tokens.
func (e *Engine) callAgent(ctx context.Context, rs *runState, a agentRun, req provider.Request, resumado bool) (provider.Result, error) {
	req.Prompt = a.prompt(resumado)
	cctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	res, err := a.prov.Run(cctx, req, e.agentEmitter(rs))
	rs.run.CostUSD += res.CostUSD
	return res, err
}

// waitPlanLimit: o limite da assinatura acabou. A demanda volta para a fila na
// mesma etapa (o trabalho já feito na worktree é preservado) e a fila inteira
// pausa até a liberação; nada é executado com cobrança extra.
func (e *Engine) waitPlanLimit(d *domain.Demand, run *domain.Run, stage domain.Stage, lim *provider.ErrPlanLimit) {
	until := lim.ResetsAt
	if until.IsZero() || until.Before(time.Now()) {
		until = time.Now().Add(30 * time.Minute)
	}
	e.mu.Lock()
	e.pausedUntil = until.Add(1 * time.Minute)
	e.mu.Unlock()
	if stage == domain.StageBlocked {
		stage = d.BlockedFrom
	}
	d.Stage, d.Status = stage, domain.StatusQueued
	d.StatusMessage = "Limite da assinatura atingido. A fila foi pausada e esta demanda continua automaticamente em " + until.Local().Format("02/01 15:04") + "."
	e.touch(d, d.StatusMessage)
	e.save(d)
	if run != nil {
		t := time.Now().UTC()
		run.Status, run.Error, run.FinishedAt = domain.StatusCancelled, lim.Error(), &t
		e.st.UpdateRun(*run)
	}
	e.log(*d, runID(run), domain.LevelWarn, "limite_plano", d.StatusMessage, "")
}

// encPrepare protege a codificação durante a edição do agente.
// Com provedores que suportam hooks (Claude), a conversão é sob demanda: só os
// arquivos que o agente abrir são convertidos (milissegundos). Para os demais,
// converte os arquivos Windows-1252 da worktree antes da execução.
func (e *Engine) encPrepare(rs *runState, wt string, prov provider.Provider) (*encx.Manifest, string) {
	if hc, ok := prov.(provider.HookCapable); ok && hc.SupportsEncodingHook() {
		dir := filepath.Join(e.dataDir, "codificacao")
		os.MkdirAll(dir, 0o755)
		rs.encSeq++
		path := filepath.Join(dir, fmt.Sprintf("run%d-%d.txt", rs.run.ID, rs.encSeq))
		os.Remove(path)
		return nil, path
	}
	start := time.Now()
	m, err := encx.Prepare(wt)
	if err != nil {
		e.log(*rs.d, rs.run.ID, domain.LevelWarn, "codificacao", "Não foi possível preparar a codificação dos arquivos", err.Error())
	}
	if m != nil && len(m.Converted) > 0 {
		e.log(*rs.d, rs.run.ID, domain.LevelInfo, "codificacao",
			fmt.Sprintf("Codificação protegida: %d arquivo(s) Windows-1252 convertidos temporariamente para UTF-8 (%s)", len(m.Converted), time.Since(start).Round(time.Second)), "")
	}
	return m, ""
}

// encRestore devolve os arquivos à codificação original e repara '�'.
func (e *Engine) encRestore(rs *runState, p domain.Project, wt string, m *encx.Manifest, manifestPath string) {
	start := time.Now()
	if manifestPath != "" {
		m = encx.LoadManifest(manifestPath)
		defer func() {
			os.Remove(manifestPath)
			os.Remove(manifestPath + ".settings.json")
		}()
	}
	repo := gitx.Repo{Dir: wt}
	rep := encx.Restore(wt, repo.MergeBase(rs.d.DiffBase()), m, p.FileEncoding)
	rs.encIssues = rep.Issues
	if m != nil && len(m.Converted) > 0 {
		e.log(*rs.d, rs.run.ID, domain.LevelOK, "codificacao", fmt.Sprintf("Codificação protegida: %d arquivo(s) Windows-1252 usados pela IA foram devolvidos à codificação original (%s)", len(m.Converted), time.Since(start).Round(100*time.Millisecond)), strings.Join(keys(m.Converted), "\n"))
	}
	if len(rep.Restored) > 0 {
		e.log(*rs.d, rs.run.ID, domain.LevelOK, "codificacao", fmt.Sprintf("%d arquivo(s) regravados na codificação original do projeto", len(rep.Restored)), strings.Join(rep.Restored, "\n"))
	}
	if len(rep.Repaired) > 0 {
		e.log(*rs.d, rs.run.ID, domain.LevelOK, "codificacao", fmt.Sprintf("Acentuação recuperada automaticamente em %d arquivo(s)", len(rep.Repaired)), strings.Join(rep.Repaired, "\n"))
	}
	if len(rep.Issues) > 0 {
		var b strings.Builder
		for _, is := range rep.Issues {
			b.WriteString(is.String() + "\n")
		}
		e.log(*rs.d, rs.run.ID, domain.LevelWarn, "codificacao", fmt.Sprintf("%d problema(s) de codificação a corrigir", len(rep.Issues)), b.String())
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func encodingNote(m *encx.Manifest, onDemand bool) string {
	if !onDemand && (m == nil || len(m.Converted) == 0) {
		return ""
	}
	return "Se algum arquivo deste projeto estiver em Windows-1252 (ANSI), a aplicação o converte automaticamente para UTF-8 quando você o abre e o devolve à codificação original ao final. " +
		"Escreva normalmente com acentuação em português. Não converta codificação, não altere finais de linha e não reescreva arquivos inteiros. " +
		"Use somente caracteres que existem em Windows-1252: evite setas (→), emojis e símbolos especiais."
}

// RepairEncoding executa somente o reparo de codificação na worktree da
// demanda (branches afetadas por versões anteriores) e faz commit local.
func (e *Engine) RepairEncoding(id int64) (encx.Report, error) {
	d, err := e.load(id)
	if err != nil {
		return encx.Report{}, err
	}
	if d.WorktreePath == "" || !dirExists(d.WorktreePath) {
		return encx.Report{}, errors.New("a demanda ainda não possui branch de trabalho")
	}
	p, err := e.st.GetProject(d.ProjectID)
	if err != nil {
		return encx.Report{}, err
	}
	repo := gitx.Repo{Dir: d.WorktreePath}
	rep := encx.Restore(d.WorktreePath, repo.MergeBase(d.DiffBase()), nil, p.FileEncoding)
	msg := fmt.Sprintf("Reparo de codificação: %d arquivo(s) regravados, %d com acentuação recuperada, %d problema(s) restantes", len(rep.Restored), len(rep.Repaired), len(rep.Issues))
	var det strings.Builder
	for _, is := range rep.Issues {
		det.WriteString(is.String() + "\n")
	}
	level := domain.LevelOK
	if len(rep.Issues) > 0 {
		level = domain.LevelWarn
	}
	e.log(d, 0, level, "codificacao", msg, det.String())
	if p.AutoCommit && len(repo.ChangedFiles()) > 0 {
		if sha, err := repo.CommitAll(d.Ticket + ": correção de codificação de caracteres"); err == nil && sha != "" {
			e.log(d, 0, domain.LevelOK, "git", "Commit local de reparo criado ("+sha[:min(10, len(sha))]+")", "")
		}
	}
	return rep, nil
}

// commitMessage monta a mensagem do commit da demanda. Leva só o que descreve
// a mudança para quem lê o histórico do repositório: ticket, título e, a partir
// da segunda, o número da revisão. Nada identifica a ferramenta, o provedor de
// IA ou o fluxo interno — o commit fica no repositório da equipe.
func commitMessage(d domain.Demand, rev domain.Revision) string {
	msg := d.Ticket + ": " + d.Title
	if rev.Number > 1 {
		msg = fmt.Sprintf("%s (revisão %d)", msg, rev.Number)
	}
	return msg
}

func (e *Engine) commitIfConfigured(rs *runState, p domain.Project, repo gitx.Repo, rev domain.Revision) {
	d := rs.d
	if !p.AutoCommit {
		e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Commit automático desativado: alterações mantidas sem commit na worktree", "")
		return
	}
	msg := commitMessage(*d, rev)
	sha, err := repo.CommitAll(msg)
	if err != nil {
		e.log(*d, rs.run.ID, domain.LevelError, "git", "Falha ao criar commit local", err.Error())
		return
	}
	if sha != "" {
		rs.run.CommitSHA = sha
		if d.DirectBranch {
			e.log(*d, rs.run.ID, domain.LevelOK, "git", "Commit criado na área de trabalho da demanda ("+sha[:min(10, len(sha))]+"). Ao aprovar, as alterações vão para a branch "+d.WorkBranch+".", "")
		} else {
			e.log(*d, rs.run.ID, domain.LevelOK, "git", "Commit local criado na branch "+d.WorkBranch+" ("+sha[:min(10, len(sha))]+") — nenhum push realizado", "")
		}
	}
}

// isDefaultBaseName diz se a branch veio da preferência padrão (main/master,
// local ou de origin) e não do checkout atual — só muda a mensagem do log.
func isDefaultBaseName(b string) bool {
	b = strings.TrimPrefix(b, "origin/")
	for _, n := range gitx.DefaultBaseNames {
		if b == n {
			return true
		}
	}
	return false
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
		base, err := main.DefaultBase()
		if err != nil {
			return err
		}
		d.BaseBranch = base
		e.st.UpdateDemandFields(*d)
		motivo := "padrão da esteira"
		if !isDefaultBaseName(base) {
			motivo = "o repositório não tem main nem master; usando a branch em checkout"
		}
		e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Branch base definida automaticamente: "+base+" ("+motivo+")", "")
	}
	if !main.RefExists(d.BaseBranch) {
		return fmt.Errorf("branch base %q não existe no repositório", d.BaseBranch)
	}
	if dirty, err := main.StatusPorcelain(); err == nil && len(dirty) > 0 {
		e.log(*d, rs.run.ID, domain.LevelWarn, "git", fmt.Sprintf("O checkout principal possui %d alteração(ões) não commitada(s). Elas NÃO serão tocadas nem incluídas: a demanda usa uma worktree isolada.", len(dirty)), strings.Join(dirty, "\n"))
	}
	if d.DirectBranch {
		return e.prepareDirect(rs, main, wt)
	}
	if d.WorkBranch == "" {
		if !domain.ValidKind(d.Kind) {
			// demandas cadastradas antes da convenção de branches
			return fmt.Errorf("a demanda não tem tipo (%s) definido; edite-a e escolha o tipo antes de executar — ele é o primeiro segmento da branch", strings.Join(domain.Kinds, "/"))
		}
		name := gitx.BranchName(d.Kind, d.Ticket)
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

// prepareDirect: a demanda trabalha na própria branch selecionada, sem criar
// branch nova. O agente edita numa worktree em HEAD destacado (o seu checkout
// não é tocado durante a execução) e cada commit é aplicado na branch por
// fast-forward (ver publish).
func (e *Engine) prepareDirect(rs *runState, main gitx.Repo, wt string) error {
	d := rs.d
	if d.WorkBranch == "" {
		local, criada, err := main.ResolveLocalBranch(d.BaseBranch)
		if err != nil {
			return err
		}
		if criada {
			e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Branch local "+local+" criada acompanhando "+d.BaseBranch, "")
		}
		d.WorkBranch = local
	}
	tip, err := main.BranchTip(d.WorkBranch)
	if err != nil {
		return fmt.Errorf("branch %q não encontrada: %v", d.WorkBranch, err)
	}
	created, err := main.EnsureDetachedWorktree(wt, tip)
	if err != nil {
		return err
	}
	if d.BaseCommit == "" {
		d.BaseCommit = tip
	}
	d.WorktreePath = wt
	e.save(d)
	if created {
		e.log(*d, rs.run.ID, domain.LevelOK, "git", fmt.Sprintf("Trabalhando direto na branch %s (nenhuma branch nova criada). Commit inicial %s.", d.WorkBranch, tip[:min(10, len(tip))]), wt)
		return nil
	}
	repo := gitx.Repo{Dir: wt}
	head := repo.HeadSHA()
	switch {
	case head == tip:
	case len(repo.ChangedFiles()) == 0 && main.IsAncestor(head, tip):
		// a branch avançou (ex.: commits seus ou da revisão anterior): acompanha a ponta
		if err := repo.DetachTo(tip); err != nil {
			return err
		}
		e.log(*d, rs.run.ID, domain.LevelInfo, "git", "Área de trabalho atualizada para a ponta atual da branch "+d.WorkBranch, "")
	default:
		e.log(*d, rs.run.ID, domain.LevelWarn, "git", "A área de trabalho da demanda tem alterações/commits que ainda não estão na branch "+d.WorkBranch+"; serão preservados", strings.Join(repo.ChangedFiles(), "\n"))
	}
	return nil
}

// ApplyToBranch leva o trabalho de uma demanda "direto na branch" para a
// branch selecionada:
//   - se a branch estiver em checkout (ex.: aberta no Visual Studio), aplica as
//     alterações nos arquivos como ALTERAÇÕES PENDENTES (aparecem no Git
//     Changes para você revisar e commitar). Não faz commit, não mexe no índice
//     nem em outros arquivos; se houver alterações locais nos mesmos
//     arquivos, aplica mesmo assim com merge de 3 vias (trechos que conflitam
//     ficam com marcação de conflito no arquivo, para resolver no VS);
//   - se a branch não estiver em checkout, cria o commit e avança a branch por
//     fast-forward.
func (e *Engine) ApplyToBranch(id int64) (string, error) {
	d, err := e.load(id)
	if err != nil {
		return "", err
	}
	if !d.DirectBranch {
		return "", errors.New("esta demanda usa uma branch própria (" + d.WorkBranch + "); faça o merge manualmente")
	}
	if d.WorktreePath == "" || d.BaseCommit == "" {
		return "", errors.New("a demanda ainda não tem alterações preparadas")
	}
	if e.IsRunning(id) {
		return "", errors.New("aguarde a execução terminar")
	}
	p, err := e.st.GetProject(d.ProjectID)
	if err != nil {
		return "", err
	}
	main := gitx.Repo{Dir: p.Path}
	wt := gitx.Repo{Dir: d.WorktreePath}
	dir := main.WorktreeOfBranch(d.WorkBranch)
	if dir == "" {
		// branch fora de checkout: commit + fast-forward da própria branch
		if len(wt.ChangedFiles()) > 0 {
			if _, err := wt.CommitAll(d.Ticket + ": " + d.Title); err != nil {
				return "", err
			}
		}
		onde, err := main.PublishToBranch(d.WorkBranch, wt.HeadSHA())
		if err != nil {
			return "", err
		}
		msg := "Alterações gravadas na branch " + d.WorkBranch + " — " + onde + " (sem push)."
		e.log(d, 0, domain.LevelOK, "git", msg, "")
		return msg, nil
	}
	patch, err := wt.PatchSince(d.BaseCommit)
	if err != nil {
		return "", err
	}
	if len(patch) == 0 {
		return "", errors.New("não há alterações da demanda para aplicar")
	}
	arquivos := wt.PatchFiles(d.BaseCommit)
	pdir := filepath.Join(e.dataDir, "patches")
	os.MkdirAll(pdir, 0o755)
	patchPath := filepath.Join(pdir, fmt.Sprintf("%d-%s.patch", d.ID, sanitize(d.Ticket)))
	if err := os.WriteFile(patchPath, patch, 0o644); err != nil {
		return "", err
	}
	if cur, _ := (gitx.Repo{Dir: dir}).CurrentBranch(); cur != d.WorkBranch {
		return "", fmt.Errorf("o checkout %s não está na branch %s", dir, d.WorkBranch)
	}
	ja, err := (gitx.Repo{Dir: dir}).ApplyPatchToWorkingTree(patchPath)
	if err == nil && ja {
		return "As alterações já estavam aplicadas no checkout " + dir + ".", nil
	}
	if err == nil {
		msg := fmt.Sprintf("%d arquivo(s) alterado(s) no seu checkout %s (branch %s) como alterações pendentes — revise no Git Changes e faça o commit.", len(arquivos), dir, d.WorkBranch)
		e.log(d, 0, domain.LevelOK, "git", msg, strings.Join(arquivos, "\n"))
		return msg, nil
	}
	// O patch não aplica direto porque o checkout tem alterações locais nos
	// mesmos arquivos: aplica mesmo assim, arquivo a arquivo, com merge de 3
	// vias. O que não conflita é juntado; o que conflita fica marcado no arquivo.
	res, merr := (gitx.Repo{Dir: dir}).MergeWorkingTreeFrom(wt, d.BaseCommit, d.Ticket)
	if merr != nil {
		e.log(d, 0, domain.LevelError, "git", "Falha ao aplicar as alterações em "+dir, merr.Error()+"\n\nPatch salvo em: "+patchPath)
		return "", fmt.Errorf("não foi possível aplicar as alterações no checkout %s: %v (patch salvo em %s)", dir, merr, patchPath)
	}
	if len(res.Changed)+len(res.Conflicts)+len(res.Kept) == 0 {
		return "As alterações já estavam aplicadas no checkout " + dir + ".", nil
	}
	msg := fmt.Sprintf("%d arquivo(s) alterado(s) no seu checkout %s (branch %s) como alterações pendentes, junto com as suas alterações locais", len(res.Changed)+len(res.Conflicts), dir, d.WorkBranch)
	if len(res.Conflicts) > 0 {
		msg += fmt.Sprintf(". %d arquivo(s) ficaram com marcação de conflito (<<<<<<< / >>>>>>>) para você resolver no Visual Studio: %s", len(res.Conflicts), strings.Join(res.Conflicts, ", "))
	}
	if len(res.Kept) > 0 {
		msg += ". Não alterados: " + strings.Join(res.Kept, "; ")
	}
	msg += " — revise no Git Changes e faça o commit."
	nivel := domain.LevelOK
	if len(res.Conflicts)+len(res.Kept) > 0 {
		nivel = domain.LevelWarn
	}
	detalhe := "Alterados: " + strings.Join(res.Changed, "\n") + "\nCom conflito: " + strings.Join(res.Conflicts, "\n") + "\nMantidos: " + strings.Join(res.Kept, "\n") + "\n\nPatch salvo em: " + patchPath
	e.log(d, 0, nivel, "git", msg, detalhe)
	return msg, nil
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
	d := rs.d
	if len(rs.encIssues) > 0 {
		var b strings.Builder
		b.WriteString("PROBLEMAS DE CODIFICAÇÃO DE CARACTERES (a acentuação precisa ser corrigida):\n")
		for _, is := range rs.encIssues {
			b.WriteString("- " + is.String() + "\n")
		}
		e.log(*d, rs.run.ID, domain.LevelError, "codificacao", fmt.Sprintf("Validação de codificação falhou: %d problema(s)", len(rs.encIssues)), b.String())
		return false, b.String(), true
	}
	if len(steps) == 0 {
		return true, "", false
	}
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
		if ev.Usage != nil {
			e.SetUsage(ev.Usage)
			if ev.Usage.FiveHourPct < 80 && ev.Usage.SevenDayPct < 80 {
				return // não poluir o log; o painel mostra o consumo
			}
		}
		level := domain.LevelAgent
		switch {
		case ev.Kind == "politica_cobranca":
			level = domain.LevelError
		case ev.Kind == "permissao_negada":
			level = domain.LevelAuth
		case ev.IsError:
			level = domain.LevelWarn
		}
		e.log(*rs.d, rs.run.ID, level, ev.Kind, ev.Message, ev.Detail)
		if ev.Kind == "arquivo_alterado" || ev.Kind == "comando" || ev.Kind == "arquivo_lido" {
			e.touch(rs.d, ev.Message)
			rs.d.ElapsedSeconds = rs.baseSecs + int64(time.Since(rs.started).Seconds())
			// O estado final não se perde: toda saída da etapa passa por
			// setStage ou block, que gravam sem o intervalo.
			if time.Since(rs.lastSave) >= activityInterval {
				rs.lastSave = time.Now()
				e.save(rs.d)
			}
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
