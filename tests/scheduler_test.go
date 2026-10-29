package tests

import (
	"testing"
	"time"

	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/services"
)

// fakeRunner records what the scheduler asked to generate, so the tests can
// assert on firing behaviour without writing report files.
type fakeRunner struct {
	activity int
	digests  int
	fail     bool
}

func (f *fakeRunner) GenerateActivityReport(branch, taskID, reason string) (*core.Report, string, error) {
	if f.fail {
		return nil, "", errFake
	}
	f.activity++
	return &core.Report{ID: "r"}, "/tmp/r.html", nil
}

func (f *fakeRunner) GenerateDigestReport(d *services.Digest, project string) (*core.Report, string, error) {
	if f.fail {
		return nil, "", errFake
	}
	f.digests++
	return &core.Report{ID: "d"}, "/tmp/d.html", nil
}

type fakeErr struct{}

func (fakeErr) Error() string { return "generation failed" }

var errFake = fakeErr{}

func newScheduler(t *testing.T, schedules []config.ScheduleConfig, now time.Time) (*services.SchedulerService, *fakeRunner, func()) {
	t.Helper()
	db, cleanup := newTestStore(t)

	cfg := config.DefaultConfig("sched-test", "main")
	cfg.Reports.Schedules = schedules

	runner := &fakeRunner{}
	s := services.NewSchedulerService(db, cfg, runner, services.NewDigestService(db, nil))
	s.Now = func() time.Time { return now }
	return s, runner, cleanup
}

func TestNextRunDaily(t *testing.T) {
	s, _, cleanup := newScheduler(t, nil, time.Now())
	defer cleanup()

	from := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	got := s.NextRun(config.ScheduleConfig{Frequency: "daily", At: "18:00"}, from)
	want := time.Date(2026, 10, 29, 18, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Past today's slot, it must roll to tomorrow rather than fire immediately.
	afterSlot := time.Date(2026, 10, 29, 19, 0, 0, 0, time.Local)
	got = s.NextRun(config.ScheduleConfig{Frequency: "daily", At: "18:00"}, afterSlot)
	want = time.Date(2026, 10, 30, 18, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("after the slot: got %v, want %v", got, want)
	}
}

func TestNextRunWeeklyPicksTheRightWeekday(t *testing.T) {
	s, _, cleanup := newScheduler(t, nil, time.Now())
	defer cleanup()

	// 29 Oct 2026 is a Thursday; the next Friday 17:00 is the 30th.
	from := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	got := s.NextRun(config.ScheduleConfig{Frequency: "weekly", Day: "friday", At: "17:00"}, from)
	if got.Weekday() != time.Friday {
		t.Fatalf("expected a Friday, got %v (%v)", got.Weekday(), got)
	}
	want := time.Date(2026, 10, 30, 17, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// On the day itself but past the hour, it must roll a full week.
	onDayLate := time.Date(2026, 10, 30, 18, 0, 0, 0, time.Local)
	got = s.NextRun(config.ScheduleConfig{Frequency: "weekly", Day: "friday", At: "17:00"}, onDayLate)
	if !got.Equal(want.AddDate(0, 0, 7)) {
		t.Errorf("same-day-late: got %v, want %v", got, want.AddDate(0, 0, 7))
	}
}

func TestNextRunIntervalUsesWindow(t *testing.T) {
	s, _, cleanup := newScheduler(t, nil, time.Now())
	defer cleanup()

	from := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	got := s.NextRun(config.ScheduleConfig{Frequency: "interval", Window: "30m"}, from)
	if !got.Equal(from.Add(30 * time.Minute)) {
		t.Errorf("got %v, want %v", got, from.Add(30*time.Minute))
	}
}

func TestSyncStoresSchedulesAndSeedsNextRun(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	s, _, cleanup := newScheduler(t, []config.ScheduleConfig{
		{Name: "daily-activity", Enabled: true, Frequency: "daily", At: "18:00", Type: "activity", Window: "previous_calendar_day"},
		{Name: "weekly-digest", Enabled: true, Frequency: "weekly", Day: "friday", At: "17:00", Type: "digest", Window: "week"},
	}, now)
	defer cleanup()

	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Store.ListSchedules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 schedules stored, got %d", len(rows))
	}
	for _, r := range rows {
		if r.NextRunAt == nil {
			t.Errorf("%s: next_run_at was not seeded", r.Name)
		}
		if !r.IsEnabled {
			t.Errorf("%s: should be enabled", r.Name)
		}
	}

	// Syncing again must not duplicate rows.
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Store.ListSchedules()
	if len(rows) != 2 {
		t.Errorf("re-sync duplicated schedules: got %d rows", len(rows))
	}
}

func TestTickFiresDueScheduleAndRecordsRun(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	s, runner, cleanup := newScheduler(t, []config.ScheduleConfig{
		{Name: "daily-activity", Enabled: true, Frequency: "daily", At: "18:00", Type: "activity", Window: "previous_calendar_day"},
	}, now)
	defer cleanup()

	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}

	// Nothing is due yet at 09:00 for an 18:00 schedule.
	if fired := s.Tick(); len(fired) != 0 {
		t.Fatalf("nothing should have fired yet, got %v", fired)
	}
	if runner.activity != 0 {
		t.Fatalf("no report should have been generated, got %d", runner.activity)
	}

	// Move past the slot.
	s.Now = func() time.Time { return time.Date(2026, 10, 29, 18, 1, 0, 0, time.Local) }
	fired := s.Tick()
	if len(fired) != 1 || fired[0] != "daily-activity" {
		t.Fatalf("expected daily-activity to fire, got %v", fired)
	}
	if runner.activity != 1 {
		t.Errorf("expected 1 activity report, got %d", runner.activity)
	}

	// It must not fire twice for the same slot.
	if fired := s.Tick(); len(fired) != 0 {
		t.Errorf("schedule fired twice in one slot: %v", fired)
	}

	rows, _ := s.Store.ListSchedules()
	if rows[0].LastRunAt == nil {
		t.Error("last_run_at was not recorded")
	}
	if rows[0].NextRunAt == nil || !rows[0].NextRunAt.After(s.Now()) {
		t.Error("next_run_at was not advanced past now")
	}
}

func TestTickSkipsDisabledSchedules(t *testing.T) {
	now := time.Date(2026, 10, 29, 19, 0, 0, 0, time.Local)
	s, runner, cleanup := newScheduler(t, []config.ScheduleConfig{
		{Name: "off", Enabled: false, Frequency: "daily", At: "18:00", Type: "activity"},
	}, now)
	defer cleanup()

	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	if fired := s.Tick(); len(fired) != 0 {
		t.Errorf("a disabled schedule must not fire, got %v", fired)
	}
	if runner.activity != 0 {
		t.Errorf("a disabled schedule generated %d reports", runner.activity)
	}
}

// catch_up "skip" means a run missed while the server was down is abandoned,
// not generated late.
func TestTickHonoursSkipCatchUpPolicy(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	s, runner, cleanup := newScheduler(t, []config.ScheduleConfig{
		{Name: "skippy", Enabled: true, Frequency: "daily", At: "18:00", Type: "activity", CatchUp: "skip"},
	}, now)
	defer cleanup()

	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}

	// Come back three days late.
	s.Now = func() time.Time { return time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local) }
	if fired := s.Tick(); len(fired) != 0 {
		t.Errorf("skip policy should not generate a stale report, got %v", fired)
	}
	if runner.activity != 0 {
		t.Errorf("skip policy generated %d reports", runner.activity)
	}

	rows, _ := s.Store.ListSchedules()
	if rows[0].NextRunAt == nil || !rows[0].NextRunAt.After(s.Now()) {
		t.Error("skip policy must still advance next_run_at")
	}
}

func TestTickRunOnceCatchesUpAfterDowntime(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	s, runner, cleanup := newScheduler(t, []config.ScheduleConfig{
		{Name: "catchy", Enabled: true, Frequency: "daily", At: "18:00", Type: "activity", CatchUp: "run_once"},
	}, now)
	defer cleanup()

	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}

	s.Now = func() time.Time { return time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local) }
	fired := s.Tick()
	if len(fired) != 1 {
		t.Fatalf("run_once should generate one catch-up report, got %v", fired)
	}
	if runner.activity != 1 {
		t.Errorf("expected exactly 1 catch-up report, got %d", runner.activity)
	}
}

// Editing a schedule's timing in config.yaml must take effect. Preserving
// next_run_at unconditionally meant changing "at" was silently ignored.
func TestSyncAppliesTimingChangesButKeepsPendingSlot(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.Local)
	s, _, cleanup := newScheduler(t, []config.ScheduleConfig{
		{Name: "movable", Enabled: true, Frequency: "daily", At: "18:00", Type: "activity"},
	}, now)
	defer cleanup()

	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Store.ListSchedules()
	first := *rows[0].NextRunAt
	if first.Hour() != 18 {
		t.Fatalf("expected an 18:00 slot, got %v", first)
	}

	// Re-syncing an unchanged schedule must keep the pending slot, so a
	// restart does not discard a run that was already due.
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Store.ListSchedules()
	if !rows[0].NextRunAt.Equal(first) {
		t.Errorf("unchanged schedule moved its slot: %v -> %v", first, *rows[0].NextRunAt)
	}

	// Changing the time must move the slot.
	s.Config.Reports.Schedules[0].At = "09:30"
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Store.ListSchedules()
	if rows[0].NextRunAt.Hour() != 9 || rows[0].NextRunAt.Minute() != 30 {
		t.Errorf("timing change was not applied: next run is %v", *rows[0].NextRunAt)
	}
}
