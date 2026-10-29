package services

import (
	"sort"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/core"
)

// MetricsService derives dashboard metrics from local git history and the
// event store. Everything it returns is computed from data DevTrail already
// holds — it makes no network calls and invents no values.
type MetricsService struct {
	Store StorageInterface
	Git   *collector.GitCollector
}

func NewMetricsService(store StorageInterface, git *collector.GitCollector) *MetricsService {
	return &MetricsService{Store: store, Git: git}
}

// DayBucket is one cell of the activity heatmap.
type DayBucket struct {
	Date    string `json:"date"` // YYYY-MM-DD
	Commits int    `json:"commits"`
	Events  int    `json:"events"`
}

// WeekBucket is one point of the commit velocity series.
type WeekBucket struct {
	WeekStart string `json:"week_start"` // YYYY-MM-DD (Monday)
	Commits   int    `json:"commits"`
}

// Churn summarises line and file movement over the window.
type Churn struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Files   int `json:"files"`
}

// Metrics is the payload returned by GET /api/metrics.
type Metrics struct {
	WindowDays       int            `json:"window_days"`
	RangeStart       string         `json:"range_start"`
	RangeEnd         string         `json:"range_end"`
	Heatmap          []DayBucket    `json:"heatmap"`
	TotalCommits     int            `json:"total_commits"`
	TotalEvents      int            `json:"total_events"`
	ActiveDays       int            `json:"active_days"`
	MaxDayCount      int            `json:"max_day_count"`
	CommitsByWeek    []WeekBucket   `json:"commits_by_week"`
	TasksByStatus    map[string]int `json:"tasks_by_status"`
	EvidencePassRate *float64       `json:"evidence_pass_rate"`
	EvidenceTotal    int            `json:"evidence_total"`
	CheckpointCount  int            `json:"checkpoint_count"`
	Contributors     int            `json:"contributors"`
	Churn            Churn          `json:"churn"`
}

const dayFormat = "2006-01-02"

// Collect gathers metrics over the trailing windowDays, ending today.
func (s *MetricsService) Collect(windowDays int) (*Metrics, error) {
	if windowDays <= 0 {
		windowDays = 365
	}

	now := time.Now()
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := end.AddDate(0, 0, -(windowDays - 1))

	m := &Metrics{
		WindowDays:    windowDays,
		RangeStart:    start.Format(dayFormat),
		RangeEnd:      end.Format(dayFormat),
		TasksByStatus: map[string]int{},
		Heatmap:       []DayBucket{},
		CommitsByWeek: []WeekBucket{},
	}

	commitsPerDay := map[string]int{}
	eventsPerDay := map[string]int{}

	// Commits. The window may hold far more than the graph view's cap, so ask
	// for a generous limit and filter by date here.
	if s.Git != nil {
		commits, err := s.Git.CommitsBetween("", "", windowDays*20)
		if err == nil {
			authors := map[string]bool{}
			files := map[string]bool{}
			for _, c := range commits {
				day := c.Timestamp.Local()
				if day.Before(start) {
					continue
				}
				commitsPerDay[day.Format(dayFormat)]++
				m.TotalCommits++
				m.Churn.Added += c.Insertions
				m.Churn.Deleted += c.Deletions
				if c.AuthorEmail != "" {
					authors[c.AuthorEmail] = true
				}
				for _, f := range c.ChangedFiles {
					files[f] = true
				}
			}
			m.Contributors = len(authors)
			m.Churn.Files = len(files)
		}
	}

	// Events from the local store.
	if s.Store != nil {
		if events, err := s.Store.ListEvents("", "", windowDays*20); err == nil {
			for _, e := range events {
				at := e.OccurredAt.Local()
				if at.Before(start) {
					continue
				}
				eventsPerDay[at.Format(dayFormat)]++
				m.TotalEvents++
			}
		}

		if tasks, err := s.Store.ListTasks("default"); err == nil {
			for _, t := range tasks {
				status := t.Status
				if status == "" {
					status = core.TaskStatusPlanned
				}
				m.TasksByStatus[status]++
			}
		}

		if evidence, err := s.Store.ListEvidence("", 500); err == nil {
			passed := 0
			for _, e := range evidence {
				if e.Result == core.EvidenceResultPassed {
					passed++
				}
			}
			m.EvidenceTotal = len(evidence)
			if len(evidence) > 0 {
				rate := float64(passed) / float64(len(evidence))
				m.EvidencePassRate = &rate
			}
		}

		if cps, err := s.Store.ListCheckpoints("", 500); err == nil {
			m.CheckpointCount = len(cps)
		}
	}

	// One bucket per day across the whole window, so the heatmap has no gaps.
	weekTotals := map[string]int{}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		key := d.Format(dayFormat)
		bucket := DayBucket{Date: key, Commits: commitsPerDay[key], Events: eventsPerDay[key]}
		m.Heatmap = append(m.Heatmap, bucket)

		total := bucket.Commits + bucket.Events
		if total > 0 {
			m.ActiveDays++
		}
		if total > m.MaxDayCount {
			m.MaxDayCount = total
		}
		weekTotals[weekStart(d).Format(dayFormat)] += bucket.Commits
	}

	keys := make([]string, 0, len(weekTotals))
	for k := range weekTotals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.CommitsByWeek = append(m.CommitsByWeek, WeekBucket{WeekStart: k, Commits: weekTotals[k]})
	}

	return m, nil
}

// weekStart returns the Monday of the week containing t.
func weekStart(t time.Time) time.Time {
	offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
	return t.AddDate(0, 0, -offset)
}
