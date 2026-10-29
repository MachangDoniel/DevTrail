package services

import (
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/google/uuid"
)

// ReleaseRunner is the slice of the report engine triggers need.
type ReleaseRunner interface {
	GenerateReleaseReport(fromRef, toRef, targetBranch string) (*core.Report, string, error)
	GenerateActivityReport(branch, taskID, snapshotReason string) (*core.Report, string, error)
}

// TriggerService reacts to things happening in the repository.
//
// reports.triggers has been in config.yaml since the first release and the
// triggers table alongside it, but nothing ever evaluated them: a configured
// "generate a release report when I merge to main" silently did nothing.
//
// DevTrail is local-first with no git hooks installed, so this observes git
// state on a timer rather than being pushed to. That makes idempotency the
// central concern — a poll must not regenerate the same report every tick.
// Each candidate gets a stable key, recorded as an event, and the key is
// checked before firing.
type TriggerService struct {
	Store   StorageInterface
	Git     *collector.GitCollector
	Config  *config.Config
	Reports ReleaseRunner

	Now func() time.Time

	mu      sync.Mutex
	running bool
}

func NewTriggerService(store StorageInterface, git *collector.GitCollector, cfg *config.Config, reports ReleaseRunner) *TriggerService {
	return &TriggerService{Store: store, Git: git, Config: cfg, Reports: reports, Now: time.Now}
}

// EventPattern renders a config trigger as the pattern stored on the row.
func EventPattern(t config.TriggerConfig) string {
	switch strings.ToLower(t.Event) {
	case "merge":
		target := t.Target
		if target == "" {
			target = "main"
		}
		return "merge:" + target
	case "release_tag", "tag":
		target := t.Target
		if target == "" {
			target = "v*"
		}
		return "tag:" + target
	case "post-commit", "commit":
		target := t.Target
		if target == "" {
			target = "*"
		}
		return "commit:" + target
	default:
		return strings.ToLower(t.Event)
	}
}

// Sync writes the triggers declared in config.yaml into the database.
func (s *TriggerService) Sync() error {
	for _, tc := range s.Config.Reports.Triggers {
		row := &core.Trigger{
			ProjectID:       "default",
			Name:            tc.Name,
			EventPattern:    EventPattern(tc),
			ReportType:      tc.ReportType,
			DebounceSeconds: tc.DebounceSeconds,
			IsEnabled:       tc.Enabled,
		}
		if err := s.Store.UpsertTrigger(row); err != nil {
			return fmt.Errorf("sync trigger %q: %w", tc.Name, err)
		}
	}
	return nil
}

// Candidate is something that happened and may deserve a report.
type Candidate struct {
	// Key is stable for the underlying event, so it can be fired exactly once.
	Key string
	// Ref is the tag or branch involved; SHA is the commit.
	Ref string
	SHA string
	// From is the previous tag, for a versioned release.
	From string
}

// Evaluate returns what a trigger would act on right now, without firing.
func (s *TriggerService) Evaluate(tc config.TriggerConfig) ([]Candidate, error) {
	pattern := EventPattern(tc)
	kind, target, _ := strings.Cut(pattern, ":")

	switch kind {
	case "merge":
		commits, err := s.Git.LogSince(target, time.Time{}, 50)
		if err != nil {
			// The branch may not exist in this checkout.
			return nil, nil
		}
		var out []Candidate
		for _, c := range commits {
			if len(c.ParentSHAs) < 2 {
				continue // not a merge
			}
			out = append(out, Candidate{
				Key: fmt.Sprintf("trigger:%s:%s", tc.Name, c.SHA),
				Ref: target, SHA: c.SHA,
			})
		}
		return out, nil

	case "tag":
		tags, err := s.Git.ListTags()
		if err != nil {
			return nil, err
		}
		var matched []core.GitRef
		for _, t := range tags {
			if ok, _ := path.Match(target, t.RefName); ok {
				matched = append(matched, t)
			}
		}
		var out []Candidate
		for _, t := range matched {
			from, _ := s.Git.PreviousTag(t.RefName)
			out = append(out, Candidate{
				Key: fmt.Sprintf("trigger:%s:%s", tc.Name, t.RefName),
				Ref: t.RefName, SHA: t.TargetSHA, From: from,
			})
		}
		return out, nil

	case "commit":
		sha, err := s.Git.CurrentHeadSHA()
		if err != nil || sha == "" {
			return nil, nil
		}
		branch, _ := s.Git.CurrentBranch()
		if target != "*" && branch != target {
			return nil, nil
		}
		return []Candidate{{
			Key: fmt.Sprintf("trigger:%s:%s", tc.Name, sha),
			Ref: branch, SHA: sha,
		}}, nil
	}

	return nil, nil
}

// Tick evaluates every enabled trigger and fires those with new work.
func (s *TriggerService) Tick() []string {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	now := s.Now()
	stored, err := s.Store.ListTriggers()
	if err != nil {
		log.Printf("devtrail triggers: list: %v", err)
		return nil
	}
	byName := map[string]core.Trigger{}
	for _, row := range stored {
		byName[row.Name] = row
	}

	var fired []string
	for _, tc := range s.Config.Reports.Triggers {
		if !tc.Enabled {
			continue
		}
		row, ok := byName[tc.Name]
		if !ok {
			continue
		}
		// Debounce: a burst of merges should not produce a burst of reports.
		if row.LastTriggeredAt != nil && tc.DebounceSeconds > 0 {
			if now.Sub(*row.LastTriggeredAt) < time.Duration(tc.DebounceSeconds)*time.Second {
				continue
			}
		}

		candidates, err := s.Evaluate(tc)
		if err != nil {
			log.Printf("devtrail triggers: %s: %v", tc.Name, err)
			continue
		}

		for _, cand := range candidates {
			seen, err := s.Store.HasEventWithKey(cand.Key)
			if err != nil || seen {
				continue
			}
			if err := s.fire(tc, cand, now); err != nil {
				log.Printf("devtrail triggers: %s failed: %v", tc.Name, err)
				continue
			}
			fired = append(fired, fmt.Sprintf("%s (%s)", tc.Name, cand.Ref))

			if err := s.Store.MarkTriggerFired(tc.Name, now); err != nil {
				log.Printf("devtrail triggers: %s: %v", tc.Name, err)
			}
			break // one report per trigger per tick; the rest wait
		}
	}
	return fired
}

// fire generates the report and records the key so it cannot run twice.
func (s *TriggerService) fire(tc config.TriggerConfig, cand Candidate, now time.Time) error {
	var err error
	// from is the lower bound actually used, which the recorded event reports
	// so the log matches what was generated.
	from := cand.From

	switch strings.ToLower(tc.ReportType) {
	case "release":
		if strings.EqualFold(tc.Mode, "versioned_release") && cand.Ref != "" {
			if from == "" {
				// The first tag in a repository has no predecessor. Bound the
				// release from the root commit so it covers everything up to
				// the tag; leaving it empty would make the engine fall back to
				// an integration report named after the branch, which is not
				// what a tag trigger was asked for.
				if root, rerr := s.Git.RootCommit(); rerr == nil {
					from = root
				}
			}
			_, _, err = s.Reports.GenerateReleaseReport(from, cand.Ref, "")
		} else {
			from = ""
			_, _, err = s.Reports.GenerateReleaseReport("", "", cand.Ref)
		}
	default:
		_, _, err = s.Reports.GenerateActivityReport("", "", "triggered:"+tc.Name)
	}
	if err != nil {
		return err
	}

	// Record the firing. The idempotency key is what stops a later tick
	// generating the same report again.
	return s.Store.InsertEvent(&core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "trigger_fired",
		Source:           "cli",
		IdempotencyKey:   cand.Key,
		Branch:           cand.Ref,
		CommitSHA:        cand.SHA,
		AttributionLevel: core.AttributionVerified,
		PayloadJSON: payload(map[string]interface{}{
			"trigger": tc.Name, "pattern": EventPattern(tc),
			"ref": cand.Ref, "from": from,
		}),
		OccurredAt: now.UTC(),
		ObservedAt: now.UTC(),
	})
}

// Start syncs triggers and then evaluates them until ctx is cancelled.
func (s *TriggerService) Start(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if err := s.Sync(); err != nil {
		log.Printf("devtrail triggers: %v", err)
		return
	}

	enabled := 0
	for _, tc := range s.Config.Reports.Triggers {
		if tc.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return
	}
	log.Printf("devtrail triggers: watching %d trigger(s)", enabled)

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if fired := s.Tick(); len(fired) > 0 {
					log.Printf("devtrail triggers: fired %s", strings.Join(fired, ", "))
				}
			}
		}
	}()
}
