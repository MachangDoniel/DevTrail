package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/reports"
	"github.com/donieltripura/devtrail/internal/services"
	"github.com/donieltripura/devtrail/internal/storage"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	cfg := config.DefaultConfig("test-project", "main")
	if cfg.Project.Name != "test-project" {
		t.Fatalf("expected project name 'test-project', got '%s'", cfg.Project.Name)
	}
	if cfg.Project.DefaultBranch != "main" {
		t.Fatalf("expected default branch 'main', got '%s'", cfg.Project.DefaultBranch)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}

	// Invalidate and verify
	cfg.Project.Name = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error on empty project name, got nil")
	}
}

func TestStorageAndMigrations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "devtrail-store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test_state.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open/migrate db: %v", err)
	}
	defer db.Close()

	// Upsert project first to satisfy foreign key constraints
	proj := &core.Project{
		ID:            "default",
		Name:          "Test Project",
		RootPath:      tmpDir,
		DefaultBranch: "main",
	}
	if err := db.UpsertProject(proj); err != nil {
		t.Fatalf("failed to upsert project: %v", err)
	}

	// 1. Tasks
	task := &core.Task{
		ID:        "DT-TEST",
		ProjectID: "default",
		Title:     "Unit Test Task",
		TaskType:  "feature",
		Status:    core.TaskStatusPlanned,
	}
	if err := db.CreateTask(task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := db.LinkTask("DT-TEST", "branch", "feature/test-branch"); err != nil {
		t.Fatalf("failed to link task: %v", err)
	}

	fetchedTask, err := db.GetTask("DT-TEST")
	if err != nil {
		t.Fatalf("failed to fetch task: %v", err)
	}
	if len(fetchedTask.Branches) != 1 || fetchedTask.Branches[0] != "feature/test-branch" {
		t.Fatalf("expected linked branch 'feature/test-branch', got %v", fetchedTask.Branches)
	}

	// 2. Checkpoints
	cp := &core.Checkpoint{
		ID:        "CP-01",
		ProjectID: "default",
		TaskID:    "DT-TEST",
		Branch:    "feature/test-branch",
		CommitSHA: "abcdef0123456789",
		Message:   "Checkpoint in unit test",
		CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateCheckpoint(cp); err != nil {
		t.Fatalf("failed to create checkpoint: %v", err)
	}
	cps, err := db.ListCheckpoints("DT-TEST", 10)
	if err != nil || len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d (%v)", len(cps), err)
	}

	// 3. Evidence
	ev := &core.Evidence{
		ID:           "EV-01",
		ProjectID:    "default",
		EvidenceType: "test",
		Result:       "passed",
		Command:      "go test",
		DurationMs:   120,
		CommitSHA:    "abcdef0123456789",
		Branch:       "feature/test-branch",
		CreatedAt:    time.Now().UTC(),
	}
	if err := db.CreateEvidence(ev); err != nil {
		t.Fatalf("failed to create evidence: %v", err)
	}
	evs, err := db.ListEvidence("feature/test-branch", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("expected 1 evidence, got %d (%v)", len(evs), err)
	}
}

func TestReportGenerationAndSnapshots(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig("DevTrail", "main")
	dbPath := filepath.Join(wd, ".devtrail", "state.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_ = db.UpsertProject(&core.Project{
		ID:            "default",
		Name:          "DevTrail",
		RootPath:      wd,
		DefaultBranch: "main",
	})

	engine := reports.NewReportEngine(wd, cfg, db)

	// Activity report
	report, path, err := engine.GenerateActivityReport("main", "", "Automated Test Snapshot")
	if err != nil {
		t.Fatalf("failed to generate activity report: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("activity report file does not exist at %s: %v", path, err)
	}

	// Verify snapshots
	snaps, err := db.ListSnapshots(report.ID)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("expected at least 1 snapshot recorded, got %d (%v)", len(snaps), err)
	}
}

func TestGitCollectorReadOnlySafety(t *testing.T) {
	wd, _ := os.Getwd()
	gitCol := collector.NewGitCollector(wd)

	isGit, root := gitCol.IsGitRepo()
	if !isGit || root == "" {
		t.Fatalf("expected valid git repository root, got %s", root)
	}

	currBranch, err := gitCol.CurrentBranch()
	if err != nil || currBranch == "" {
		t.Fatalf("expected current branch, got '%s' (err: %v)", currBranch, err)
	}

	headSHA, err := gitCol.CurrentHeadSHA()
	if err != nil || len(headSHA) != 40 {
		t.Fatalf("expected 40-char commit SHA, got '%s' (err: %v)", headSHA, err)
	}

	_, _, _, clean, err := gitCol.WorkingTreeSummary()
	if err != nil {
		t.Fatalf("failed to get working tree status: %v", err)
	}
	_ = clean
}

func TestDoctorDiagnostics(t *testing.T) {
	wd, _ := os.Getwd()
	cfg := config.DefaultConfig("DevTrail", "main")
	doc := services.NewDoctorService(wd, cfg)
	report := doc.RunDiagnostics()

	if report.OverallStatus != "HEALTHY" {
		t.Fatalf("expected HEALTHY diagnostics in initialized repo, got '%s'", report.OverallStatus)
	}
	if len(report.Items) < 5 {
		t.Fatalf("expected at least 5 diagnostic items, got %d", len(report.Items))
	}
}

func TestCommitGraph(t *testing.T) {
	wd, _ := os.Getwd()
	gitCol := collector.NewGitCollector(wd)
	nodes, err := gitCol.GetCommitGraph(20)
	if err != nil {
		t.Fatalf("failed to get commit graph: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least 1 commit in graph, got 0")
	}
	if nodes[0].SHA == "" || nodes[0].ShortSHA == "" {
		t.Fatal("commit graph node missing SHA")
	}
}

func TestHandoffPackage(t *testing.T) {
	wd, _ := os.Getwd()
	dbPath := filepath.Join(wd, ".devtrail", "state.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	taskSvc := services.NewTaskService(db, collector.NewGitCollector(wd))
	task, err := taskSvc.CreateTask("Test Handoff Feature", "feature", "Verify handoff prompt formatting", "claude", true)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	handoffSvc := services.NewHandoffService(db, collector.NewGitCollector(wd))
	pkg, err := handoffSvc.GeneratePackage(task.ID)
	if err != nil {
		t.Fatalf("failed to generate handoff package: %v", err)
	}

	if pkg.TaskID != task.ID {
		t.Fatalf("expected task id %s, got %s", task.ID, pkg.TaskID)
	}
	if pkg.FormattedPrompt == "" {
		t.Fatal("expected formatted prompt to not be empty")
	}
}
