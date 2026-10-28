package services

import (
	"fmt"
	"strings"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/core"
)

type HandoffService struct {
	Store StorageInterface
	Git   *collector.GitCollector
}

func NewHandoffService(store StorageInterface, git *collector.GitCollector) *HandoffService {
	return &HandoffService{Store: store, Git: git}
}

// GeneratePackage builds a verified, evidence-backed prompt context bundle.
func (h *HandoffService) GeneratePackage(taskID string) (*core.HandoffPackage, error) {
	task, err := h.Store.GetTask(taskID)
	if err != nil {
		return nil, fmt.Errorf("task %s not found: %w", taskID, err)
	}

	currBranch := "main"
	headSHA := ""
	if h.Git != nil {
		if b, err := h.Git.CurrentBranch(); err == nil && b != "" {
			currBranch = b
		}
		if s, err := h.Git.CurrentHeadSHA(); err == nil {
			headSHA = s
		}
	}

	// Fetch commits
	var commits []core.Commit
	if h.Git != nil {
		commits, _ = h.Git.CommitsBetween("", currBranch, 10)
	}

	// Fetch evidence
	var latestEv *core.Evidence
	if evList, err := h.Store.ListEvidence(currBranch, 5); err == nil && len(evList) > 0 {
		latestEv = &evList[0]
	}

	// Fetch checkpoints
	checkpoints, _ := h.Store.ListCheckpoints(taskID, 10)

	// Build the markdown prompt
	var sb strings.Builder
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString(fmt.Sprintf("🤖 DEVIP-AI CONTEXT HANDOFF: [%s] %s\n", task.ID, task.Title))
	sb.WriteString(fmt.Sprintf("Generated: %s | Source: DevTrail Intelligence\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	sb.WriteString("### 1. Task Objective & State\n")
	sb.WriteString(fmt.Sprintf("- **Task ID:** %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("- **Status:** %s\n", task.Status))
	sb.WriteString(fmt.Sprintf("- **AI Provider Context:** %s\n", task.AIProvider))
	if task.Objective != "" {
		sb.WriteString(fmt.Sprintf("- **Objective:** %s\n", task.Objective))
	}
	if task.Notes != "" {
		sb.WriteString(fmt.Sprintf("- **Current Notes:**\n%s\n", task.Notes))
	}
	sb.WriteString("\n")

	sb.WriteString("### 2. Git Environment & Evidence\n")
	sb.WriteString(fmt.Sprintf("- **Active Branch:** `%s`\n", currBranch))
	sb.WriteString(fmt.Sprintf("- **HEAD Commit:** `%s`\n", headSHA))
	if latestEv != nil {
		sb.WriteString(fmt.Sprintf("- **Latest Test Verification:** [%s] `%s` (%dms)\n",
			strings.ToUpper(latestEv.Result), latestEv.Command, latestEv.DurationMs))
	} else {
		sb.WriteString("- **Latest Test Verification:** No automated test evidence recorded.\n")
	}
	sb.WriteString("\n")

	if len(checkpoints) > 0 {
		sb.WriteString("### 3. Checkpoints & Milestones\n")
		for _, cp := range checkpoints {
			sb.WriteString(fmt.Sprintf("- [%s] %s (Commit: `%s`)\n",
				cp.CreatedAt.Format("15:04:05"), cp.Message, truncate(cp.CommitSHA, 7)))
		}
		sb.WriteString("\n")
	}

	if len(commits) > 0 {
		sb.WriteString("### 4. Recent Commits on Branch\n")
		for _, c := range commits {
			sb.WriteString(fmt.Sprintf("- `%s` %s (%s, %s)\n",
				c.ShortSHA, c.Subject, c.AuthorName, c.Timestamp.Format("2006-01-02 15:04")))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### 5. Instruction for Incoming AI Agent\n")
	sb.WriteString("You are continuing work on this exact task. The developer has transitioned this session to you.\n")
	sb.WriteString("Review the objectives, recent commits, and test verification above. Do not rewrite existing verified work.\n")
	sb.WriteString("Ask clarifying questions if anything is ambiguous, or proceed with the next unfinished step.\n")
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	pkg := &core.HandoffPackage{
		TaskID:          task.ID,
		Title:           task.Title,
		Status:          task.Status,
		Objective:       task.Objective,
		Notes:           task.Notes,
		CurrentBranch:   currBranch,
		HeadSHA:         headSHA,
		Commits:         commits,
		LatestEvidence:  latestEv,
		Checkpoints:     checkpoints,
		FormattedPrompt: sb.String(),
	}

	return pkg, nil
}

func truncate(s string, l int) string {
	if len(s) > l {
		return s[:l]
	}
	return s
}
