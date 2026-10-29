// Package store implementa a persistência em SQLite (driver puro-Go, sem CGO).
// Anexos ficam em disco; o banco guarda apenas metadados e referências.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"devpipe/internal/cryptx"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

type Store struct {
	db *sql.DB
	// keeper cifra/decifra os segredos gravados (senhas dos servidores de
	// banco). Fica aqui, e não na camada web, para que "cifrado em repouso"
	// seja garantido na fronteira da persistência: não existe caminho que
	// grave um segredo sem passar por ele.
	keeper *cryptx.Keeper
}

// UseKeeper liga o cofre de segredos à store (chamado na inicialização).
func (s *Store) UseKeeper(k *cryptx.Keeper) { s.keeper = k }

var ErrNotFound = errors.New("registro não encontrado")

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: um escritor; evita "database is locked"
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migração: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB expõe a conexão para os testes montarem estados que a API não produz
// (linhas legadas, dados corrompidos). O código de produção não deve usá-la:
// todo SQL fica neste pacote, para que a troca de banco seja local.
func (s *Store) DB() *sql.DB { return s.db }

// migrations: cada item é aplicado uma única vez (controle em schema_version).
var migrations = []string{
	`CREATE TABLE projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		path TEXT NOT NULL UNIQUE,
		language TEXT DEFAULT '',
		main_solution TEXT DEFAULT '',
		branch_options TEXT DEFAULT '[]',
		standards_files TEXT DEFAULT '[]',
		extra_rules TEXT DEFAULT '',
		build_command TEXT DEFAULT '',
		test_command TEXT DEFAULT '',
		agent_bash_allow TEXT DEFAULT '[]',
		branch_prefix TEXT DEFAULT 'devpipe/',
		auto_commit INTEGER DEFAULT 1,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE TABLE demands (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ticket TEXT NOT NULL,
		title TEXT NOT NULL,
		summary TEXT DEFAULT '',
		project_id INTEGER NOT NULL REFERENCES projects(id),
		priority INTEGER DEFAULT 3,
		queue_order INTEGER DEFAULT 0,
		notes TEXT DEFAULT '',
		base_branch TEXT DEFAULT '',
		work_branch TEXT DEFAULT '',
		worktree_path TEXT DEFAULT '',
		provider TEXT DEFAULT 'claude',
		model TEXT DEFAULT '',
		review_spec INTEGER DEFAULT 0,
		stage TEXT NOT NULL DEFAULT 'backlog',
		blocked_from TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'parado',
		status_message TEXT DEFAULT '',
		progress INTEGER DEFAULT 0,
		attempts INTEGER DEFAULT 0,
		validation_state TEXT DEFAULT '',
		current_revision INTEGER DEFAULT 1,
		last_activity TEXT DEFAULT '',
		last_activity_at TIMESTAMP,
		elapsed_seconds INTEGER DEFAULT 0,
		started_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX ix_demands_ticket ON demands(ticket);
	CREATE INDEX ix_demands_project ON demands(project_id);
	CREATE TABLE revisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		demand_id INTEGER NOT NULL REFERENCES demands(id) ON DELETE CASCADE,
		number INTEGER NOT NULL,
		kind TEXT NOT NULL,
		instructions TEXT DEFAULT '',
		spec TEXT DEFAULT '',
		spec_approved INTEGER DEFAULT 0,
		created_at TIMESTAMP NOT NULL,
		UNIQUE(demand_id, number)
	);
	CREATE TABLE attachments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		demand_id INTEGER NOT NULL REFERENCES demands(id) ON DELETE CASCADE,
		revision_id INTEGER NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
		file_name TEXT NOT NULL,
		stored_path TEXT NOT NULL,
		mime TEXT DEFAULT '',
		size INTEGER DEFAULT 0,
		kind TEXT DEFAULT 'outro',
		extracted_path TEXT DEFAULT '',
		extract_note TEXT DEFAULT '',
		created_at TIMESTAMP NOT NULL
	);
	CREATE TABLE runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		demand_id INTEGER NOT NULL REFERENCES demands(id) ON DELETE CASCADE,
		revision_num INTEGER NOT NULL,
		provider TEXT NOT NULL,
		status TEXT NOT NULL,
		stage TEXT NOT NULL,
		rules_hash TEXT DEFAULT '',
		rules_text TEXT DEFAULT '',
		session_id TEXT DEFAULT '',
		summary TEXT DEFAULT '',
		error TEXT DEFAULT '',
		cost_usd REAL DEFAULT 0,
		diff_stat TEXT DEFAULT '',
		commit_sha TEXT DEFAULT '',
		started_at TIMESTAMP NOT NULL,
		finished_at TIMESTAMP
	);
	CREATE INDEX ix_runs_demand ON runs(demand_id);
	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		time TIMESTAMP NOT NULL,
		demand_id INTEGER DEFAULT 0,
		ticket TEXT DEFAULT '',
		project_id INTEGER DEFAULT 0,
		project TEXT DEFAULT '',
		run_id INTEGER DEFAULT 0,
		stage TEXT DEFAULT '',
		level TEXT NOT NULL,
		kind TEXT NOT NULL,
		message TEXT NOT NULL,
		detail TEXT DEFAULT ''
	);
	CREATE INDEX ix_events_demand ON events(demand_id, id);
	CREATE INDEX ix_events_time ON events(time);
	CREATE TABLE rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope TEXT NOT NULL,
		project_id INTEGER DEFAULT 0,
		title TEXT NOT NULL,
		category TEXT DEFAULT '',
		content TEXT NOT NULL,
		path_glob TEXT DEFAULT '',
		language TEXT DEFAULT '',
		mandatory INTEGER DEFAULT 0,
		priority INTEGER DEFAULT 100,
		enabled INTEGER DEFAULT 1,
		version INTEGER DEFAULT 1,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE TABLE rule_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		rule_id INTEGER NOT NULL,
		version INTEGER NOT NULL,
		content TEXT NOT NULL,
		changed_at TIMESTAMP NOT NULL
	);
	CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
	`ALTER TABLE projects ADD COLUMN file_encoding TEXT DEFAULT 'auto';`,
	`ALTER TABLE demands ADD COLUMN direct_branch INTEGER DEFAULT 0;`,
	`ALTER TABLE demands ADD COLUMN base_commit TEXT DEFAULT '';`,
	`ALTER TABLE rules ADD COLUMN source_dir TEXT DEFAULT '';
	 ALTER TABLE rules ADD COLUMN source_glob TEXT DEFAULT '';`,
	// O agendador consulta as demandas na fila a cada 3 s (ver Engine.schedule).
	`CREATE INDEX ix_demands_status ON demands(status, priority, queue_order);`,
	// Tipo da demanda (BUG|FIX|NR): primeiro segmento da branch de trabalho.
	// Demandas anteriores ficam com '' — a branch delas já existe.
	`ALTER TABLE demands ADD COLUMN kind TEXT DEFAULT '';`,
	// Catálogo de servidores de banco. password_enc guarda a senha cifrada
	// (internal/cryptx); nunca texto puro.
	`CREATE TABLE db_servers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		kind TEXT DEFAULT 'sqlserver',
		host TEXT DEFAULT '',
		port INTEGER DEFAULT 0,
		database TEXT DEFAULT '',
		username TEXT DEFAULT '',
		password_enc TEXT DEFAULT '',
		notes TEXT DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	ALTER TABLE projects ADD COLUMN db_server_id INTEGER DEFAULT 0;`,
	// Publicar a branch de trabalho no servidor ao aprovar a revisão humana.
	// Desligado por padrão, inclusive nos projetos já cadastrados: push é a
	// única escrita fora da máquina e precisa ser habilitado de propósito.
	`ALTER TABLE projects ADD COLUMN auto_push INTEGER DEFAULT 0;`,
	// Publicação da branch de desenvolvimento e branch de teste criada a
	// partir dela (ver pipeline.SendToTest).
	`ALTER TABLE demands ADD COLUMN pushed INTEGER DEFAULT 0;
	 ALTER TABLE demands ADD COLUMN test_branch TEXT DEFAULT '';
	 ALTER TABLE demands ADD COLUMN test_target TEXT DEFAULT '';`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(v),0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migração %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(v) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fromJSONList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func now() time.Time { return time.Now().UTC() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
