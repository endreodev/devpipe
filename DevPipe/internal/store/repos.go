package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"devpipe/internal/domain"
)

// ---------- Projetos ----------

const projectCols = `id,name,path,language,main_solution,branch_options,standards_files,extra_rules,build_command,test_command,agent_bash_allow,branch_prefix,auto_commit,file_encoding,created_at,updated_at`

func scanProject(r interface{ Scan(...any) error }) (domain.Project, error) {
	var p domain.Project
	var bo, sf, ab string
	var ac int
	err := r.Scan(&p.ID, &p.Name, &p.Path, &p.Language, &p.MainSolution, &bo, &sf, &p.ExtraRules, &p.BuildCommand, &p.TestCommand, &ab, &p.BranchPrefix, &ac, &p.FileEncoding, &p.CreatedAt, &p.UpdatedAt)
	p.BranchOptions, p.StandardsFiles, p.AgentBashAllow = fromJSONList(bo), fromJSONList(sf), fromJSONList(ab)
	p.AutoCommit = ac == 1
	return p, err
}

func (s *Store) ListProjects() ([]domain.Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(id int64) (domain.Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) SaveProject(p *domain.Project) error {
	t := now()
	if p.BranchPrefix == "" {
		p.BranchPrefix = "devpipe/"
	}
	if p.FileEncoding == "" {
		p.FileEncoding = "auto"
	}
	if p.ID == 0 {
		p.CreatedAt = t
		p.UpdatedAt = t
		res, err := s.db.Exec(`INSERT INTO projects(name,path,language,main_solution,branch_options,standards_files,extra_rules,build_command,test_command,agent_bash_allow,branch_prefix,auto_commit,file_encoding,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Name, p.Path, p.Language, p.MainSolution, toJSON(nz(p.BranchOptions)), toJSON(nz(p.StandardsFiles)), p.ExtraRules, p.BuildCommand, p.TestCommand, toJSON(nz(p.AgentBashAllow)), p.BranchPrefix, b2i(p.AutoCommit), p.FileEncoding, t, t)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("já existe um projeto cadastrado com este caminho")
			}
			return err
		}
		p.ID, _ = res.LastInsertId()
		return nil
	}
	p.UpdatedAt = t
	_, err := s.db.Exec(`UPDATE projects SET name=?,path=?,language=?,main_solution=?,branch_options=?,standards_files=?,extra_rules=?,build_command=?,test_command=?,agent_bash_allow=?,branch_prefix=?,auto_commit=?,file_encoding=?,updated_at=? WHERE id=?`,
		p.Name, p.Path, p.Language, p.MainSolution, toJSON(nz(p.BranchOptions)), toJSON(nz(p.StandardsFiles)), p.ExtraRules, p.BuildCommand, p.TestCommand, toJSON(nz(p.AgentBashAllow)), p.BranchPrefix, b2i(p.AutoCommit), p.FileEncoding, t, p.ID)
	return err
}

func (s *Store) DeleteProject(id int64) error {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM demands WHERE project_id=?`, id).Scan(&n)
	if n > 0 {
		return fmt.Errorf("o projeto possui %d demanda(s); remova-as antes", n)
	}
	_, err := s.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

func nz(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ---------- Demandas ----------

const demandCols = `d.id,d.ticket,d.title,d.summary,d.project_id,COALESCE(p.name,''),d.priority,d.queue_order,d.notes,d.base_branch,d.work_branch,d.worktree_path,d.provider,d.model,d.review_spec,d.stage,d.blocked_from,d.status,d.status_message,d.progress,d.attempts,d.validation_state,d.current_revision,d.last_activity,d.last_activity_at,d.elapsed_seconds,d.started_at,d.created_at,d.updated_at`

func scanDemand(r interface{ Scan(...any) error }) (domain.Demand, error) {
	var d domain.Demand
	var rs int
	var la, st sql.NullTime
	err := r.Scan(&d.ID, &d.Ticket, &d.Title, &d.Summary, &d.ProjectID, &d.ProjectName, &d.Priority, &d.QueueOrder, &d.Notes, &d.BaseBranch, &d.WorkBranch, &d.WorktreePath, &d.Provider, &d.Model, &rs, &d.Stage, &d.BlockedFrom, &d.Status, &d.StatusMessage, &d.Progress, &d.Attempts, &d.ValidationState, &d.CurrentRevision, &d.LastActivity, &la, &d.ElapsedSeconds, &st, &d.CreatedAt, &d.UpdatedAt)
	d.ReviewSpec = rs == 1
	if la.Valid {
		t := la.Time
		d.LastActivityAt = &t
	}
	if st.Valid {
		t := st.Time
		d.StartedAt = &t
	}
	return d, err
}

type DemandFilter struct {
	ProjectID int64
	Stage     string
	Query     string
	Branch    string
	From, To  *time.Time
}

func (s *Store) ListDemands(f DemandFilter) ([]domain.Demand, error) {
	q := `SELECT ` + demandCols + ` FROM demands d LEFT JOIN projects p ON p.id=d.project_id WHERE 1=1`
	args := []any{}
	if f.ProjectID > 0 {
		q += ` AND d.project_id=?`
		args = append(args, f.ProjectID)
	}
	if f.Stage != "" {
		q += ` AND d.stage=?`
		args = append(args, f.Stage)
	}
	if f.Branch != "" {
		q += ` AND (d.base_branch=? OR d.work_branch=?)`
		args = append(args, f.Branch, f.Branch)
	}
	if f.Query != "" {
		q += ` AND (d.ticket LIKE ? OR d.title LIKE ? OR d.summary LIKE ?)`
		like := "%" + f.Query + "%"
		args = append(args, like, like, like)
	}
	if f.From != nil {
		q += ` AND d.created_at>=?`
		args = append(args, *f.From)
	}
	if f.To != nil {
		q += ` AND d.created_at<=?`
		args = append(args, *f.To)
	}
	q += ` ORDER BY d.priority ASC, d.queue_order ASC, d.id ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Demand{}
	for rows.Next() {
		d, err := scanDemand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDemand(id int64) (domain.Demand, error) {
	d, err := scanDemand(s.db.QueryRow(`SELECT `+demandCols+` FROM demands d LEFT JOIN projects p ON p.id=d.project_id WHERE d.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// CreateDemand cria a demanda e a revisão original (nº 1) numa transação.
func (s *Store) CreateDemand(d *domain.Demand, instructions string) (domain.Revision, error) {
	t := now()
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Revision{}, err
	}
	defer tx.Rollback()
	if d.QueueOrder == 0 {
		tx.QueryRow(`SELECT COALESCE(MAX(queue_order),0)+10 FROM demands`).Scan(&d.QueueOrder)
	}
	if d.Priority == 0 {
		d.Priority = 3
	}
	d.Stage, d.Status, d.CurrentRevision = domain.StageBacklog, domain.StatusIdle, 1
	d.CreatedAt, d.UpdatedAt = t, t
	res, err := tx.Exec(`INSERT INTO demands(ticket,title,summary,project_id,priority,queue_order,notes,base_branch,provider,model,review_spec,stage,status,current_revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.Ticket, d.Title, d.Summary, d.ProjectID, d.Priority, d.QueueOrder, d.Notes, d.BaseBranch, d.Provider, d.Model, b2i(d.ReviewSpec), d.Stage, d.Status, 1, t, t)
	if err != nil {
		return domain.Revision{}, err
	}
	d.ID, _ = res.LastInsertId()
	rev := domain.Revision{DemandID: d.ID, Number: 1, Kind: "original", Instructions: instructions, CreatedAt: t}
	res, err = tx.Exec(`INSERT INTO revisions(demand_id,number,kind,instructions,created_at) VALUES (?,?,?,?,?)`, d.ID, 1, rev.Kind, instructions, t)
	if err != nil {
		return rev, err
	}
	rev.ID, _ = res.LastInsertId()
	return rev, tx.Commit()
}

// UpdateDemandFields atualiza campos editáveis pelo usuário.
func (s *Store) UpdateDemandFields(d domain.Demand) error {
	_, err := s.db.Exec(`UPDATE demands SET ticket=?,title=?,summary=?,priority=?,queue_order=?,notes=?,base_branch=?,provider=?,model=?,review_spec=?,updated_at=? WHERE id=?`,
		d.Ticket, d.Title, d.Summary, d.Priority, d.QueueOrder, d.Notes, d.BaseBranch, d.Provider, d.Model, b2i(d.ReviewSpec), now(), d.ID)
	return err
}

// UpdateDemandState persiste o estado de execução (usado pelo orquestrador).
func (s *Store) UpdateDemandState(d domain.Demand) error {
	_, err := s.db.Exec(`UPDATE demands SET stage=?,blocked_from=?,status=?,status_message=?,progress=?,attempts=?,validation_state=?,current_revision=?,work_branch=?,worktree_path=?,last_activity=?,last_activity_at=?,elapsed_seconds=?,started_at=?,updated_at=? WHERE id=?`,
		d.Stage, d.BlockedFrom, d.Status, d.StatusMessage, d.Progress, d.Attempts, d.ValidationState, d.CurrentRevision, d.WorkBranch, d.WorktreePath, d.LastActivity, d.LastActivityAt, d.ElapsedSeconds, d.StartedAt, now(), d.ID)
	return err
}

func (s *Store) SetQueueOrder(id int64, order int) error {
	_, err := s.db.Exec(`UPDATE demands SET queue_order=?,updated_at=? WHERE id=?`, order, now(), id)
	return err
}

func (s *Store) DeleteDemand(id int64) error {
	_, err := s.db.Exec(`DELETE FROM demands WHERE id=?`, id)
	return err
}

// ---------- Revisões ----------

func (s *Store) ListRevisions(demandID int64) ([]domain.Revision, error) {
	rows, err := s.db.Query(`SELECT id,demand_id,number,kind,instructions,spec,spec_approved,created_at FROM revisions WHERE demand_id=? ORDER BY number`, demandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Revision{}
	for rows.Next() {
		var r domain.Revision
		var ap int
		if err := rows.Scan(&r.ID, &r.DemandID, &r.Number, &r.Kind, &r.Instructions, &r.Spec, &ap, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.SpecApproved = ap == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRevision(demandID int64, number int) (domain.Revision, error) {
	revs, err := s.ListRevisions(demandID)
	if err != nil {
		return domain.Revision{}, err
	}
	for _, r := range revs {
		if r.Number == number {
			return r, nil
		}
	}
	return domain.Revision{}, ErrNotFound
}

func (s *Store) AddRevision(demandID int64, instructions string) (domain.Revision, error) {
	t := now()
	var n int
	s.db.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM revisions WHERE demand_id=?`, demandID).Scan(&n)
	res, err := s.db.Exec(`INSERT INTO revisions(demand_id,number,kind,instructions,created_at) VALUES (?,?,?,?,?)`, demandID, n, "ajuste", instructions, t)
	if err != nil {
		return domain.Revision{}, err
	}
	id, _ := res.LastInsertId()
	return domain.Revision{ID: id, DemandID: demandID, Number: n, Kind: "ajuste", Instructions: instructions, CreatedAt: t}, nil
}

func (s *Store) SetRevisionSpec(revID int64, spec string, approved bool) error {
	_, err := s.db.Exec(`UPDATE revisions SET spec=?,spec_approved=? WHERE id=?`, spec, b2i(approved), revID)
	return err
}

// ---------- Anexos ----------

func (s *Store) AddAttachment(a *domain.Attachment) error {
	a.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO attachments(demand_id,revision_id,file_name,stored_path,mime,size,kind,extracted_path,extract_note,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		a.DemandID, a.RevisionID, a.FileName, a.StoredPath, a.Mime, a.Size, a.Kind, a.ExtractedPath, a.ExtractNote, a.CreatedAt)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateAttachmentExtraction(id int64, path, note string) error {
	_, err := s.db.Exec(`UPDATE attachments SET extracted_path=?,extract_note=? WHERE id=?`, path, note, id)
	return err
}

func (s *Store) ListAttachments(demandID int64) ([]domain.Attachment, error) {
	rows, err := s.db.Query(`SELECT id,demand_id,revision_id,file_name,stored_path,mime,size,kind,extracted_path,extract_note,created_at FROM attachments WHERE demand_id=? ORDER BY id`, demandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Attachment{}
	for rows.Next() {
		var a domain.Attachment
		if err := rows.Scan(&a.ID, &a.DemandID, &a.RevisionID, &a.FileName, &a.StoredPath, &a.Mime, &a.Size, &a.Kind, &a.ExtractedPath, &a.ExtractNote, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAttachment(id int64) (domain.Attachment, error) {
	var a domain.Attachment
	err := s.db.QueryRow(`SELECT id,demand_id,revision_id,file_name,stored_path,mime,size,kind,extracted_path,extract_note,created_at FROM attachments WHERE id=?`, id).
		Scan(&a.ID, &a.DemandID, &a.RevisionID, &a.FileName, &a.StoredPath, &a.Mime, &a.Size, &a.Kind, &a.ExtractedPath, &a.ExtractNote, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// ---------- Execuções ----------

const runCols = `id,demand_id,revision_num,provider,status,stage,rules_hash,rules_text,session_id,summary,error,cost_usd,diff_stat,commit_sha,started_at,finished_at`

func scanRun(r interface{ Scan(...any) error }) (domain.Run, error) {
	var x domain.Run
	var fin sql.NullTime
	err := r.Scan(&x.ID, &x.DemandID, &x.RevisionNum, &x.Provider, &x.Status, &x.Stage, &x.RulesHash, &x.RulesText, &x.SessionID, &x.Summary, &x.Error, &x.CostUSD, &x.DiffStat, &x.CommitSHA, &x.StartedAt, &fin)
	if fin.Valid {
		t := fin.Time
		x.FinishedAt = &t
	}
	return x, err
}

func (s *Store) CreateRun(r *domain.Run) error {
	r.StartedAt = now()
	res, err := s.db.Exec(`INSERT INTO runs(demand_id,revision_num,provider,status,stage,rules_hash,rules_text,started_at) VALUES (?,?,?,?,?,?,?,?)`,
		r.DemandID, r.RevisionNum, r.Provider, r.Status, r.Stage, r.RulesHash, r.RulesText, r.StartedAt)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateRun(r domain.Run) error {
	_, err := s.db.Exec(`UPDATE runs SET status=?,stage=?,session_id=?,summary=?,error=?,cost_usd=?,diff_stat=?,commit_sha=?,finished_at=? WHERE id=?`,
		r.Status, r.Stage, r.SessionID, r.Summary, r.Error, r.CostUSD, r.DiffStat, r.CommitSHA, r.FinishedAt, r.ID)
	return err
}

func (s *Store) ListRuns(demandID int64) ([]domain.Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE demand_id=? ORDER BY id DESC`, demandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkInterruptedRuns marca execuções que estavam ativas quando o processo parou.
func (s *Store) MarkInterruptedRuns() ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT demand_id FROM runs WHERE status=?`, domain.StatusRunning)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	_, err = s.db.Exec(`UPDATE runs SET status=?, error='Execução interrompida (aplicação encerrada durante a execução)', finished_at=? WHERE status=?`, domain.StatusCancelled, now(), domain.StatusRunning)
	return ids, err
}

// ---------- Eventos ----------

func (s *Store) AddEvent(e *domain.Event) error {
	if e.Time.IsZero() {
		e.Time = now()
	}
	res, err := s.db.Exec(`INSERT INTO events(time,demand_id,ticket,project_id,project,run_id,stage,level,kind,message,detail) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.Time, e.DemandID, e.Ticket, e.ProjectID, e.Project, e.RunID, e.Stage, e.Level, e.Kind, e.Message, e.Detail)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

type EventFilter struct {
	DemandID int64
	RunID    int64
	Level    string
	AfterID  int64
	Limit    int
}

func (s *Store) ListEvents(f EventFilter) ([]domain.Event, error) {
	q := `SELECT id,time,demand_id,ticket,project_id,project,run_id,stage,level,kind,message,detail FROM events WHERE id>?`
	args := []any{f.AfterID}
	if f.DemandID > 0 {
		q += ` AND demand_id=?`
		args = append(args, f.DemandID)
	}
	if f.RunID > 0 {
		q += ` AND run_id=?`
		args = append(args, f.RunID)
	}
	if f.Level != "" {
		q += ` AND level=?`
		args = append(args, f.Level)
	}
	if f.Limit <= 0 || f.Limit > 2000 {
		f.Limit = 500
	}
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var e domain.Event
		if err := rows.Scan(&e.ID, &e.Time, &e.DemandID, &e.Ticket, &e.ProjectID, &e.Project, &e.RunID, &e.Stage, &e.Level, &e.Kind, &e.Message, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	// devolve em ordem cronológica
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ---------- Regras ----------

const ruleCols = `id,scope,project_id,title,category,content,path_glob,language,mandatory,priority,enabled,version,updated_at`

func (s *Store) ListRules(projectID int64, onlyEnabled bool) ([]domain.Rule, error) {
	q := `SELECT ` + ruleCols + ` FROM rules WHERE 1=1`
	args := []any{}
	if projectID >= 0 {
		q += ` AND (scope='global' OR project_id=?)`
		args = append(args, projectID)
	}
	if onlyEnabled {
		q += ` AND enabled=1`
	}
	q += ` ORDER BY CASE scope WHEN 'global' THEN 0 ELSE 1 END, mandatory DESC, priority ASC, id ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Rule{}
	for rows.Next() {
		var r domain.Rule
		var m, e int
		if err := rows.Scan(&r.ID, &r.Scope, &r.ProjectID, &r.Title, &r.Category, &r.Content, &r.PathGlob, &r.Language, &m, &r.Priority, &e, &r.Version, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Mandatory, r.Enabled = m == 1, e == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SaveRule(r *domain.Rule) error {
	t := now()
	r.UpdatedAt = t
	if r.Scope != "projeto" {
		r.Scope, r.ProjectID = "global", 0
	}
	if r.ID == 0 {
		r.Version = 1
		res, err := s.db.Exec(`INSERT INTO rules(scope,project_id,title,category,content,path_glob,language,mandatory,priority,enabled,version,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Scope, r.ProjectID, r.Title, r.Category, r.Content, r.PathGlob, r.Language, b2i(r.Mandatory), r.Priority, b2i(r.Enabled), 1, t)
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
	} else {
		var cur int
		var content string
		if err := s.db.QueryRow(`SELECT version,content FROM rules WHERE id=?`, r.ID).Scan(&cur, &content); err != nil {
			return ErrNotFound
		}
		r.Version = cur + 1
		_, err := s.db.Exec(`UPDATE rules SET scope=?,project_id=?,title=?,category=?,content=?,path_glob=?,language=?,mandatory=?,priority=?,enabled=?,version=?,updated_at=? WHERE id=?`,
			r.Scope, r.ProjectID, r.Title, r.Category, r.Content, r.PathGlob, r.Language, b2i(r.Mandatory), r.Priority, b2i(r.Enabled), r.Version, t, r.ID)
		if err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`INSERT INTO rule_history(rule_id,version,content,changed_at) VALUES (?,?,?,?)`, r.ID, r.Version, r.Content, t)
	return err
}

func (s *Store) DeleteRule(id int64) error {
	_, err := s.db.Exec(`DELETE FROM rules WHERE id=?`, id)
	return err
}

// ---------- Configurações ----------

func (s *Store) GetSettings() (domain.Settings, error) {
	st := domain.DefaultSettings()
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key='app'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	err = json.Unmarshal([]byte(v), &st)
	return st, err
}

func (s *Store) SaveSettings(st domain.Settings) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES('app',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, toJSON(st))
	return err
}

// ---------- Indicadores ----------

type Stats struct {
	ByStage     map[string]int `json:"byStage"`
	Total       int            `json:"total"`
	Running     int            `json:"running"`
	Queued      int            `json:"queued"`
	RunsOK      int            `json:"runsOk"`
	RunsFailed  int            `json:"runsFailed"`
	TotalCost   float64        `json:"totalCost"`
	AvgProgress int            `json:"avgProgress"`
	Projects    int            `json:"projects"`
}

func (s *Store) Stats() (Stats, error) {
	st := Stats{ByStage: map[string]int{}}
	rows, err := s.db.Query(`SELECT stage,COUNT(*) FROM demands GROUP BY stage`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k string
		var n int
		rows.Scan(&k, &n)
		st.ByStage[k] = n
		st.Total += n
	}
	rows.Close()
	s.db.QueryRow(`SELECT COUNT(*) FROM demands WHERE status=?`, domain.StatusRunning).Scan(&st.Running)
	s.db.QueryRow(`SELECT COUNT(*) FROM demands WHERE status=?`, domain.StatusQueued).Scan(&st.Queued)
	s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE status=?`, domain.StatusDone).Scan(&st.RunsOK)
	s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE status IN (?,?)`, domain.StatusFailed, domain.StatusCancelled).Scan(&st.RunsFailed)
	s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd),0) FROM runs`).Scan(&st.TotalCost)
	s.db.QueryRow(`SELECT CAST(COALESCE(AVG(progress),0) AS INTEGER) FROM demands`).Scan(&st.AvgProgress)
	s.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&st.Projects)
	return st, nil
}
