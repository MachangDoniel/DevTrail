package reports

import (
	"bytes"
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

type ReportEngine struct {
	RepoDir string
	Config  *config.Config
	Store   core.StorageInterface
	Git     *collector.GitCollector
}

func NewReportEngine(repoDir string, cfg *config.Config, store core.StorageInterface) *ReportEngine {
	return &ReportEngine{
		RepoDir: repoDir,
		Config:  cfg,
		Store:   store,
		Git:     collector.NewGitCollector(repoDir),
	}
}

// GenerateActivityReport builds an evolving Activity Report for a branch or task.
func (e *ReportEngine) GenerateActivityReport(branch string, taskID string, snapshotReason string) (*core.Report, string, error) {
	if branch == "" {
		var err error
		branch, err = e.Git.CurrentBranch()
		if err != nil {
			branch = "main"
		}
	}

	headSHA, _ := e.Git.CurrentHeadSHA()
	defaultBranch := "main"
	if e.Config != nil && e.Config.Project.DefaultBranch != "" {
		defaultBranch = e.Config.Project.DefaultBranch
	}

	// Calculate merge-base if comparing to default branch
	baseSHA, _ := e.Git.MergeBase(defaultBranch, branch)
	if baseSHA == "" {
		baseSHA = headSHA
	}

	// Fetch commits
	commits, _ := e.Git.CommitsBetween(defaultBranch, branch, 50)
	if len(commits) == 0 && branch == defaultBranch {
		// If on main itself, show recent commits
		commits, _ = e.Git.CommitsBetween("", branch, 15)
	}

	// Calculate diff totals
	var totalIns, totalDel int
	filesSeen := make(map[string]bool)
	for _, c := range commits {
		totalIns += c.Insertions
		totalDel += c.Deletions
		for _, f := range c.ChangedFiles {
			filesSeen[f] = true
		}
	}

	// Fetch task details if linked
	var task *core.Task
	if taskID != "" && e.Store != nil {
		task, _ = e.Store.GetTask(taskID)
	}

	// Fetch evidence & checkpoints
	var evidenceList []core.Evidence
	var checkpoints []core.Checkpoint
	if e.Store != nil {
		evidenceList, _ = e.Store.ListEvidence(branch, 20)
		checkpoints, _ = e.Store.ListCheckpoints(taskID, 20)
	}

	// Check working tree
	_, _, _, clean, _ := e.Git.WorkingTreeSummary()
	wtStatus := ""
	if !clean {
		wtStatus = "Uncommitted Changes In Working Tree"
	}

	var warnings []string
	if len(evidenceList) == 0 {
		warnings = append(warnings, "No test or build evidence recorded for this branch yet.")
	} else {
		// Freshness check
		latestEv := evidenceList[0]
		if latestEv.CommitSHA != headSHA {
			warnings = append(warnings, fmt.Sprintf("Latest test evidence is against older commit %s (HEAD is %s). Rerun tests for verified evidence.", truncate(latestEv.CommitSHA, 8), truncate(headSHA, 8)))
		}
	}

	projectName := "Project"
	if e.Config != nil && e.Config.Project.Name != "" {
		projectName = e.Config.Project.Name
	}

	data := ReportData{
		Title:           fmt.Sprintf("Activity Report: %s", branch),
		ReportType:      "Activity Report",
		ProjectName:     projectName,
		GeneratedAt:     time.Now().UTC(),
		BaseRef:         defaultBranch,
		HeadRef:         branch,
		BaseSHA:         baseSHA,
		HeadSHA:         headSHA,
		Branch:          branch,
		Task:            task,
		Commits:         commits,
		TotalInsertions: totalIns,
		TotalDeletions:  totalDel,
		TotalFiles:      len(filesSeen),
		Evidence:        evidenceList,
		Checkpoints:     checkpoints,
		WorkingTree:     wtStatus,
		Warnings:        warnings,
	}

	// Render HTML
	tmpl, err := GetReportTemplate()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, "", fmt.Errorf("failed to render HTML: %w", err)
	}

	// Safe filesystem ID
	safeBranchName := sanitizeFilename(branch)
	reportsDir := filepath.Join(e.RepoDir, config.DefaultDirName, "reports", "activity", safeBranchName)
	if err := os.MkdirAll(filepath.Join(reportsDir, "snapshots"), 0755); err != nil {
		return nil, "", err
	}

	latestPath := filepath.Join(reportsDir, "latest.html")
	if err := os.WriteFile(latestPath, buf.Bytes(), 0644); err != nil {
		return nil, "", err
	}

	// Record report entry in DB
	reportRecord := &core.Report{
		ID:             uuid.NewString(),
		ProjectID:      "default",
		ReportType:     core.ReportTypeActivity,
		Title:          data.Title,
		Status:         "generated",
		BaseRef:        defaultBranch,
		HeadRef:        branch,
		BaseSHA:        baseSHA,
		HeadSHA:        headSHA,
		TaskID:         taskID,
		FilePath:       latestPath,
		ProfileVersion: 1,
		CreatedAt:      time.Now().UTC(),
	}
	if e.Store != nil {
		if err := e.Store.CreateReport(reportRecord); err != nil {
			return nil, "", fmt.Errorf("failed to store report: %w", err)
		}
	}

	// If snapshot is requested or snapshotReason is provided
	if snapshotReason != "" && e.Store != nil {
		existingSnapshots, _ := e.Store.ListSnapshots(reportRecord.ID)
		snapNum := len(existingSnapshots) + 1
		snapPath := filepath.Join(reportsDir, "snapshots", fmt.Sprintf("snapshot_%03d.html", snapNum))
		_ = os.WriteFile(snapPath, buf.Bytes(), 0644)

		if err := e.Store.CreateReportSnapshot(&core.ReportSnapshot{
			ID:             uuid.NewString(),
			ReportID:       reportRecord.ID,
			SnapshotNumber: snapNum,
			TriggerReason:  snapshotReason,
			HeadSHA:        headSHA,
			FilePath:       snapPath,
			CreatedAt:      time.Now().UTC(),
		}); err != nil {
			return nil, "", fmt.Errorf("failed to store snapshot: %w", err)
		}
	}

	return reportRecord, latestPath, nil
}

// GenerateReleaseReport builds an Integration or Versioned Release report.
func (e *ReportEngine) GenerateReleaseReport(fromRef, toRef, targetBranch string) (*core.Report, string, error) {
	isVersioned := fromRef != "" && toRef != ""
	reportType := core.ReportTypeReleaseIntegration
	title := "Integration Report"

	if isVersioned {
		reportType = core.ReportTypeReleaseVersioned
		title = fmt.Sprintf("Release Report: %s → %s", fromRef, toRef)
	} else {
		if targetBranch == "" {
			targetBranch = "main"
		}
		title = fmt.Sprintf("Target Integration Report: %s", targetBranch)
		toRef = targetBranch
	}

	headSHA, _ := e.Git.CurrentHeadSHA()
	commits, _ := e.Git.CommitsBetween(fromRef, toRef, 100)

	var totalIns, totalDel int
	filesSeen := make(map[string]bool)
	for _, c := range commits {
		totalIns += c.Insertions
		totalDel += c.Deletions
		for _, f := range c.ChangedFiles {
			filesSeen[f] = true
		}
	}

	var evidenceList []core.Evidence
	if e.Store != nil {
		evidenceList, _ = e.Store.ListEvidence(toRef, 20)
	}

	projectName := "Project"
	if e.Config != nil && e.Config.Project.Name != "" {
		projectName = e.Config.Project.Name
	}

	data := ReportData{
		Title:           title,
		ReportType:      title,
		ProjectName:     projectName,
		GeneratedAt:     time.Now().UTC(),
		BaseRef:         fromRef,
		HeadRef:         toRef,
		BaseSHA:         fromRef,
		HeadSHA:         headSHA,
		Branch:          toRef,
		Commits:         commits,
		TotalInsertions: totalIns,
		TotalDeletions:  totalDel,
		TotalFiles:      len(filesSeen),
		Evidence:        evidenceList,
	}

	tmpl, err := GetReportTemplate()
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, "", err
	}

	reportsDir := filepath.Join(e.RepoDir, config.DefaultDirName, "reports", "release")
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		return nil, "", err
	}

	reportID := uuid.NewString()
	fileName := fmt.Sprintf("release_%s_%d.html", sanitizeFilename(toRef), time.Now().Unix())
	filePath := filepath.Join(reportsDir, fileName)

	if err := os.WriteFile(filePath, buf.Bytes(), 0644); err != nil {
		return nil, "", err
	}

	reportRecord := &core.Report{
		ID:             reportID,
		ProjectID:      "default",
		ReportType:     reportType,
		Title:          title,
		Status:         "generated",
		BaseRef:        fromRef,
		HeadRef:        toRef,
		HeadSHA:        headSHA,
		FilePath:       filePath,
		ProfileVersion: 1,
		CreatedAt:      time.Now().UTC(),
	}
	if e.Store != nil {
		_ = e.Store.CreateReport(reportRecord)
	}

	return reportRecord, filePath, nil
}

func sanitizeFilename(s string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "_")
	return r.Replace(s)
}

func truncate(s string, l int) string {
	if len(s) > l {
		return s[:l]
	}
	return s
}
