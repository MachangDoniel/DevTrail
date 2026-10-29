package services

import (
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/google/uuid"
)

type EvidenceService struct {
	Store StorageInterface
	Git   *collector.GitCollector
}

func NewEvidenceService(store StorageInterface, git *collector.GitCollector) *EvidenceService {
	return &EvidenceService{Store: store, Git: git}
}

// Record attaches test or build evidence to the current Git commit.
func (s *EvidenceService) Record(evType, result, command, commitSHA, branch string, durationMs int64, logPath string) (*core.Evidence, error) {
	if branch == "" && s.Git != nil {
		if b, err := s.Git.CurrentBranch(); err == nil {
			branch = b
		}
	}
	if commitSHA == "" || commitSHA == "HEAD" {
		if s.Git != nil {
			if sha, err := s.Git.CurrentHeadSHA(); err == nil {
				commitSHA = sha
			}
		}
	}

	ev := &core.Evidence{
		ID:              uuid.NewString(),
		ProjectID:       "default",
		EvidenceType:    evType,
		Result:          result,
		Command:         command,
		DurationMs:      durationMs,
		CommitSHA:       commitSHA,
		Branch:          branch,
		LogArtifactPath: logPath,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.Store.CreateEvidence(ev); err != nil {
		return nil, err
	}

	_ = s.Store.InsertEvent(&core.Event{
		ID:               uuid.NewString(),
		ProjectID:        "default",
		EventType:        "evidence_recorded",
		Source:           "cli",
		Branch:           branch,
		CommitSHA:        commitSHA,
		AttributionLevel: core.AttributionVerified,
		PayloadJSON: payload(map[string]interface{}{
			"evidence_id": ev.ID, "type": ev.EvidenceType,
			"result": ev.Result, "duration_ms": ev.DurationMs,
		}),
		OccurredAt: time.Now().UTC(),
		ObservedAt: time.Now().UTC(),
	})

	return ev, nil
}

// CheckFreshness checks if the latest evidence matches the current HEAD commit.
func (s *EvidenceService) CheckFreshness(latestEvidenceCommitSHA string) (isFresh bool, currentHeadSHA string, err error) {
	if s.Git == nil {
		return true, "", nil
	}
	headSHA, err := s.Git.CurrentHeadSHA()
	if err != nil {
		return false, "", err
	}
	if latestEvidenceCommitSHA == "" {
		return false, headSHA, nil
	}
	return latestEvidenceCommitSHA == headSHA, headSHA, nil
}
