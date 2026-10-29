package services

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/google/uuid"
)

// TaskLinkService connects commits to the task they delivered.
//
// task_links has always supported a "commit" link type and ListTasks already
// reads it back into Task.Commits, but nothing ever wrote one, so Task.Commits
// was permanently empty and the board and the change log knew nothing about
// each other. This service fills that in, retroactively, from git.
type TaskLinkService struct {
	Store         StorageInterface
	Git           *collector.GitCollector
	DefaultBranch string
}

func NewTaskLinkService(store StorageInterface, git *collector.GitCollector, defaultBranch string) *TaskLinkService {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	return &TaskLinkService{Store: store, Git: git, DefaultBranch: defaultBranch}
}

// taskRefRe finds an explicit task reference such as DT-0042 in a commit
// message. Word boundaries stop it matching inside a longer token.
var taskRefRe = regexp.MustCompile(`(?i)\b(DT-\d{1,6})\b`)

// LinkResult summarises what a sync did.
type LinkResult struct {
	CommitsScanned int            `json:"commits_scanned"`
	Linked         int            `json:"linked"`
	ByReference    int            `json:"by_reference"`
	ByBranch       int            `json:"by_branch"`
	PerTask        map[string]int `json:"per_task"`
	SkippedNoTask  int            `json:"skipped_no_task"`
}

// ReferencedTasks returns the task IDs a commit message mentions, uppercased
// and deduplicated.
func ReferencedTasks(message string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range taskRefRe.FindAllStringSubmatch(message, -1) {
		id := strings.ToUpper(m[1])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// Sync scans recent history and links commits to tasks.
//
// Two signals, in priority order:
//
//  1. An explicit "DT-1234" reference in the commit message. Deliberate and
//     unambiguous, so it always wins.
//  2. Membership of a feature branch linked to the task. Only branches other
//     than the default are considered, and only commits not already reachable
//     from the default branch — linking a task to every commit on main would
//     say nothing about which commits are actually its.
func (s *TaskLinkService) Sync(limit int) (*LinkResult, error) {
	if limit <= 0 {
		limit = 500
	}

	tasks, err := s.Store.ListTasks("default")
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	if len(tasks) == 0 {
		return &LinkResult{PerTask: map[string]int{}}, nil
	}

	known := map[string]bool{}
	for _, t := range tasks {
		known[strings.ToUpper(t.ID)] = true
	}

	res := &LinkResult{PerTask: map[string]int{}}
	// linked guards against double-counting a commit claimed by both signals.
	linked := map[string]bool{}

	link := func(taskID, sha string, byReference bool) {
		key := taskID + "|" + sha
		if linked[key] {
			return
		}
		// LinkTask is backed by a UNIQUE triple, so re-running is harmless.
		if err := s.Store.LinkTask(taskID, "commit", sha); err != nil {
			return
		}
		linked[key] = true
		res.Linked++
		res.PerTask[taskID]++
		if byReference {
			res.ByReference++
		} else {
			res.ByBranch++
		}
	}

	// 1. Explicit references anywhere in recent history.
	commits, err := s.Git.LogSince("--all", time.Time{}, limit)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	res.CommitsScanned = len(commits)

	for _, c := range commits {
		refs := ReferencedTasks(c.Subject + "\n" + c.Message)
		if len(refs) == 0 {
			res.SkippedNoTask++
			continue
		}
		for _, id := range refs {
			if !known[id] {
				continue // a reference to a task this project does not have
			}
			link(id, c.SHA, true)
		}
	}

	// 2. Feature-branch membership.
	for _, t := range tasks {
		for _, branch := range t.Branches {
			if branch == "" || branch == s.DefaultBranch {
				continue
			}
			onBranch, err := s.Git.CommitsBetween(s.DefaultBranch, branch, limit)
			if err != nil {
				continue // branch may have been deleted after the merge
			}
			for _, c := range onBranch {
				link(t.ID, c.SHA, false)
			}
		}
	}

	if res.Linked > 0 {
		_ = s.Store.InsertEvent(&core.Event{
			ID:               uuid.NewString(),
			ProjectID:        "default",
			EventType:        "task_commits_linked",
			Source:           "cli",
			AttributionLevel: core.AttributionInferred,
			PayloadJSON: payload(map[string]interface{}{
				"linked":       res.Linked,
				"by_reference": res.ByReference,
				"by_branch":    res.ByBranch,
			}),
			OccurredAt: time.Now().UTC(),
			ObservedAt: time.Now().UTC(),
		})
	}

	return res, nil
}

// CommitIndex maps a commit SHA to the task that delivered it.
type CommitIndex map[string]TaskRef

// TaskRef is the minimum a change needs to show which task it belongs to.
type TaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// BuildCommitIndex returns a SHA -> task lookup from the stored links.
func BuildCommitIndex(store StorageInterface) (CommitIndex, error) {
	tasks, err := store.ListTasks("default")
	if err != nil {
		return nil, err
	}
	idx := CommitIndex{}
	for _, t := range tasks {
		ref := TaskRef{ID: t.ID, Title: t.Title, Status: t.Status}
		for _, sha := range t.Commits {
			// First task wins; a commit delivering two tasks is rare and the
			// change log only has room to show one.
			if _, exists := idx[sha]; !exists {
				idx[sha] = ref
			}
		}
	}
	return idx, nil
}
