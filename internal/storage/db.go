package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/donieltripura/devtrail/internal/core"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

// Open initializes and migrates the SQLite database.
func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// migrate creates all database tables and indexes.
func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		root_path TEXT NOT NULL UNIQUE,
		default_branch TEXT NOT NULL DEFAULT 'main',
		timezone TEXT NOT NULL DEFAULT 'UTC',
		config_version INTEGER NOT NULL DEFAULT 1,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS worktrees (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		path TEXT NOT NULL UNIQUE,
		branch TEXT,
		head_sha TEXT,
		is_bare BOOLEAN NOT NULL DEFAULT 0,
		is_locked BOOLEAN NOT NULL DEFAULT 0,
		last_observed_at TIMESTAMP NOT NULL,
		status TEXT NOT NULL DEFAULT 'active'
	);

	CREATE TABLE IF NOT EXISTS git_refs (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		ref_name TEXT NOT NULL,
		ref_type TEXT NOT NULL,
		target_sha TEXT NOT NULL,
		upstream_ref TEXT,
		ahead_count INTEGER DEFAULT 0,
		behind_count INTEGER DEFAULT 0,
		last_updated_at TIMESTAMP NOT NULL,
		UNIQUE(project_id, ref_name)
	);

	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		worktree_id TEXT,
		event_type TEXT NOT NULL,
		source TEXT NOT NULL,
		source_event_id TEXT,
		idempotency_key TEXT UNIQUE,
		branch TEXT,
		commit_sha TEXT,
		attribution_level TEXT NOT NULL DEFAULT 'unknown',
		attribution_provider TEXT,
		payload_json TEXT NOT NULL,
		occurred_at TIMESTAMP NOT NULL,
		observed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_events_occurred_at ON events(occurred_at DESC);
	CREATE INDEX IF NOT EXISTS idx_events_branch ON events(branch);
	CREATE INDEX IF NOT EXISTS idx_events_commit_sha ON events(commit_sha);

	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		title TEXT NOT NULL,
		task_type TEXT NOT NULL DEFAULT 'feature',
		status TEXT NOT NULL DEFAULT 'planned',
		objective TEXT,
		notes TEXT,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);

	CREATE TABLE IF NOT EXISTS task_links (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		link_type TEXT NOT NULL,
		link_value TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(task_id, link_type, link_value)
	);

	CREATE TABLE IF NOT EXISTS checkpoints (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		task_id TEXT,
		branch TEXT NOT NULL,
		commit_sha TEXT NOT NULL,
		message TEXT NOT NULL,
		evidence_id TEXT,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS evidence (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		evidence_type TEXT NOT NULL,
		result TEXT NOT NULL,
		command TEXT,
		duration_ms INTEGER DEFAULT 0,
		commit_sha TEXT NOT NULL,
		branch TEXT NOT NULL,
		log_artifact_path TEXT,
		metadata_json TEXT,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_evidence_commit_sha ON evidence(commit_sha);

	CREATE TABLE IF NOT EXISTS reports (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		report_type TEXT NOT NULL,
		title TEXT NOT NULL,
		status TEXT NOT NULL,
		base_ref TEXT,
		head_ref TEXT,
		base_sha TEXT,
		head_sha TEXT,
		since_time TIMESTAMP,
		until_time TIMESTAMP,
		task_id TEXT,
		file_path TEXT NOT NULL,
		profile_version INTEGER NOT NULL DEFAULT 1,
		warnings_json TEXT,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_reports_created_at ON reports(created_at DESC);

	CREATE TABLE IF NOT EXISTS report_snapshots (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
		snapshot_number INTEGER NOT NULL,
		trigger_reason TEXT NOT NULL,
		head_sha TEXT NOT NULL,
		file_path TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS schedules (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL UNIQUE,
		frequency TEXT NOT NULL,
		cron_expr TEXT,
		at_time TEXT,
		day_of_week TEXT,
		report_type TEXT NOT NULL,
		window_spec TEXT NOT NULL,
		catch_up_policy TEXT NOT NULL DEFAULT 'run_once',
		is_enabled BOOLEAN NOT NULL DEFAULT 1,
		last_run_at TIMESTAMP,
		next_run_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS triggers (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL UNIQUE,
		event_pattern TEXT NOT NULL,
		report_type TEXT NOT NULL,
		debounce_seconds INTEGER NOT NULL DEFAULT 30,
		is_enabled BOOLEAN NOT NULL DEFAULT 1,
		last_triggered_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := d.conn.Exec(schema)
	return err
}

// UpsertProject inserts or updates the project entry.
func (d *DB) UpsertProject(p *core.Project) error {
	now := time.Now().UTC()
	query := `
	INSERT INTO projects (id, name, root_path, default_branch, timezone, config_version, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		default_branch = excluded.default_branch,
		timezone = excluded.timezone,
		config_version = excluded.config_version,
		updated_at = excluded.updated_at
	`
	if p.ID == "" {
		p.ID = "default"
	}
	p.UpdatedAt = now
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}

	_, err := d.conn.Exec(query, p.ID, p.Name, p.RootPath, p.DefaultBranch, p.Timezone, p.ConfigVersion, p.CreatedAt, p.UpdatedAt)
	return err
}

// GetProject retrieves project details.
func (d *DB) GetProject(id string) (*core.Project, error) {
	if id == "" {
		id = "default"
	}
	var p core.Project
	query := `SELECT id, name, root_path, default_branch, timezone, config_version, created_at, updated_at FROM projects WHERE id = ? LIMIT 1`
	err := d.conn.QueryRow(query, id).Scan(&p.ID, &p.Name, &p.RootPath, &p.DefaultBranch, &p.Timezone, &p.ConfigVersion, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// InsertEvent records an append-only event.
func (d *DB) InsertEvent(evt *core.Event) error {
	if evt.ID == "" {
		evt.ID = uuid.NewString()
	}
	if evt.ObservedAt.IsZero() {
		evt.ObservedAt = time.Now().UTC()
	}
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = evt.ObservedAt
	}

	query := `
	INSERT OR IGNORE INTO events (
		id, project_id, worktree_id, event_type, source, source_event_id,
		idempotency_key, branch, commit_sha, attribution_level, attribution_provider,
		payload_json, occurred_at, observed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := d.conn.Exec(query,
		evt.ID, evt.ProjectID, evt.WorktreeID, evt.EventType, evt.Source,
		evt.SourceEventID, evt.IdempotencyKey, evt.Branch, evt.CommitSHA,
		evt.AttributionLevel, evt.AttributionProvider, evt.PayloadJSON,
		evt.OccurredAt, evt.ObservedAt,
	)
	return err
}

// ListEvents queries events with optional filters.
func (d *DB) ListEvents(branch, eventType string, limit int) ([]core.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
	SELECT id, project_id, worktree_id, event_type, source, source_event_id,
	       idempotency_key, branch, commit_sha, attribution_level, attribution_provider,
	       payload_json, occurred_at, observed_at
	FROM events
	WHERE (? = '' OR branch = ?)
	  AND (? = '' OR event_type = ?)
	ORDER BY occurred_at DESC
	LIMIT ?
	`
	rows, err := d.conn.Query(query, branch, branch, eventType, eventType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []core.Event
	for rows.Next() {
		var e core.Event
		var wtID, srcEvtID, idempKey, br, csha, attrProv sql.NullString
		err := rows.Scan(
			&e.ID, &e.ProjectID, &wtID, &e.EventType, &e.Source, &srcEvtID,
			&idempKey, &br, &csha, &e.AttributionLevel, &attrProv,
			&e.PayloadJSON, &e.OccurredAt, &e.ObservedAt,
		)
		if err != nil {
			return nil, err
		}
		if wtID.Valid {
			e.WorktreeID = wtID.String
		}
		if srcEvtID.Valid {
			e.SourceEventID = srcEvtID.String
		}
		if idempKey.Valid {
			e.IdempotencyKey = idempKey.String
		}
		if br.Valid {
			e.Branch = br.String
		}
		if csha.Valid {
			e.CommitSHA = csha.String
		}
		if attrProv.Valid {
			e.AttributionProvider = attrProv.String
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// CreateTask adds a new feature or task.
func (d *DB) CreateTask(t *core.Task) error {
	if t.ID == "" {
		t.ID = fmt.Sprintf("DT-%04d", time.Now().Unix()%10000)
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now

	query := `
	INSERT INTO tasks (id, project_id, title, task_type, status, objective, notes, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := d.conn.Exec(query, t.ID, t.ProjectID, t.Title, t.TaskType, t.Status, t.Objective, t.Notes, t.CreatedAt, t.UpdatedAt)
	return err
}

// GetTask retrieves a task along with its linked branches/commits.
func (d *DB) GetTask(id string) (*core.Task, error) {
	var t core.Task
	var obj, notes sql.NullString
	query := `SELECT id, project_id, title, task_type, status, objective, notes, created_at, updated_at FROM tasks WHERE id = ?`
	err := d.conn.QueryRow(query, id).Scan(&t.ID, &t.ProjectID, &t.Title, &t.TaskType, &t.Status, &obj, &notes, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if obj.Valid {
		t.Objective = obj.String
	}
	if notes.Valid {
		t.Notes = notes.String
	}

	// Fetch links
	linkQuery := `SELECT link_type, link_value FROM task_links WHERE task_id = ?`
	rows, err := d.conn.Query(linkQuery, id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var lType, lVal string
			if err := rows.Scan(&lType, &lVal); err == nil {
				if lType == "branch" {
					t.Branches = append(t.Branches, lVal)
				} else if lType == "commit" {
					t.Commits = append(t.Commits, lVal)
				}
			}
		}
	}

	return &t, nil
}

// ListTasks returns all tasks for a project.
func (d *DB) ListTasks(projectID string) ([]core.Task, error) {
	query := `SELECT id, project_id, title, task_type, status, objective, notes, created_at, updated_at FROM tasks ORDER BY created_at DESC`
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []core.Task
	for rows.Next() {
		var t core.Task
		var obj, notes sql.NullString
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.TaskType, &t.Status, &obj, &notes, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if obj.Valid {
			t.Objective = obj.String
		}
		if notes.Valid {
			t.Notes = notes.String
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// LinkTask associates a branch or commit with a task.
func (d *DB) LinkTask(taskID, linkType, linkValue string) error {
	id := uuid.NewString()
	query := `INSERT OR IGNORE INTO task_links (id, task_id, link_type, link_value, created_at) VALUES (?, ?, ?, ?, ?)`
	_, err := d.conn.Exec(query, id, taskID, linkType, linkValue, time.Now().UTC())
	return err
}

// UpdateTaskStatus updates a task's status.
func (d *DB) UpdateTaskStatus(taskID, status string) error {
	query := `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`
	_, err := d.conn.Exec(query, status, time.Now().UTC(), taskID)
	return err
}

// AddTaskNote appends or sets a note for a task.
func (d *DB) AddTaskNote(taskID, note string) error {
	query := `
	UPDATE tasks 
	SET notes = CASE WHEN notes IS NULL OR notes = '' THEN ? ELSE notes || CHAR(10) || ? END,
	    updated_at = ?
	WHERE id = ?`
	now := time.Now().UTC()
	_, err := d.conn.Exec(query, note, note, now, taskID)
	return err
}

// CreateCheckpoint saves a development checkpoint.
func (d *DB) CreateCheckpoint(cp *core.Checkpoint) error {
	if cp.ID == "" {
		cp.ID = uuid.NewString()
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	query := `INSERT INTO checkpoints (id, project_id, task_id, branch, commit_sha, message, evidence_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.conn.Exec(query, cp.ID, cp.ProjectID, cp.TaskID, cp.Branch, cp.CommitSHA, cp.Message, cp.EvidenceID, cp.CreatedAt)
	return err
}

// ListCheckpoints returns checkpoints for a project or task.
func (d *DB) ListCheckpoints(taskID string, limit int) ([]core.Checkpoint, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, project_id, task_id, branch, commit_sha, message, evidence_id, created_at FROM checkpoints WHERE (? = '' OR task_id = ?) ORDER BY created_at DESC LIMIT ?`
	rows, err := d.conn.Query(query, taskID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var checkpoints []core.Checkpoint
	for rows.Next() {
		var cp core.Checkpoint
		var tID, evID sql.NullString
		if err := rows.Scan(&cp.ID, &cp.ProjectID, &tID, &cp.Branch, &cp.CommitSHA, &cp.Message, &evID, &cp.CreatedAt); err != nil {
			return nil, err
		}
		if tID.Valid {
			cp.TaskID = tID.String
		}
		if evID.Valid {
			cp.EvidenceID = evID.String
		}
		checkpoints = append(checkpoints, cp)
	}
	return checkpoints, rows.Err()
}

// CreateEvidence saves test/build/ci outcomes.
func (d *DB) CreateEvidence(ev *core.Evidence) error {
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	query := `INSERT INTO evidence (id, project_id, evidence_type, result, command, duration_ms, commit_sha, branch, log_artifact_path, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.conn.Exec(query, ev.ID, ev.ProjectID, ev.EvidenceType, ev.Result, ev.Command, ev.DurationMs, ev.CommitSHA, ev.Branch, ev.LogArtifactPath, ev.MetadataJSON, ev.CreatedAt)
	return err
}

// ListEvidence retrieves evidence records.
func (d *DB) ListEvidence(branch string, limit int) ([]core.Evidence, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, project_id, evidence_type, result, command, duration_ms, commit_sha, branch, log_artifact_path, metadata_json, created_at FROM evidence WHERE (? = '' OR branch = ?) ORDER BY created_at DESC LIMIT ?`
	rows, err := d.conn.Query(query, branch, branch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []core.Evidence
	for rows.Next() {
		var ev core.Evidence
		var cmd, logPath, meta sql.NullString
		if err := rows.Scan(&ev.ID, &ev.ProjectID, &ev.EvidenceType, &ev.Result, &cmd, &ev.DurationMs, &ev.CommitSHA, &ev.Branch, &logPath, &meta, &ev.CreatedAt); err != nil {
			return nil, err
		}
		if cmd.Valid {
			ev.Command = cmd.String
		}
		if logPath.Valid {
			ev.LogArtifactPath = logPath.String
		}
		if meta.Valid {
			ev.MetadataJSON = meta.String
		}
		list = append(list, ev)
	}
	return list, rows.Err()
}

// CreateReport records a new report.
func (d *DB) CreateReport(r *core.Report) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	query := `
	INSERT INTO reports (
		id, project_id, report_type, title, status, base_ref, head_ref,
		base_sha, head_sha, since_time, until_time, task_id, file_path,
		profile_version, warnings_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := d.conn.Exec(query,
		r.ID, r.ProjectID, r.ReportType, r.Title, r.Status, r.BaseRef, r.HeadRef,
		r.BaseSHA, r.HeadSHA, r.SinceTime, r.UntilTime, r.TaskID, r.FilePath,
		r.ProfileVersion, r.WarningsJSON, r.CreatedAt,
	)
	return err
}

// ListReports queries reports.
func (d *DB) ListReports(reportType string, limit int) ([]core.Report, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
	SELECT id, project_id, report_type, title, status, base_ref, head_ref,
	       base_sha, head_sha, since_time, until_time, task_id, file_path,
	       profile_version, warnings_json, created_at
	FROM reports
	WHERE (? = '' OR report_type = ?)
	ORDER BY created_at DESC
	LIMIT ?
	`
	rows, err := d.conn.Query(query, reportType, reportType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reports []core.Report
	for rows.Next() {
		var r core.Report
		var bRef, hRef, bSha, hSha, tID, warn sql.NullString
		var since, until sql.NullTime
		if err := rows.Scan(
			&r.ID, &r.ProjectID, &r.ReportType, &r.Title, &r.Status,
			&bRef, &hRef, &bSha, &hSha, &since, &until, &tID,
			&r.FilePath, &r.ProfileVersion, &warn, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		if bRef.Valid {
			r.BaseRef = bRef.String
		}
		if hRef.Valid {
			r.HeadRef = hRef.String
		}
		if bSha.Valid {
			r.BaseSHA = bSha.String
		}
		if hSha.Valid {
			r.HeadSHA = hSha.String
		}
		if since.Valid {
			r.SinceTime = &since.Time
		}
		if until.Valid {
			r.UntilTime = &until.Time
		}
		if tID.Valid {
			r.TaskID = tID.String
		}
		if warn.Valid {
			r.WarningsJSON = warn.String
		}
		reports = append(reports, r)
	}
	return reports, rows.Err()
}

// CreateReportSnapshot saves a snapshot record for an evolving report.
func (d *DB) CreateReportSnapshot(s *core.ReportSnapshot) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	query := `INSERT INTO report_snapshots (id, report_id, snapshot_number, trigger_reason, head_sha, file_path, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := d.conn.Exec(query, s.ID, s.ReportID, s.SnapshotNumber, s.TriggerReason, s.HeadSHA, s.FilePath, s.CreatedAt)
	return err
}

// ListSnapshots returns snapshots for a report.
func (d *DB) ListSnapshots(reportID string) ([]core.ReportSnapshot, error) {
	query := `SELECT id, report_id, snapshot_number, trigger_reason, head_sha, file_path, created_at FROM report_snapshots WHERE report_id = ? ORDER BY snapshot_number ASC`
	rows, err := d.conn.Query(query, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []core.ReportSnapshot
	for rows.Next() {
		var s core.ReportSnapshot
		if err := rows.Scan(&s.ID, &s.ReportID, &s.SnapshotNumber, &s.TriggerReason, &s.HeadSHA, &s.FilePath, &s.CreatedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}
