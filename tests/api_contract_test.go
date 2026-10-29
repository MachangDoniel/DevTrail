package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donieltripura/devtrail/internal/api"
	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/storage"
)

// These tests pin the exact JSON keys and status codes the dashboard in
// web/index.html consumes. The frontend and the Go models drifted apart once
// before — every view that read a renamed field failed silently inside a
// catch, so the UI showed stale placeholder values instead of an error.
// If a field below is renamed, this suite fails loudly rather than the UI
// going quiet.

// newTestRepo creates a throwaway git repository with one commit, so the
// git-backed endpoints have something real to read without touching the
// developer's own checkout.
func newTestRepo(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "devtrail-api-test-*")
	if err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		// Keep the developer's global git config out of the fixture.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	run("init", "--initial-branch=main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "DevTrail Test")
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "chore: fixture commit")

	return tmpDir
}

// newCollector builds a git collector rooted at a fixture repository.
func newCollector(repoDir string) *collector.GitCollector {
	return collector.NewGitCollector(repoDir)
}

func newTestServer(t *testing.T) (http.Handler, func()) {
	t.Helper()

	tmpDir := newTestRepo(t)

	cfg := config.DefaultConfig("contract-test", "main")
	db, err := storage.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	// Rows reference project_id "default" via a foreign key.
	if err := db.UpsertProject(&core.Project{
		ID: "default", Name: "contract-test", RootPath: tmpDir, DefaultBranch: "main",
	}); err != nil {
		db.Close()
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	srv := api.NewServer(tmpDir, cfg, db, nil)
	return srv.Routes(), func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}
}

// newTestStore opens a store with the "default" project already present.
func newTestStore(t *testing.T) (*storage.DB, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "devtrail-store-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}
	if err := db.UpsertProject(&core.Project{
		ID: "default", Name: "store-test", RootPath: tmpDir, DefaultBranch: "main",
	}); err != nil {
		db.Close()
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}
	return db, func() { db.Close(); os.RemoveAll(tmpDir) }
}

func getJSON(t *testing.T, handler http.Handler, path string) (int, map[string]interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: response is not a JSON object: %v (body: %s)", path, err, rec.Body.String())
	}
	return rec.Code, out
}

func postJSON(t *testing.T, handler http.Handler, path, body string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func requireKeys(t *testing.T, label string, obj map[string]interface{}, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			t.Errorf("%s: missing key %q (the dashboard reads it); got keys %v", label, k, keysOf(obj))
		}
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// /api/status is flat. The dashboard previously read data.project.name and
// threw, which silently aborted the whole topbar update.
func TestStatusIsFlat(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/status")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/status", body,
		"name", "current_branch", "working_tree_clean",
		"staged_files", "modified_files", "untracked_files",
		"total_tasks", "total_reports", "total_evidence")

	if _, nested := body["project"]; nested {
		t.Error(`/api/status must stay flat: a "project" object would mean the shape changed`)
	}
}

// /api/branches is an object with a branches array, not a bare array.
func TestBranchesIsObject(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/branches")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/branches", body, "branches", "tags", "worktrees")
}

// /api/timeline is an aggregate object whose tasks live under "tasks".
func TestTimelineIsAggregate(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/timeline")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/timeline", body,
		"parallel_tasks", "files_modified", "lines_added", "lines_deleted", "tasks")
}

// config.Config needs json tags, or it marshals Go field names (Project.Name).
func TestConfigMarshalsLowercase(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/config")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/config", body, "config", "yaml_raw")

	cfg, ok := body["config"].(map[string]interface{})
	if !ok {
		t.Fatal(`/api/config: "config" is not an object`)
	}
	requireKeys(t, "/api/config config", cfg, "version", "project", "storage", "privacy")

	if _, pascal := cfg["Project"]; pascal {
		t.Error(`config marshalled "Project" — json tags are missing from config.Config`)
	}
}

// /api/metrics backs the Overview page. Every tile reads one of these keys.
func TestMetricsShape(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/metrics")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/metrics", body,
		"heatmap", "commits_by_week", "tasks_by_status", "evidence_pass_rate",
		"evidence_total", "checkpoint_count", "total_commits", "total_events",
		"active_days", "max_day_count", "churn", "range_start", "range_end")

	if _, ok := body["heatmap"].([]interface{}); !ok {
		t.Error("/api/metrics: heatmap must be an array")
	}
}

// A bodyless POST means "default activity report" — the dashboard's quick
// action sends no body.
func TestReportGenerateAcceptsEmptyBody(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/reports/generate", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatalf("empty body must not be a 400; got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportGenerateRejectsUnknownType(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, _ := postJSON(t, h, "/api/reports/generate", `{"type":"nonsense"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown report type, got %d", code)
	}
}

// UpdateTaskStatus matches zero rows for an unknown id. Reporting success
// there made the UI claim a status change that never happened.
func TestTaskStatusUnknownIDIsNotFound(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, _ := postJSON(t, h, "/api/tasks/status", `{"task_id":"DT-0000","status":"testing"}`)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown task, got %d", code)
	}
}

func TestTaskStatusRejectsUndeclaredStatus(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	// "verified" was never a declared status; the old UI sent it anyway.
	code, _ := postJSON(t, h, "/api/tasks/status", `{"task_id":"DT-0001","status":"verified"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an undeclared status, got %d", code)
	}
}

// Task creation must keep the title and the provider the modal collects.
func TestTaskCreateKeepsTitleAndProvider(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := postJSON(t, h, "/api/tasks",
		`{"title":"Contract task","task_type":"chore","objective":"check","ai_provider":"gemini","attach_branch":true}`)
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", code, body)
	}
	if body["title"] != "Contract task" {
		t.Errorf("title was dropped: got %v", body["title"])
	}
	if body["ai_provider"] != "gemini" {
		t.Errorf("ai_provider was dropped: got %v", body["ai_provider"])
	}
	// branches is omitempty, so it only appears when a branch was attached.
	requireKeys(t, "task", body, "id", "task_type", "status", "ai_provider", "branches")
}

func TestTaskCreateRejectsEmptyTitle(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, _ := postJSON(t, h, "/api/tasks", `{"title":"  "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty title, got %d", code)
	}
}

func TestEvidenceRejectsUndeclaredTypeAndResult(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	if code, _ := postJSON(t, h, "/api/evidence", `{"evidence_type":"bogus","result":"passed"}`); code != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown evidence type, got %d", code)
	}
	if code, _ := postJSON(t, h, "/api/evidence", `{"evidence_type":"test","result":"bogus"}`); code != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown result, got %d", code)
	}
}

// Every event without an explicit idempotency key used to collide on "" and
// be dropped by INSERT OR IGNORE, so only the first one was ever stored.
func TestKeylessEventsAreAllPersisted(t *testing.T) {
	db, cleanup := newTestStore(t)
	defer cleanup()

	for _, kind := range []string{"task_created", "task_note_added", "checkpoint", "evidence_recorded"} {
		if err := db.InsertEvent(&core.Event{
			ProjectID:        "default",
			EventType:        kind,
			Source:           "cli",
			AttributionLevel: core.AttributionExplicit,
			PayloadJSON:      `{}`,
		}); err != nil {
			t.Fatalf("insert %s: %v", kind, err)
		}
	}

	events, err := db.ListEvents("", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected all 4 keyless events to persist, got %d", len(events))
	}
}

// Keyed events must still deduplicate — that is what the git collector relies on.
func TestKeyedEventsStillDeduplicate(t *testing.T) {
	db, cleanup := newTestStore(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		if err := db.InsertEvent(&core.Event{
			ProjectID:        "default",
			EventType:        "observe",
			Source:           "git_collector",
			IdempotencyKey:   "observe:main:abc123",
			AttributionLevel: core.AttributionVerified,
			PayloadJSON:      `{}`,
		}); err != nil {
			t.Fatal(err)
		}
	}

	events, err := db.ListEvents("", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected the keyed event to be stored once, got %d", len(events))
	}
}

// The Changes view filters by file path and builds its hotspot chart entirely
// from per-change `files`. If the digest stopped emitting them the UI would
// quietly show empty hotspots and a path filter that matches nothing, so the
// contract is pinned here.
func TestDigestChangesCarryFilesAndAttribution(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/digest?group=week")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/digest", body,
		"group_by", "stats", "types", "buckets", "contributors")

	buckets, ok := body["buckets"].([]interface{})
	if !ok || len(buckets) == 0 {
		t.Fatal("/api/digest: expected at least one bucket for the fixture commit")
	}
	bucket := buckets[0].(map[string]interface{})
	requireKeys(t, "bucket", bucket, "key", "label", "stats", "types", "agents", "changes")

	changes, ok := bucket["changes"].([]interface{})
	if !ok || len(changes) == 0 {
		t.Fatal("bucket carried no changes")
	}
	change := changes[0].(map[string]interface{})
	requireKeys(t, "change", change,
		"sha", "short_sha", "subject", "headline", "type", "timestamp", "author", "stats", "files")

	if _, ok := change["files"].([]interface{}); !ok {
		t.Error("change.files must be an array; the path filter and hotspots depend on it")
	}
	author, ok := change["author"].(map[string]interface{})
	if !ok {
		t.Fatal("change.author must be an object")
	}
	requireKeys(t, "change.author", author, "name", "is_agent")
}

func TestDigestRejectsUnknownGrouping(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, _ := getJSON(t, h, "/api/digest?group=quarter")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown grouping, got %d", code)
	}
}

func TestSchedulesEndpointShape(t *testing.T) {
	h, cleanup := newTestServer(t)
	defer cleanup()

	code, body := getJSON(t, h, "/api/schedules")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	requireKeys(t, "/api/schedules", body, "schedules", "sprint")

	sprint, ok := body["sprint"].(map[string]interface{})
	if !ok {
		t.Fatal("/api/schedules: sprint must be an object")
	}
	requireKeys(t, "sprint", sprint, "length_days", "prefix")
}
