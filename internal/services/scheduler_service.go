package services

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
)

// ReportRunner is the subset of the report engine the scheduler needs.
// Keeping it an interface avoids a services -> reports dependency.
type ReportRunner interface {
	GenerateActivityReport(branch, taskID, snapshotReason string) (*core.Report, string, error)
	GenerateDigestReport(digest *Digest, projectName string) (*core.Report, string, error)
}

// SchedulerService runs the schedules declared in config.yaml. Those schedules
// and their database tables have existed since the first release; nothing ever
// executed them, so a configured "weekly-summary" never produced a report.
type SchedulerService struct {
	Store   StorageInterface
	Config  *config.Config
	Reports ReportRunner
	Digest  *DigestService

	// Now is overridable so tests can control the clock.
	Now func() time.Time

	mu      sync.Mutex
	running bool
}

func NewSchedulerService(store StorageInterface, cfg *config.Config, reports ReportRunner, digest *DigestService) *SchedulerService {
	return &SchedulerService{
		Store:   store,
		Config:  cfg,
		Reports: reports,
		Digest:  digest,
		Now:     time.Now,
	}
}

// Sync writes the schedules declared in config.yaml into the database,
// preserving run bookkeeping, and seeds next_run_at for new ones.
func (s *SchedulerService) Sync() error {
	for _, sc := range s.Config.Reports.Schedules {
		next := s.NextRun(sc, s.Now())
		row := &core.Schedule{
			ProjectID:     "default",
			Name:          sc.Name,
			Frequency:     sc.Frequency,
			AtTime:        sc.At,
			DayOfWeek:     sc.Day,
			ReportType:    sc.Type,
			WindowSpec:    sc.Window,
			CatchUpPolicy: sc.CatchUp,
			IsEnabled:     sc.Enabled,
			NextRunAt:     &next,
		}
		if row.CatchUpPolicy == "" {
			row.CatchUpPolicy = "run_once"
		}
		if err := s.Store.UpsertSchedule(row); err != nil {
			return fmt.Errorf("sync schedule %q: %w", sc.Name, err)
		}
	}
	return nil
}

// NextRun computes the next firing time at or after `from` for a schedule.
func (s *SchedulerService) NextRun(sc config.ScheduleConfig, from time.Time) time.Time {
	hour, minute := parseAtTime(sc.At)

	switch strings.ToLower(sc.Frequency) {
	case "interval":
		// window doubles as the interval, e.g. "30m" or "6h".
		if d, err := time.ParseDuration(sc.Window); err == nil && d > 0 {
			return from.Add(d)
		}
		return from.Add(time.Hour)

	case "weekly":
		target := parseWeekday(sc.Day)
		candidate := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, from.Location())
		delta := (int(target) - int(candidate.Weekday()) + 7) % 7
		candidate = candidate.AddDate(0, 0, delta)
		if !candidate.After(from) {
			candidate = candidate.AddDate(0, 0, 7)
		}
		return candidate

	case "monthly":
		candidate := time.Date(from.Year(), from.Month(), 1, hour, minute, 0, 0, from.Location())
		if !candidate.After(from) {
			candidate = candidate.AddDate(0, 1, 0)
		}
		return candidate

	case "yearly":
		candidate := time.Date(from.Year(), time.January, 1, hour, minute, 0, 0, from.Location())
		if !candidate.After(from) {
			candidate = candidate.AddDate(1, 0, 0)
		}
		return candidate

	default: // daily
		candidate := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, from.Location())
		if !candidate.After(from) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate
	}
}

func parseAtTime(at string) (hour, minute int) {
	parts := strings.SplitN(strings.TrimSpace(at), ":", 2)
	if len(parts) == 2 {
		h, errH := strconv.Atoi(parts[0])
		m, errM := strconv.Atoi(parts[1])
		if errH == nil && errM == nil && h >= 0 && h < 24 && m >= 0 && m < 60 {
			return h, m
		}
	}
	return 18, 0
}

func parseWeekday(day string) time.Weekday {
	switch strings.ToLower(strings.TrimSpace(day)) {
	case "sunday", "sun":
		return time.Sunday
	case "monday", "mon":
		return time.Monday
	case "tuesday", "tue":
		return time.Tuesday
	case "wednesday", "wed":
		return time.Wednesday
	case "thursday", "thu":
		return time.Thursday
	case "saturday", "sat":
		return time.Saturday
	default:
		return time.Friday
	}
}

// Tick runs every schedule that is due, and returns the names that fired.
func (s *SchedulerService) Tick() []string {
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
	stored, err := s.Store.ListSchedules()
	if err != nil {
		log.Printf("devtrail scheduler: list schedules: %v", err)
		return nil
	}

	byName := map[string]core.Schedule{}
	for _, row := range stored {
		byName[row.Name] = row
	}

	var fired []string
	for _, sc := range s.Config.Reports.Schedules {
		if !sc.Enabled {
			continue
		}
		row, ok := byName[sc.Name]
		if !ok || row.NextRunAt == nil {
			continue
		}
		if row.NextRunAt.After(now) {
			continue
		}

		// A schedule whose time passed while the server was down is a missed
		// run. "skip" moves on to the next slot without generating anything.
		missedBy := now.Sub(*row.NextRunAt)
		if row.CatchUpPolicy == "skip" && missedBy > 2*time.Hour {
			next := s.NextRun(sc, now)
			if err := s.Store.MarkScheduleRun(sc.Name, now, next); err != nil {
				log.Printf("devtrail scheduler: %s: %v", sc.Name, err)
			}
			continue
		}

		if err := s.run(sc); err != nil {
			log.Printf("devtrail scheduler: %s failed: %v", sc.Name, err)
			continue
		}
		fired = append(fired, sc.Name)

		next := s.NextRun(sc, now)
		if err := s.Store.MarkScheduleRun(sc.Name, now, next); err != nil {
			log.Printf("devtrail scheduler: %s: %v", sc.Name, err)
		}
	}
	return fired
}

// run generates whatever the schedule asks for.
func (s *SchedulerService) run(sc config.ScheduleConfig) error {
	switch strings.ToLower(sc.Type) {
	case "digest":
		group := GroupByWeek
		if g := strings.ToLower(sc.Window); ValidGroupBy(g) {
			group = GroupBy(g)
		}
		digest, err := s.Digest.Build(DigestOptions{
			GroupBy: group,
			Since:   windowStart(sc.Window, s.Now()),
			Sprint: SprintSpec{
				LengthDays: s.Config.SprintLength(),
				Anchor:     s.Config.SprintAnchor(),
				Prefix:     s.Config.SprintPrefix(),
			},
		})
		if err != nil {
			return err
		}
		_, _, err = s.Reports.GenerateDigestReport(digest, s.Config.Project.Name)
		return err

	default: // activity
		_, _, err := s.Reports.GenerateActivityReport("", "", "scheduled:"+sc.Name)
		return err
	}
}

// windowStart turns a config window spec into a lower bound for the digest.
func windowStart(window string, now time.Time) time.Time {
	switch strings.ToLower(window) {
	case "previous_calendar_day", "day":
		return now.AddDate(0, 0, -1)
	case "previous_calendar_week", "week":
		return now.AddDate(0, 0, -7)
	case "previous_calendar_month", "month":
		return now.AddDate(0, -1, 0)
	case "sprint":
		return now.AddDate(0, 0, -28)
	default:
		return time.Time{}
	}
}

// Start syncs schedules and then ticks until ctx is cancelled.
func (s *SchedulerService) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if err := s.Sync(); err != nil {
		log.Printf("devtrail scheduler: %v", err)
		return
	}

	enabled := 0
	for _, sc := range s.Config.Reports.Schedules {
		if sc.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		log.Printf("devtrail scheduler: no enabled schedules; not starting")
		return
	}
	log.Printf("devtrail scheduler: watching %d schedule(s)", enabled)

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if fired := s.Tick(); len(fired) > 0 {
					log.Printf("devtrail scheduler: generated %s", strings.Join(fired, ", "))
				}
			}
		}
	}()
}
