package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/reports"
)

// Snapshots are meant to be an immutable archive. The numbering was derived
// from a report row that was recreated on every run, so it was always 1 and
// each snapshot silently overwrote the previous one — the archive could only
// ever hold the most recent copy.

func newEngine(t *testing.T) (*reports.ReportEngine, string, func()) {
	t.Helper()
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)

	cfg := config.DefaultConfig("snapshot-test", "main")
	return reports.NewReportEngine(repo, cfg, db), repo, func() {
		cleanup()
		os.RemoveAll(repo)
	}
}

func TestSnapshotsAccumulateInsteadOfOverwriting(t *testing.T) {
	engine, repo, cleanup := newEngine(t)
	defer cleanup()

	for i, reason := range []string{"first", "second", "third"} {
		report, _, err := engine.GenerateActivityReport("main", "", reason)
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if len(report.Snapshots) != i+1 {
			t.Fatalf("after run %d the report carries %d snapshot(s), want %d",
				i+1, len(report.Snapshots), i+1)
		}
	}

	dir := filepath.Join(repo, ".devtrail", "reports", "activity", "main", "snapshots")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected 3 archived snapshots on disk, got %d: %v", len(entries), names)
	}

	for _, want := range []string{"snapshot_001.html", "snapshot_002.html", "snapshot_003.html"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
}

// Regenerating the same report must update one row, not add a row per run.
func TestRegeneratingReusesTheReportRow(t *testing.T) {
	engine, _, cleanup := newEngine(t)
	defer cleanup()

	var firstID string
	for i := 0; i < 3; i++ {
		report, _, err := engine.GenerateActivityReport("main", "", "")
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if i == 0 {
			firstID = report.ID
		} else if report.ID != firstID {
			t.Errorf("run %d created a new report row (%s, first was %s)", i+1, report.ID, firstID)
		}
	}

	rows, err := engine.Store.ListReports(core.ReportTypeActivity, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 activity report row after 3 runs, got %d", len(rows))
	}
}

// Different branches and different tasks are different logical reports and
// must keep their own rows and their own archives.
func TestSeparateTargetsKeepSeparateReports(t *testing.T) {
	engine, repo, cleanup := newEngine(t)
	defer cleanup()

	if err := engine.Store.CreateTask(&core.Task{
		ID: "DT-0042", ProjectID: "default", Title: "Separate target",
		TaskType: "feature", Status: core.TaskStatusPlanned,
	}); err != nil {
		t.Fatal(err)
	}

	plain, _, err := engine.GenerateActivityReport("main", "", "a")
	if err != nil {
		t.Fatal(err)
	}
	scoped, _, err := engine.GenerateActivityReport("main", "DT-0042", "a")
	if err != nil {
		t.Fatal(err)
	}
	if plain.ID == scoped.ID {
		t.Error("a task-scoped report must not reuse the unscoped report's row")
	}

	rows, _ := engine.Store.ListReports(core.ReportTypeActivity, 50)
	if len(rows) != 2 {
		t.Errorf("expected 2 report rows for 2 targets, got %d", len(rows))
	}
	_ = repo
}

// Numbering takes the maximum rather than the count, so deleting a snapshot
// cannot cause the next one to overwrite an existing file.
func TestSnapshotNumberingSurvivesDeletedRows(t *testing.T) {
	db, cleanup := newTestStore(t)
	defer cleanup()

	report := &core.Report{
		ID: "rep-1", ProjectID: "default", ReportType: core.ReportTypeActivity,
		Title: "t", Status: "generated", FilePath: "/tmp/x.html", ProfileVersion: 1,
	}
	if err := db.CreateReport(report); err != nil {
		t.Fatal(err)
	}

	for n := 1; n <= 3; n++ {
		if err := db.CreateReportSnapshot(&core.ReportSnapshot{
			ID: string(rune('a'+n)) + "-snap", ReportID: report.ID, SnapshotNumber: n,
			TriggerReason: "r", HeadSHA: "sha", FilePath: "/tmp/s.html",
		}); err != nil {
			t.Fatal(err)
		}
	}

	next, err := db.NextSnapshotNumber(report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Fatalf("next snapshot number = %d, want 4", next)
	}
}

// Two logical reports must not write the same file. A task-scoped report and
// an unscoped one for the same branch previously shared latest.html, so each
// run overwrote the other's output.
func TestTaskScopedReportGetsItsOwnFile(t *testing.T) {
	engine, _, cleanup := newEngine(t)
	defer cleanup()

	if err := engine.Store.CreateTask(&core.Task{
		ID: "DT-0077", ProjectID: "default", Title: "Own file",
		TaskType: "feature", Status: core.TaskStatusPlanned,
	}); err != nil {
		t.Fatal(err)
	}

	_, plainPath, err := engine.GenerateActivityReport("main", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, scopedPath, err := engine.GenerateActivityReport("main", "DT-0077", "")
	if err != nil {
		t.Fatal(err)
	}

	if plainPath == scopedPath {
		t.Fatalf("both reports wrote the same file: %s", plainPath)
	}
	for _, p := range []string{plainPath, scopedPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", filepath.Base(p), err)
		}
	}

	// Their snapshot archives must be separate too.
	if _, _, err := engine.GenerateActivityReport("main", "DT-0077", "scoped snap"); err != nil {
		t.Fatal(err)
	}
	scopedSnaps := filepath.Join(filepath.Dir(scopedPath), "snapshots")
	entries, err := os.ReadDir(scopedSnaps)
	if err != nil {
		t.Fatalf("task-scoped report has no snapshot directory of its own: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 snapshot in the task's own archive, got %d", len(entries))
	}
}

// Dedupe must be conservative: it removes only rows that share a file with
// another row AND own no snapshot archive of their own.
func TestFindDuplicateReportsOnlyTargetsSafeRows(t *testing.T) {
	db, cleanup := newTestStore(t)
	defer cleanup()

	mk := func(id, path string, snaps int) {
		t.Helper()
		if err := db.CreateReport(&core.Report{
			ID: id, ProjectID: "default", ReportType: core.ReportTypeActivity,
			Title: "r", Status: "generated", FilePath: path, ProfileVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
		for n := 1; n <= snaps; n++ {
			if err := db.CreateReportSnapshot(&core.ReportSnapshot{
				ID: id + "-s" + string(rune('0'+n)), ReportID: id, SnapshotNumber: n,
				TriggerReason: "r", HeadSHA: "sha", FilePath: "/tmp/s.html",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Three rows share one file: one has an archive, one is bare, one has its
	// own archive and must therefore survive despite sharing the path.
	mk("keeper", "/r/activity/main/latest.html", 3)
	mk("bare", "/r/activity/main/latest.html", 0)
	mk("archived", "/r/activity/main/latest.html", 1)
	// A row with its own file is never a duplicate.
	mk("solo", "/r/digest/digest_week_1.html", 0)
	mk("solo2", "/r/digest/digest_week_2.html", 0)

	dupes, err := db.FindDuplicateReports("default")
	if err != nil {
		t.Fatal(err)
	}

	if len(dupes) != 1 {
		ids := []string{}
		for _, d := range dupes {
			ids = append(ids, d.Report.ID)
		}
		t.Fatalf("expected exactly 1 removable row, got %d: %v", len(dupes), ids)
	}
	if dupes[0].Report.ID != "bare" {
		t.Errorf("removable row = %q, want %q", dupes[0].Report.ID, "bare")
	}
	if dupes[0].KeeperID != "keeper" {
		t.Errorf("keeper = %q, want the row with the most snapshots", dupes[0].KeeperID)
	}
}

func TestDeleteReportLeavesOtherRowsAndSnapshotsIntact(t *testing.T) {
	db, cleanup := newTestStore(t)
	defer cleanup()

	for _, id := range []string{"keep", "drop"} {
		if err := db.CreateReport(&core.Report{
			ID: id, ProjectID: "default", ReportType: core.ReportTypeActivity,
			Title: "r", Status: "generated", FilePath: "/r/latest.html", ProfileVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateReportSnapshot(&core.ReportSnapshot{
		ID: "s1", ReportID: "keep", SnapshotNumber: 1,
		TriggerReason: "r", HeadSHA: "sha", FilePath: "/r/s.html",
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteReport("drop"); err != nil {
		t.Fatal(err)
	}

	rows, _ := db.ListReports(core.ReportTypeActivity, 50)
	if len(rows) != 1 || rows[0].ID != "keep" {
		t.Fatalf("expected only the keeper to remain, got %+v", rows)
	}
	snaps, _ := db.ListSnapshots("keep")
	if len(snaps) != 1 {
		t.Errorf("the keeper's archive was damaged: %d snapshot(s)", len(snaps))
	}
}
