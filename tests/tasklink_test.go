package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/services"
)

func TestReferencedTasksParsing(t *testing.T) {
	cases := []struct {
		message string
		want    []string
	}{
		{"feat(api): add endpoint (DT-1234)", []string{"DT-1234"}},
		{"fix: correct totals\n\nCloses DT-0007.", []string{"DT-0007"}},
		{"chore: tidy", nil},
		{"feat: two at once DT-1 and DT-22", []string{"DT-1", "DT-22"}},
		{"feat: repeated DT-5 ... DT-5", []string{"DT-5"}}, // deduplicated
		{"feat: lowercase dt-9 counts", []string{"DT-9"}},  // normalised
		{"feat: not a match ABCDT-1234x", nil},             // word boundary
		{"feat: too long DT-1234567", nil},                 // bounded digits
	}

	for _, tc := range cases {
		got := services.ReferencedTasks(tc.message)
		if len(got) != len(tc.want) {
			t.Errorf("%q -> %v, want %v", tc.message, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q -> %v, want %v", tc.message, got, tc.want)
				break
			}
		}
	}
}

// gitCommit makes a commit in repoDir touching a uniquely named file.
func gitCommit(t *testing.T, repoDir, file, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, file), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", file}, {"commit", "-m", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
}

func TestSyncLinksCommitsByExplicitReference(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	task := &core.Task{
		ID: "DT-4242", ProjectID: "default", Title: "Add the thing",
		TaskType: "feature", Status: core.TaskStatusInProgress,
	}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	gitCommit(t, repo, "a.txt", "feat(core): add the thing (DT-4242)")
	gitCommit(t, repo, "b.txt", "chore: unrelated tidy-up")

	svc := services.NewTaskLinkService(db, newCollector(repo), "main")
	res, err := svc.Sync(100)
	if err != nil {
		t.Fatal(err)
	}

	if res.Linked != 1 {
		t.Fatalf("expected exactly 1 link, got %d", res.Linked)
	}
	if res.ByReference != 1 {
		t.Errorf("expected the link to come from a reference, got %d", res.ByReference)
	}

	// The whole point: Task.Commits must now be populated.
	stored, err := db.GetTask("DT-4242")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Commits) != 1 {
		t.Fatalf("expected Task.Commits to hold 1 commit, got %d", len(stored.Commits))
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	if err := db.CreateTask(&core.Task{
		ID: "DT-1001", ProjectID: "default", Title: "Repeatable",
		TaskType: "feature", Status: core.TaskStatusPlanned,
	}); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repo, "a.txt", "feat: repeatable work (DT-1001)")

	svc := services.NewTaskLinkService(db, newCollector(repo), "main")
	if _, err := svc.Sync(100); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(100); err != nil {
		t.Fatal(err)
	}

	stored, _ := db.GetTask("DT-1001")
	if len(stored.Commits) != 1 {
		t.Errorf("re-running the sync duplicated links: %d commits", len(stored.Commits))
	}
}

// A reference to a task this project does not have must be ignored rather
// than creating a dangling link.
func TestSyncIgnoresUnknownTaskReferences(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	if err := db.CreateTask(&core.Task{
		ID: "DT-0001", ProjectID: "default", Title: "Real task",
		TaskType: "feature", Status: core.TaskStatusPlanned,
	}); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repo, "a.txt", "feat: references a ghost (DT-9999)")

	svc := services.NewTaskLinkService(db, newCollector(repo), "main")
	res, err := svc.Sync(100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked != 0 {
		t.Errorf("expected no links for an unknown task, got %d", res.Linked)
	}
}

// Branch matching must not claim the whole default branch. A task attached to
// main would otherwise absorb every commit in the repository.
func TestSyncDoesNotClaimDefaultBranchCommits(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	if err := db.CreateTask(&core.Task{
		ID: "DT-2002", ProjectID: "default", Title: "Attached to main",
		TaskType: "feature", Status: core.TaskStatusPlanned,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.LinkTask("DT-2002", "branch", "main"); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repo, "a.txt", "chore: ordinary work on main")

	svc := services.NewTaskLinkService(db, newCollector(repo), "main")
	res, err := svc.Sync(100)
	if err != nil {
		t.Fatal(err)
	}
	if res.ByBranch != 0 {
		t.Errorf("a task attached to the default branch claimed %d commits", res.ByBranch)
	}
}

func TestSyncLinksFeatureBranchCommits(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	if err := db.CreateTask(&core.Task{
		ID: "DT-3003", ProjectID: "default", Title: "On a branch",
		TaskType: "feature", Status: core.TaskStatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.LinkTask("DT-3003", "branch", "feature/x"); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("checkout", "-b", "feature/x")
	gitCommit(t, repo, "feat.txt", "feat: work with no task reference")
	run("checkout", "main")

	svc := services.NewTaskLinkService(db, newCollector(repo), "main")
	res, err := svc.Sync(100)
	if err != nil {
		t.Fatal(err)
	}
	if res.ByBranch != 1 {
		t.Fatalf("expected 1 branch-matched link, got %d (total %d)", res.ByBranch, res.Linked)
	}

	stored, _ := db.GetTask("DT-3003")
	if len(stored.Commits) != 1 {
		t.Errorf("expected the branch commit to be linked, got %d", len(stored.Commits))
	}
}

// Once links exist, the digest must carry the task on each change.
func TestDigestCarriesTaskOnLinkedChanges(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	if err := db.CreateTask(&core.Task{
		ID: "DT-7007", ProjectID: "default", Title: "Shows up in the digest",
		TaskType: "feature", Status: core.TaskStatusInProgress,
	}); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, repo, "a.txt", "feat(core): linked work (DT-7007)")

	collector := newCollector(repo)
	if _, err := services.NewTaskLinkService(db, collector, "main").Sync(100); err != nil {
		t.Fatal(err)
	}

	d, err := services.NewDigestService(db, collector).Build(services.DigestOptions{GroupBy: services.GroupByWeek})
	if err != nil {
		t.Fatal(err)
	}

	var withTask int
	for _, b := range d.Buckets {
		for _, ch := range b.Changes {
			if ch.Task != nil {
				withTask++
				if ch.Task.ID != "DT-7007" {
					t.Errorf("change linked to %q, want DT-7007", ch.Task.ID)
				}
				if ch.Task.Title != "Shows up in the digest" {
					t.Errorf("task title not carried: %q", ch.Task.Title)
				}
			}
		}
	}
	if withTask != 1 {
		t.Errorf("expected exactly 1 change carrying a task, got %d", withTask)
	}
}
