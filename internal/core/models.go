package core

import (
	"time"
)

// Attribution Levels
const (
	AttributionVerified = "verified" // Direct integration/session evidence
	AttributionExplicit = "explicit" // Developer manually associated it
	AttributionInferred = "inferred" // Timing/branch/task correlation, clearly labeled
	AttributionUnknown  = "unknown"  // Evidence insufficient
)

type StorageInterface interface {
	UpsertProject(p *Project) error
	GetProject(id string) (*Project, error)
	InsertEvent(evt *Event) error
	ListEvents(branch, eventType string, limit int) ([]Event, error)
	CreateTask(t *Task) error
	GetTask(id string) (*Task, error)
	ListTasks(projectID string) ([]Task, error)
	LinkTask(taskID, linkType, linkValue string) error
	UpdateTaskStatus(taskID, status string) error
	AddTaskNote(taskID, note string) error
	CreateCheckpoint(cp *Checkpoint) error
	ListCheckpoints(taskID string, limit int) ([]Checkpoint, error)
	CreateEvidence(ev *Evidence) error
	ListEvidence(branch string, limit int) ([]Evidence, error)
	CreateReport(r *Report) error
	ListReports(reportType string, limit int) ([]Report, error)
	CreateReportSnapshot(s *ReportSnapshot) error
	ListSnapshots(reportID string) ([]ReportSnapshot, error)
	Close() error
}

// Task Statuses
const (
	TaskStatusPlanned        = "planned"
	TaskStatusInProgress     = "in_progress"
	TaskStatusBlocked        = "blocked"
	TaskStatusReadyForReview = "ready_for_review"
	TaskStatusTesting        = "testing"
	TaskStatusMerged         = "merged"
	TaskStatusReleased       = "released"
	TaskStatusClosed         = "closed"
)

// Report Types
const (
	ReportTypeActivity           = "activity"
	ReportTypeReleaseIntegration = "release_integration"
	ReportTypeReleaseVersioned   = "release_versioned"
	ReportTypeBranchComparison   = "branch_comparison"
	ReportTypeDaily              = "daily"
	ReportTypeWeekly             = "weekly"
	ReportTypeMonthly            = "monthly"
	ReportTypeYearly             = "yearly"
	ReportTypeCustom             = "custom"
	ReportTypeDigest             = "digest"
)

// Evidence Types & Results
const (
	EvidenceTypeTest      = "test"
	EvidenceTypeBuild     = "build"
	EvidenceTypeCI        = "ci"
	EvidenceTypeManual    = "manual"
	EvidenceTypeBenchmark = "benchmark"

	EvidenceResultPassed  = "passed"
	EvidenceResultFailed  = "failed"
	EvidenceResultError   = "error"
	EvidenceResultSkipped = "skipped"
)

// Project represents the repository configuration and root identity.
type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	RootPath      string    `json:"root_path"`
	DefaultBranch string    `json:"default_branch"`
	Timezone      string    `json:"timezone"`
	ConfigVersion int       `json:"config_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Worktree represents a Git worktree linked to the repository.
type Worktree struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	Path           string    `json:"path"`
	Branch         string    `json:"branch"`
	HeadSHA        string    `json:"head_sha"`
	IsBare         bool      `json:"is_bare"`
	IsLocked       bool      `json:"is_locked"`
	LastObservedAt time.Time `json:"last_observed_at"`
	Status         string    `json:"status"` // 'active', 'detached', 'pruned'
}

// GitRef represents branches, tags, and tracking refs.
type GitRef struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	RefName       string    `json:"ref_name"`
	RefType       string    `json:"ref_type"` // 'branch', 'tag', 'remote'
	TargetSHA     string    `json:"target_sha"`
	UpstreamRef   string    `json:"upstream_ref,omitempty"`
	AheadCount    int       `json:"ahead_count"`
	BehindCount   int       `json:"behind_count"`
	LastUpdatedAt time.Time `json:"last_updated_at"`
}

// Commit represents a Git commit with change metadata.
type Commit struct {
	SHA            string    `json:"sha"`
	ShortSHA       string    `json:"short_sha"`
	ParentSHAs     []string  `json:"parent_shas"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	Timestamp      time.Time `json:"timestamp"`
	Message        string    `json:"message"`
	Subject        string    `json:"subject"`
	FilesChanged   int       `json:"files_changed"`
	Insertions     int       `json:"insertions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   []string  `json:"changed_files,omitempty"`
}

// CommitDetail provides extended information for GitKraken-style commit inspection.
type CommitDetail struct {
	Commit
	Diff string `json:"diff,omitempty"`
}

// Event represents an append-oriented event in the immutable event log.
type Event struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	WorktreeID          string    `json:"worktree_id,omitempty"`
	EventType           string    `json:"event_type"` // 'commit', 'merge', 'tag', 'checkpoint', 'test_run', 'task_update'
	Source              string    `json:"source"`     // 'git_collector', 'cli', 'dashboard', 'agent_adapter'
	SourceEventID       string    `json:"source_event_id,omitempty"`
	IdempotencyKey      string    `json:"idempotency_key,omitempty"`
	Branch              string    `json:"branch,omitempty"`
	CommitSHA           string    `json:"commit_sha,omitempty"`
	AttributionLevel    string    `json:"attribution_level"` // verified, explicit, inferred, unknown
	AttributionProvider string    `json:"attribution_provider,omitempty"`
	PayloadJSON         string    `json:"payload_json"`
	OccurredAt          time.Time `json:"occurred_at"`
	ObservedAt          time.Time `json:"observed_at"`
}

// Task represents a developer feature, fix, or work item.
type Task struct {
	ID          string    `json:"id"` // e.g. DT-0001
	ProjectID   string    `json:"project_id"`
	Title       string    `json:"title"`
	TaskType    string    `json:"task_type"` // 'feature', 'fix', 'refactor', 'chore'
	Status      string    `json:"status"`
	Objective   string    `json:"objective,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	AIProvider  string    `json:"ai_provider,omitempty"` // claude, chatgpt, gemini, copilot, local, human
	TestStatus  string    `json:"test_status,omitempty"` // passed, failed, unverified
	Uncommitted bool      `json:"uncommitted,omitempty"`
	Branches    []string  `json:"branches,omitempty"`
	Commits     []string  `json:"commits,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CommitGraphNode represents a node in the visual GitKraken-style branch graph.
type CommitGraphNode struct {
	SHA        string    `json:"sha"`
	ShortSHA   string    `json:"short_sha"`
	ParentSHAs []string  `json:"parent_shas"`
	AuthorName string    `json:"author_name"`
	Timestamp  time.Time `json:"timestamp"`
	Subject    string    `json:"subject"`
	Lane       int       `json:"lane"`
	Refs       []string  `json:"refs"`
	IsHead     bool      `json:"is_head"`
	TaskID     string    `json:"task_id,omitempty"`
}

// HandoffPackage represents an evidence-backed context package for transferring work between AI providers.
type HandoffPackage struct {
	TaskID          string       `json:"task_id"`
	Title           string       `json:"title"`
	Status          string       `json:"status"`
	Objective       string       `json:"objective"`
	Notes           string       `json:"notes"`
	CurrentBranch   string       `json:"current_branch"`
	HeadSHA         string       `json:"head_sha"`
	Commits         []Commit     `json:"commits"`
	LatestEvidence  *Evidence    `json:"latest_evidence,omitempty"`
	Checkpoints     []Checkpoint `json:"checkpoints"`
	FormattedPrompt string       `json:"formatted_prompt"`
}

// TaskLink maps a relationship between a task and a branch, commit, session, etc.
type TaskLink struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	LinkType  string    `json:"link_type"` // 'branch', 'commit', 'agent_session', 'pr'
	LinkValue string    `json:"link_value"`
	CreatedAt time.Time `json:"created_at"`
}

// Checkpoint represents a recorded milestone with commit/branch context.
type Checkpoint struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	TaskID     string    `json:"task_id,omitempty"`
	Branch     string    `json:"branch"`
	CommitSHA  string    `json:"commit_sha"`
	Message    string    `json:"message"`
	EvidenceID string    `json:"evidence_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Evidence represents test runs, build results, or CI evidence.
type Evidence struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	EvidenceType    string    `json:"evidence_type"` // 'test', 'build', 'ci', 'manual'
	Result          string    `json:"result"`        // 'passed', 'failed', 'error', 'skipped'
	Command         string    `json:"command,omitempty"`
	DurationMs      int64     `json:"duration_ms"`
	CommitSHA       string    `json:"commit_sha"`
	Branch          string    `json:"branch"`
	LogArtifactPath string    `json:"log_artifact_path,omitempty"`
	MetadataJSON    string    `json:"metadata_json,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// Report represents a generated report record.
type Report struct {
	ID             string           `json:"id"`
	ProjectID      string           `json:"project_id"`
	ReportType     string           `json:"report_type"`
	Title          string           `json:"title"`
	Status         string           `json:"status"` // 'generated', 'generating', 'failed'
	BaseRef        string           `json:"base_ref,omitempty"`
	HeadRef        string           `json:"head_ref,omitempty"`
	BaseSHA        string           `json:"base_sha,omitempty"`
	HeadSHA        string           `json:"head_sha,omitempty"`
	SinceTime      *time.Time       `json:"since_time,omitempty"`
	UntilTime      *time.Time       `json:"until_time,omitempty"`
	TaskID         string           `json:"task_id,omitempty"`
	FilePath       string           `json:"file_path"`
	ProfileVersion int              `json:"profile_version"`
	WarningsJSON   string           `json:"warnings_json,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	Snapshots      []ReportSnapshot `json:"snapshots,omitempty"`
	// WebURL is the path the report is served at, computed by the API layer
	// from FilePath. It is not persisted.
	WebURL string `json:"web_url,omitempty"`
}

// ReportSnapshot represents an immutable checkpoint of an evolving report.
type ReportSnapshot struct {
	ID             string    `json:"id"`
	ReportID       string    `json:"report_id"`
	SnapshotNumber int       `json:"snapshot_number"`
	TriggerReason  string    `json:"trigger_reason"`
	HeadSHA        string    `json:"head_sha"`
	FilePath       string    `json:"file_path"`
	CreatedAt      time.Time `json:"created_at"`
}

// Schedule represents an automated report recurrence.
type Schedule struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"project_id"`
	Name          string     `json:"name"`
	Frequency     string     `json:"frequency"` // 'daily', 'weekly', 'monthly', 'yearly', 'interval'
	CronExpr      string     `json:"cron_expr,omitempty"`
	AtTime        string     `json:"at_time,omitempty"`
	DayOfWeek     string     `json:"day_of_week,omitempty"`
	ReportType    string     `json:"report_type"`
	WindowSpec    string     `json:"window_spec"`
	CatchUpPolicy string     `json:"catch_up_policy"` // 'run_once', 'skip'
	IsEnabled     bool       `json:"is_enabled"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	NextRunAt     *time.Time `json:"next_run_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// Trigger represents an event-driven automation hook.
type Trigger struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	Name            string     `json:"name"`
	EventPattern    string     `json:"event_pattern"` // 'merge:main', 'tag:v*', 'post-commit'
	ReportType      string     `json:"report_type"`
	DebounceSeconds int        `json:"debounce_seconds"`
	IsEnabled       bool       `json:"is_enabled"`
	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}
