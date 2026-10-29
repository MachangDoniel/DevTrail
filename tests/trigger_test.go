package tests

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/services"
)

type recordingRunner struct {
	releases []([2]string) // from, to
	integra  []string      // target branch
	activity int
	failNext bool
}

func (r *recordingRunner) GenerateReleaseReport(from, to, target string) (*core.Report, string, error) {
	if r.failNext {
		r.failNext = false
		return nil, "", errFake
	}
	if to != "" {
		r.releases = append(r.releases, [2]string{from, to})
	} else {
		r.integra = append(r.integra, target)
	}
	return &core.Report{ID: "rel"}, "/tmp/rel.html", nil
}

func (r *recordingRunner) GenerateActivityReport(branch, taskID, reason string) (*core.Report, string, error) {
	r.activity++
	return &core.Report{ID: "act"}, "/tmp/act.html", nil
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func newTriggers(t *testing.T, repo string, tcs []config.TriggerConfig) (*services.TriggerService, *recordingRunner, func()) {
	t.Helper()
	db, cleanup := newTestStore(t)

	cfg := config.DefaultConfig("trigger-test", "main")
	cfg.Reports.Triggers = tcs

	runner := &recordingRunner{}
	svc := services.NewTriggerService(db, collector.NewGitCollector(repo), cfg, runner)
	return svc, runner, cleanup
}

func TestEventPatternRendering(t *testing.T) {
	cases := []struct {
		tc   config.TriggerConfig
		want string
	}{
		{config.TriggerConfig{Event: "merge", Target: "main"}, "merge:main"},
		{config.TriggerConfig{Event: "merge"}, "merge:main"},
		{config.TriggerConfig{Event: "release_tag"}, "tag:v*"},
		{config.TriggerConfig{Event: "release_tag", Target: "rel-*"}, "tag:rel-*"},
		{config.TriggerConfig{Event: "post-commit"}, "commit:*"},
	}
	for _, c := range cases {
		if got := services.EventPattern(c.tc); got != c.want {
			t.Errorf("%+v -> %q, want %q", c.tc, got, c.want)
		}
	}
}

func TestTagTriggerFiresOncePerTag(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "release-tags", Enabled: true, Event: "release_tag",
			ReportType: "release", Mode: "versioned_release"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	gitIn(t, repo, "tag", "v1.0.0")

	fired := svc.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected the tag to fire once, got %v", fired)
	}
	if len(runner.releases) != 1 || runner.releases[0][1] != "v1.0.0" {
		t.Fatalf("expected a versioned release for v1.0.0, got %v", runner.releases)
	}

	// Polling again must not regenerate the same report.
	if fired := svc.Tick(); len(fired) != 0 {
		t.Errorf("the same tag fired twice: %v", fired)
	}
	if len(runner.releases) != 1 {
		t.Errorf("a second report was generated for the same tag: %v", runner.releases)
	}
}

func TestTagTriggerHonoursGlob(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "release-tags", Enabled: true, Event: "release_tag", Target: "v*",
			ReportType: "release", Mode: "versioned_release"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	gitIn(t, repo, "tag", "nightly-2026-10-29") // must not match v*
	if fired := svc.Tick(); len(fired) != 0 {
		t.Errorf("a non-matching tag fired: %v", fired)
	}
	if len(runner.releases) != 0 {
		t.Errorf("a non-matching tag produced a report: %v", runner.releases)
	}
}

func TestMergeTriggerIgnoresLinearHistory(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "main-merges", Enabled: true, Event: "merge", Target: "main",
			ReportType: "release", Mode: "integration"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	// The fixture has a single linear commit and no merges.
	if fired := svc.Tick(); len(fired) != 0 {
		t.Errorf("a linear history fired a merge trigger: %v", fired)
	}
	if len(runner.integra) != 0 {
		t.Errorf("a merge report was generated with no merge: %v", runner.integra)
	}
}

func TestMergeTriggerFiresOnARealMerge(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "main-merges", Enabled: true, Event: "merge", Target: "main",
			ReportType: "release", Mode: "integration"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	gitIn(t, repo, "checkout", "-b", "feature/x")
	gitCommit(t, repo, "f.txt", "feat: branch work")
	gitIn(t, repo, "checkout", "main")
	gitIn(t, repo, "merge", "--no-ff", "feature/x", "-m", "Merge branch 'feature/x'")

	fired := svc.Tick()
	if len(fired) != 1 {
		t.Fatalf("expected the merge to fire, got %v", fired)
	}
	if len(runner.integra) != 1 || runner.integra[0] != "main" {
		t.Errorf("expected an integration report for main, got %v", runner.integra)
	}

	if fired := svc.Tick(); len(fired) != 0 {
		t.Errorf("the same merge fired twice: %v", fired)
	}
}

func TestDisabledTriggerNeverFires(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "off", Enabled: false, Event: "release_tag", ReportType: "release"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "tag", "v9.9.9")

	if fired := svc.Tick(); len(fired) != 0 {
		t.Errorf("a disabled trigger fired: %v", fired)
	}
	if len(runner.releases) != 0 {
		t.Errorf("a disabled trigger generated a report: %v", runner.releases)
	}
}

func TestDebounceSuppressesABurst(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "release-tags", Enabled: true, Event: "release_tag",
			ReportType: "release", Mode: "versioned_release", DebounceSeconds: 300},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	base := time.Now()
	svc.Now = func() time.Time { return base }

	gitIn(t, repo, "tag", "v1.0.0")
	if fired := svc.Tick(); len(fired) != 1 {
		t.Fatalf("first tag should fire, got %v", fired)
	}

	// A second tag arrives inside the debounce window.
	gitIn(t, repo, "tag", "v1.0.1")
	svc.Now = func() time.Time { return base.Add(10 * time.Second) }
	if fired := svc.Tick(); len(fired) != 0 {
		t.Errorf("debounce did not suppress the burst: %v", fired)
	}

	// Past the window it is allowed through.
	svc.Now = func() time.Time { return base.Add(400 * time.Second) }
	if fired := svc.Tick(); len(fired) != 1 {
		t.Errorf("expected the queued tag after the debounce window, got %v", fired)
	}
	if len(runner.releases) != 2 {
		t.Errorf("expected 2 reports in total, got %d", len(runner.releases))
	}
}

// A failed generation must not record the key, so the trigger retries.
func TestFailedGenerationIsRetried(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "release-tags", Enabled: true, Event: "release_tag",
			ReportType: "release", Mode: "versioned_release"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	gitIn(t, repo, "tag", "v2.0.0")
	runner.failNext = true
	if fired := svc.Tick(); len(fired) != 0 {
		t.Fatalf("a failed generation should not count as fired: %v", fired)
	}

	if fired := svc.Tick(); len(fired) != 1 {
		t.Errorf("expected a retry after the failure, got %v", fired)
	}
	if len(runner.releases) != 1 {
		t.Errorf("expected exactly 1 successful report, got %d", len(runner.releases))
	}
}

// The first tag in a repository has no predecessor. Without a lower bound the
// engine falls back to an integration report named after the branch, which is
// not what a tag trigger asked for.
func TestFirstTagStillProducesAVersionedRelease(t *testing.T) {
	repo := newTestRepo(t)
	defer os.RemoveAll(repo)

	svc, runner, cleanup := newTriggers(t, repo, []config.TriggerConfig{
		{Name: "release-tags", Enabled: true, Event: "release_tag",
			ReportType: "release", Mode: "versioned_release"},
	})
	defer cleanup()
	if err := svc.Sync(); err != nil {
		t.Fatal(err)
	}

	gitIn(t, repo, "tag", "v1.0.0") // the repository's very first tag

	if fired := svc.Tick(); len(fired) != 1 {
		t.Fatalf("expected the first tag to fire, got %v", fired)
	}
	if len(runner.releases) != 1 {
		t.Fatalf("expected a versioned release, got integration reports %v", runner.integra)
	}
	from, to := runner.releases[0][0], runner.releases[0][1]
	if to != "v1.0.0" {
		t.Errorf("release upper bound = %q, want v1.0.0", to)
	}
	if from == "" {
		t.Error("the first release must still be bounded, from the root commit")
	}
}
