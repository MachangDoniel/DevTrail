package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/google/uuid"
)

type StorageInterface interface {
	UpsertProject(p *core.Project) error
	GetProject(id string) (*core.Project, error)
	InsertEvent(evt *core.Event) error
	ListEvents(branch, eventType string, limit int) ([]core.Event, error)
	CreateTask(t *core.Task) error
	GetTask(id string) (*core.Task, error)
	ListTasks(projectID string) ([]core.Task, error)
	LinkTask(taskID, linkType, linkValue string) error
	UpdateTaskStatus(taskID, status string) error
	AddTaskNote(taskID, note string) error
	CreateCheckpoint(cp *core.Checkpoint) error
	ListCheckpoints(taskID string, limit int) ([]core.Checkpoint, error)
	CreateEvidence(ev *core.Evidence) error
	ListEvidence(branch string, limit int) ([]core.Evidence, error)
	CreateReport(r *core.Report) error
	ListReports(reportType string, limit int) ([]core.Report, error)
	CreateReportSnapshot(s *core.ReportSnapshot) error
	ListSnapshots(reportID string) ([]core.ReportSnapshot, error)
	FindReportByTarget(projectID, reportType, headRef, taskID string) (*core.Report, error)
	UpdateReport(r *core.Report) error
	NextSnapshotNumber(reportID string) (int, error)
	UpsertSchedule(s *core.Schedule) error
	ListSchedules() ([]core.Schedule, error)
	MarkScheduleRun(name string, ranAt, nextRun time.Time) error
	UpsertTrigger(t *core.Trigger) error
	ListTriggers() ([]core.Trigger, error)
	MarkTriggerFired(name string, at time.Time) error
	HasEventWithKey(key string) (bool, error)
	Close() error
}

type ProjectService struct {
	RepoDir string
	Config  *config.Config
	Store   StorageInterface
	Git     *collector.GitCollector
}

type ProjectStatus struct {
	Name             string       `json:"name"`
	RootPath         string       `json:"root_path"`
	CurrentBranch    string       `json:"current_branch"`
	HeadSHA          string       `json:"head_sha"`
	WorkingTreeClean bool         `json:"working_tree_clean"`
	StagedFiles      int          `json:"staged_files"`
	ModifiedFiles    int          `json:"modified_files"`
	UntrackedFiles   int          `json:"untracked_files"`
	TotalTasks       int          `json:"total_tasks"`
	TotalReports     int          `json:"total_reports"`
	TotalEvidence    int          `json:"total_evidence"`
	RecentEvents     []core.Event `json:"recent_events"`
	LastUpdated      time.Time    `json:"last_updated"`
}

func NewProjectService(repoDir string, cfg *config.Config, store StorageInterface) *ProjectService {
	return &ProjectService{
		RepoDir: repoDir,
		Config:  cfg,
		Store:   store,
		Git:     collector.NewGitCollector(repoDir),
	}
}

// InitProject initializes DevTrail in a target directory.
func InitProject(repoDir string, projectName string, autoGitIgnore bool) (*config.Config, []string, error) {
	gitCol := collector.NewGitCollector(repoDir)
	isGit, root := gitCol.IsGitRepo()
	if !isGit {
		return nil, nil, fmt.Errorf("directory %s is not a git repository (run 'git init' first)", repoDir)
	}
	repoDir = root

	if projectName == "" {
		projectName = filepath.Base(repoDir)
	}

	currentBranch, _ := gitCol.CurrentBranch()
	if currentBranch == "" {
		currentBranch = "main"
	}

	cfg := config.DefaultConfig(projectName, currentBranch)
	if err := cfg.Save(repoDir); err != nil {
		return nil, nil, fmt.Errorf("failed to save config: %w", err)
	}

	// Prepare directories
	dtDir := filepath.Join(repoDir, config.DefaultDirName)
	for _, sub := range []string{"reports", "events", "cache", "logs", "hooks"} {
		if err := os.MkdirAll(filepath.Join(dtDir, sub), 0755); err != nil {
			return nil, nil, err
		}
	}

	// Check gitignore
	missingEntries, _, _ := config.CheckGitIgnore(repoDir)
	if autoGitIgnore && len(missingEntries) > 0 {
		_ = config.AppendGitIgnore(repoDir, missingEntries)
	}

	return cfg, missingEntries, nil
}

// GetStatus computes real-time repository and intelligence status.
func (s *ProjectService) GetStatus() (*ProjectStatus, error) {
	currBranch, _ := s.Git.CurrentBranch()
	headSHA, _ := s.Git.CurrentHeadSHA()
	staged, modified, untracked, clean, _ := s.Git.WorkingTreeSummary()

	var totalTasks, totalReports, totalEvidence int
	var recentEvents []core.Event

	if s.Store != nil {
		if tasks, err := s.Store.ListTasks(""); err == nil {
			totalTasks = len(tasks)
		}
		if reports, err := s.Store.ListReports("", 50); err == nil {
			totalReports = len(reports)
		}
		if ev, err := s.Store.ListEvidence("", 50); err == nil {
			totalEvidence = len(ev)
		}
		if evts, err := s.Store.ListEvents("", "", 10); err == nil {
			recentEvents = evts
		}
	}

	name := "DevTrail Project"
	if s.Config != nil && s.Config.Project.Name != "" {
		name = s.Config.Project.Name
	}

	return &ProjectStatus{
		Name:             name,
		RootPath:         s.RepoDir,
		CurrentBranch:    currBranch,
		HeadSHA:          headSHA,
		WorkingTreeClean: clean,
		StagedFiles:      staged,
		ModifiedFiles:    modified,
		UntrackedFiles:   untracked,
		TotalTasks:       totalTasks,
		TotalReports:     totalReports,
		TotalEvidence:    totalEvidence,
		RecentEvents:     recentEvents,
		LastUpdated:      time.Now().UTC(),
	}, nil
}

// SyncCurrentState observes git HEAD and branches and logs events if needed.
func (s *ProjectService) SyncCurrentState() error {
	if s.Store == nil {
		return nil
	}

	currBranch, err := s.Git.CurrentBranch()
	if err != nil {
		return err
	}
	headSHA, err := s.Git.CurrentHeadSHA()
	if err != nil {
		return err
	}

	// Record event for current observed state
	idempKey := fmt.Sprintf("observe:%s:%s", currBranch, headSHA)
	evt := &core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "observe",
		Source:           "git_collector",
		IdempotencyKey:   idempKey,
		Branch:           currBranch,
		CommitSHA:        headSHA,
		AttributionLevel: core.AttributionVerified,
		PayloadJSON:      payload(map[string]interface{}{"branch": currBranch, "head_sha": headSHA}),
		OccurredAt:       time.Now().UTC(),
		ObservedAt:       time.Now().UTC(),
	}
	return s.Store.InsertEvent(evt)
}

// FormatStatusText produces human-friendly status text for the CLI.
func (status *ProjectStatus) FormatStatusText() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("DevTrail Project: %s\n", status.Name))
	sb.WriteString(fmt.Sprintf("Root: %s\n", status.RootPath))
	sb.WriteString(fmt.Sprintf("Branch: %s (HEAD: %s)\n", status.CurrentBranch, truncateSHA(status.HeadSHA)))
	if status.WorkingTreeClean {
		sb.WriteString("Working tree: Clean\n")
	} else {
		sb.WriteString(fmt.Sprintf("Working tree: %d staged, %d modified, %d untracked\n", status.StagedFiles, status.ModifiedFiles, status.UntrackedFiles))
	}
	sb.WriteString(fmt.Sprintf("Intelligence: %d tasks | %d reports | %d evidence entries\n", status.TotalTasks, status.TotalReports, status.TotalEvidence))
	return sb.String()
}

func truncateSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
