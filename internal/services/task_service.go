package services

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/google/uuid"
)

type TaskService struct {
	Store StorageInterface
	Git   *collector.GitCollector
}

func NewTaskService(store StorageInterface, git *collector.GitCollector) *TaskService {
	return &TaskService{Store: store, Git: git}
}

// payload marshals an event payload. Values are escaped properly, so a note or
// title containing quotes or newlines cannot produce invalid JSON.
func payload(fields map[string]interface{}) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// CreateTask creates a new task or feature item.
func (s *TaskService) CreateTask(title, taskType, objective, aiProvider string, autoAttachCurrentBranch bool) (*core.Task, error) {
	if taskType == "" {
		taskType = "feature"
	}
	if aiProvider == "" {
		aiProvider = "claude"
	}
	task := &core.Task{
		ID:         fmt.Sprintf("DT-%04d", time.Now().Unix()%10000),
		ProjectID:  "default",
		Title:      title,
		TaskType:   taskType,
		Status:     core.TaskStatusPlanned,
		Objective:  objective,
		AIProvider: aiProvider,
	}

	if autoAttachCurrentBranch && s.Git != nil {
		if branch, err := s.Git.CurrentBranch(); err == nil && branch != "" {
			task.Branches = []string{branch}
		}
	}

	if err := s.Store.CreateTask(task); err != nil {
		return nil, err
	}

	// Link branch if attached
	for _, br := range task.Branches {
		_ = s.Store.LinkTask(task.ID, "branch", br)
	}

	// Log event
	_ = s.Store.InsertEvent(&core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "task_created",
		Source:           "cli",
		AttributionLevel: core.AttributionExplicit,
		PayloadJSON:      payload(map[string]interface{}{"task_id": task.ID, "title": task.Title}),
		OccurredAt:       time.Now().UTC(),
		ObservedAt:       time.Now().UTC(),
	})

	return task, nil
}

// AttachBranch associates a branch with a task.
func (s *TaskService) AttachBranch(taskID, branch string) error {
	if branch == "" && s.Git != nil {
		var err error
		branch, err = s.Git.CurrentBranch()
		if err != nil {
			return err
		}
	}
	if err := s.Store.LinkTask(taskID, "branch", branch); err != nil {
		return err
	}

	_ = s.Store.InsertEvent(&core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "task_attached_branch",
		Source:           "cli",
		Branch:           branch,
		AttributionLevel: core.AttributionExplicit,
		PayloadJSON:      payload(map[string]interface{}{"task_id": taskID, "branch": branch}),
		OccurredAt:       time.Now().UTC(),
		ObservedAt:       time.Now().UTC(),
	})
	return nil
}

// AddNote adds a development note to a task.
func (s *TaskService) AddNote(taskID, note string) error {
	if err := s.Store.AddTaskNote(taskID, note); err != nil {
		return err
	}
	_ = s.Store.InsertEvent(&core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "task_note_added",
		Source:           "cli",
		AttributionLevel: core.AttributionExplicit,
		PayloadJSON:      payload(map[string]interface{}{"task_id": taskID, "note": note}),
		OccurredAt:       time.Now().UTC(),
		ObservedAt:       time.Now().UTC(),
	})
	return nil
}

// RecordCheckpoint creates a snapshot milestone.
func (s *TaskService) RecordCheckpoint(taskID, message string) (*core.Checkpoint, error) {
	currBranch := "main"
	headSHA := ""
	if s.Git != nil {
		if b, err := s.Git.CurrentBranch(); err == nil {
			currBranch = b
		}
		if sha, err := s.Git.CurrentHeadSHA(); err == nil {
			headSHA = sha
		}
	}

	cp := &core.Checkpoint{
		ID:        uuid.NewString(),
		ProjectID: "default",
		TaskID:    taskID,
		Branch:    currBranch,
		CommitSHA: headSHA,
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}

	if err := s.Store.CreateCheckpoint(cp); err != nil {
		return nil, err
	}

	_ = s.Store.InsertEvent(&core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "checkpoint",
		Source:           "cli",
		Branch:           currBranch,
		CommitSHA:        headSHA,
		AttributionLevel: core.AttributionExplicit,
		PayloadJSON:      payload(map[string]interface{}{"task_id": taskID, "message": message}),
		OccurredAt:       time.Now().UTC(),
		ObservedAt:       time.Now().UTC(),
	})

	return cp, nil
}
