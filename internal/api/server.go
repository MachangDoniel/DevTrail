package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/reports"
	"github.com/donieltripura/devtrail/internal/services"
)

type Server struct {
	RepoDir      string
	Config       *config.Config
	Store        core.StorageInterface
	Git          *collector.GitCollector
	ReportEngine *reports.ReportEngine
	TaskService  *services.TaskService
	EvidenceSvc  *services.EvidenceService
	DoctorSvc    *services.DoctorService
	ProjectSvc   *services.ProjectService
	HandoffSvc   *services.HandoffService
	MetricsSvc   *services.MetricsService
	DigestSvc    *services.DigestService
	Scheduler    *services.SchedulerService
	TaskLinkSvc  *services.TaskLinkService
	Assets       fs.FS
}

func NewServer(repoDir string, cfg *config.Config, store core.StorageInterface, assets fs.FS) *Server {
	gitCol := collector.NewGitCollector(repoDir)
	engine := reports.NewReportEngine(repoDir, cfg, store)
	digestSvc := services.NewDigestService(store, gitCol)
	return &Server{
		RepoDir:      repoDir,
		Config:       cfg,
		Store:        store,
		Git:          gitCol,
		ReportEngine: engine,
		TaskService:  services.NewTaskService(store, gitCol),
		EvidenceSvc:  services.NewEvidenceService(store, gitCol),
		DoctorSvc:    services.NewDoctorService(repoDir, cfg),
		ProjectSvc:   services.NewProjectService(repoDir, cfg, store),
		HandoffSvc:   services.NewHandoffService(store, gitCol),
		MetricsSvc:   services.NewMetricsService(store, gitCol),
		DigestSvc:    digestSvc,
		Scheduler:    services.NewSchedulerService(store, cfg, engine, digestSvc),
		TaskLinkSvc:  services.NewTaskLinkService(store, gitCol, cfg.Project.DefaultBranch),
		Assets:       assets,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// REST API Endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/activity", s.handleActivity)
	mux.HandleFunc("/api/branches", s.handleBranches)
	mux.HandleFunc("/api/git/graph", s.handleGitGraph)
	mux.HandleFunc("/api/git/commit", s.handleGitCommit)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/status", s.handleTaskStatus)
	mux.HandleFunc("/api/tasks/attach", s.handleTaskAttach)
	mux.HandleFunc("/api/tasks/note", s.handleTaskNote)
	mux.HandleFunc("/api/tasks/handoff", s.handleTaskHandoff)
	mux.HandleFunc("/api/tasks/link", s.handleTaskLink)
	mux.HandleFunc("/api/timeline", s.handleTimeline)
	mux.HandleFunc("/api/checkpoints", s.handleCheckpoints)
	mux.HandleFunc("/api/evidence", s.handleEvidence)
	mux.HandleFunc("/api/reports", s.handleReports)
	mux.HandleFunc("/api/reports/generate", s.handleReportGenerate)
	mux.HandleFunc("/api/doctor", s.handleDoctor)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/digest", s.handleDigest)
	mux.HandleFunc("/api/digest/report", s.handleDigestReport)
	mux.HandleFunc("/api/schedules", s.handleSchedules)

	// Serve generated HTML reports statically
	reportsDir := http.Dir(s.RepoDir + "/.devtrail/reports")
	mux.Handle("/reports/", http.StripPrefix("/reports/", http.FileServer(reportsDir)))

	// Serve embedded dashboard UI
	if s.Assets != nil {
		mux.Handle("/", http.FileServer(http.FS(s.Assets)))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("DevTrail API is running. No embedded web assets provided."))
		})
	}

	return s.withCors(mux)
}

func (s *Server) withCors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// clientError returns a message that is safe to show the caller.
func clientError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

// serverError logs the underlying failure and returns a generic message.
// Raw errors carry git stderr and absolute filesystem paths, so they stay server-side.
func serverError(w http.ResponseWriter, op string, err error) {
	log.Printf("devtrail: %s: %v", op, err)
	jsonResponse(w, http.StatusInternalServerError, map[string]string{
		"error": op + " failed; see server log for details",
	})
}

// queryInt reads a positive integer query parameter, falling back to def.
func queryInt(r *http.Request, key string, def, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// decodeBody decodes a JSON request body, treating an empty body as "use defaults".
func decodeBody(r *http.Request, dst interface{}) error {
	err := json.NewDecoder(r.Body).Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

var validEvidenceTypes = map[string]bool{
	core.EvidenceTypeTest:      true,
	core.EvidenceTypeBuild:     true,
	core.EvidenceTypeCI:        true,
	core.EvidenceTypeManual:    true,
	core.EvidenceTypeBenchmark: true,
}

var validEvidenceResults = map[string]bool{
	core.EvidenceResultPassed:  true,
	core.EvidenceResultFailed:  true,
	core.EvidenceResultError:   true,
	core.EvidenceResultSkipped: true,
}

var validTaskStatuses = map[string]bool{
	core.TaskStatusPlanned:        true,
	core.TaskStatusInProgress:     true,
	core.TaskStatusBlocked:        true,
	core.TaskStatusReadyForReview: true,
	core.TaskStatusTesting:        true,
	core.TaskStatusMerged:         true,
	core.TaskStatusReleased:       true,
	core.TaskStatusClosed:         true,
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.ProjectSvc.GetStatus()
	if err != nil {
		serverError(w, "read project status", err)
		return
	}
	jsonResponse(w, http.StatusOK, status)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	branch := r.URL.Query().Get("branch")
	evtType := r.URL.Query().Get("type")
	evts, err := s.Store.ListEvents(branch, evtType, queryInt(r, "limit", 100, 500))
	if err != nil {
		serverError(w, "list events", err)
		return
	}
	jsonResponse(w, http.StatusOK, evts)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := s.MetricsSvc.Collect(queryInt(r, "days", 365, 730))
	if err != nil {
		serverError(w, "collect metrics", err)
		return
	}
	jsonResponse(w, http.StatusOK, m)
}

// handleDigest answers "what changed, grouped by X".
func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	if group == "" {
		group = string(services.GroupByWeek)
	}
	if !services.ValidGroupBy(group) {
		clientError(w, http.StatusBadRequest,
			"group must be one of: day, week, month, sprint, feature, merge, author")
		return
	}

	opts := services.DigestOptions{
		GroupBy: services.GroupBy(group),
		Ref:     r.URL.Query().Get("ref"),
		Limit:   queryInt(r, "limit", 400, 2000),
		Sprint:  s.sprintSpec(),
	}
	if days := queryInt(r, "days", 0, 3650); days > 0 {
		opts.Since = time.Now().AddDate(0, 0, -days)
	}

	digest, err := s.DigestSvc.Build(opts)
	if err != nil {
		serverError(w, "build digest", err)
		return
	}
	jsonResponse(w, http.StatusOK, digest)
}

// handleDigestReport renders the current digest as a shareable HTML file.
func (s *Server) handleDigestReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Group string `json:"group"`
		Ref   string `json:"ref"`
		Days  int    `json:"days"`
		Limit int    `json:"limit"`
	}
	if err := decodeBody(r, &req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if req.Group == "" {
		req.Group = string(services.GroupByWeek)
	}
	if !services.ValidGroupBy(req.Group) {
		clientError(w, http.StatusBadRequest,
			"group must be one of: day, week, month, sprint, feature, merge, author")
		return
	}

	opts := services.DigestOptions{
		GroupBy: services.GroupBy(req.Group),
		Ref:     req.Ref,
		Limit:   req.Limit,
		Sprint:  s.sprintSpec(),
	}
	if req.Days > 0 {
		opts.Since = time.Now().AddDate(0, 0, -req.Days)
	}

	digest, err := s.DigestSvc.Build(opts)
	if err != nil {
		serverError(w, "build digest", err)
		return
	}

	report, path, err := s.ReportEngine.GenerateDigestReport(digest, s.Config.Project.Name)
	if err != nil {
		serverError(w, "generate digest report", err)
		return
	}

	report.WebURL = s.reportWebURL(path)
	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"report":  report,
		"file":    path,
		"web_url": report.WebURL,
	})
}

// sprintSpec maps the sprint cadence from config into the digest's spec.
func (s *Server) sprintSpec() services.SprintSpec {
	return services.SprintSpec{
		LengthDays: s.Config.SprintLength(),
		Anchor:     s.Config.SprintAnchor(),
		Prefix:     s.Config.SprintPrefix(),
	}
}

// handleSchedules reports what the scheduler is watching and when each
// schedule last ran or is next due.
func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListSchedules()
	if err != nil {
		serverError(w, "list schedules", err)
		return
	}
	if rows == nil {
		rows = []core.Schedule{}
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"schedules": rows,
		"sprint": map[string]interface{}{
			"length_days": s.Config.SprintLength(),
			"start_date":  s.Config.Sprints.StartDate,
			"prefix":      s.Config.SprintPrefix(),
		},
	})
}

// handleTaskLink scans history and links commits to the tasks they delivered.
func (s *Server) handleTaskLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Limit int `json:"limit"`
	}
	if err := decodeBody(r, &req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	res, err := s.TaskLinkSvc.Sync(req.Limit)
	if err != nil {
		serverError(w, "link commits to tasks", err)
		return
	}
	jsonResponse(w, http.StatusOK, res)
}

func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	branches, err := s.Git.ListBranches()
	if err != nil {
		serverError(w, "list branches", err)
		return
	}
	worktrees, _ := s.Git.ListWorktrees()
	tags, _ := s.Git.ListTags()

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"branches":  branches,
		"worktrees": worktrees,
		"tags":      tags,
	})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Title      string `json:"title"`
			TaskType   string `json:"task_type"`
			Objective  string `json:"objective"`
			AIProvider string `json:"ai_provider"`
			Attach     bool   `json:"attach_branch"`
		}
		if err := decodeBody(r, &req); err != nil {
			clientError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			clientError(w, http.StatusBadRequest, "title is required")
			return
		}
		task, err := s.TaskService.CreateTask(req.Title, req.TaskType, req.Objective, req.AIProvider, req.Attach)
		if err != nil {
			serverError(w, "create task", err)
			return
		}
		jsonResponse(w, http.StatusCreated, task)
		return
	}

	tasks, err := s.Store.ListTasks("default")
	if err != nil {
		serverError(w, "list tasks", err)
		return
	}
	jsonResponse(w, http.StatusOK, tasks)
}

func (s *Server) handleTaskAttach(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
		Branch string `json:"branch"`
	}
	if err := decodeBody(r, &req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if err := s.TaskService.AttachBranch(req.TaskID, req.Branch); err != nil {
		serverError(w, "attach branch", err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "attached"})
}

func (s *Server) handleTaskNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
		Note   string `json:"note"`
	}
	if err := decodeBody(r, &req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if err := s.TaskService.AddNote(req.TaskID, req.Note); err != nil {
		serverError(w, "add note", err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "note_added"})
}

func (s *Server) handleCheckpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			TaskID  string `json:"task_id"`
			Message string `json:"message"`
		}
		if err := decodeBody(r, &req); err != nil {
			clientError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		cp, err := s.TaskService.RecordCheckpoint(req.TaskID, req.Message)
		if err != nil {
			serverError(w, "record checkpoint", err)
			return
		}
		jsonResponse(w, http.StatusCreated, cp)
		return
	}

	cps, err := s.Store.ListCheckpoints("", 50)
	if err != nil {
		serverError(w, "list checkpoints", err)
		return
	}
	jsonResponse(w, http.StatusOK, cps)
}

func (s *Server) handleEvidence(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Type       string `json:"evidence_type"`
			Result     string `json:"result"`
			Command    string `json:"command"`
			CommitSHA  string `json:"commit_sha"`
			Branch     string `json:"branch"`
			DurationMs int64  `json:"duration_ms"`
			LogPath    string `json:"log_path"`
		}
		if err := decodeBody(r, &req); err != nil {
			clientError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if !validEvidenceTypes[req.Type] {
			clientError(w, http.StatusBadRequest, "evidence_type must be one of: test, build, ci, manual, benchmark")
			return
		}
		if !validEvidenceResults[req.Result] {
			clientError(w, http.StatusBadRequest, "result must be one of: passed, failed, error, skipped")
			return
		}
		ev, err := s.EvidenceSvc.Record(req.Type, req.Result, req.Command, req.CommitSHA, req.Branch, req.DurationMs, req.LogPath)
		if err != nil {
			serverError(w, "record evidence", err)
			return
		}
		jsonResponse(w, http.StatusCreated, ev)
		return
	}

	evList, err := s.Store.ListEvidence("", 50)
	if err != nil {
		serverError(w, "list evidence", err)
		return
	}
	jsonResponse(w, http.StatusOK, evList)
}

func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	rType := r.URL.Query().Get("type")
	reportsList, err := s.Store.ListReports(rType, 50)
	if err != nil {
		serverError(w, "list reports", err)
		return
	}
	for i := range reportsList {
		reportsList[i].WebURL = s.reportWebURL(reportsList[i].FilePath)
	}
	jsonResponse(w, http.StatusOK, reportsList)
}

// reportWebURL maps an absolute report path to the path it is served at under /reports/.
// Returns "" when the file lives outside the reports directory.
func (s *Server) reportWebURL(filePath string) string {
	root := filepath.Join(s.RepoDir, ".devtrail", "reports")
	rel, err := filepath.Rel(root, filePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return "/reports/" + filepath.ToSlash(rel)
}

func (s *Server) handleReportGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var req struct {
		Type           string `json:"type"` // 'activity', 'release'
		Branch         string `json:"branch"`
		TaskID         string `json:"task_id"`
		FromRef        string `json:"from_ref"`
		ToRef          string `json:"to_ref"`
		SnapshotReason string `json:"snapshot_reason"`
	}
	if err := decodeBody(r, &req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	switch req.Type {
	case "", "activity", "release":
	default:
		clientError(w, http.StatusBadRequest, `type must be "activity" or "release"`)
		return
	}

	var report *core.Report
	var path string
	var err error

	if req.Type == "release" {
		report, path, err = s.ReportEngine.GenerateReleaseReport(req.FromRef, req.ToRef, req.Branch)
	} else {
		report, path, err = s.ReportEngine.GenerateActivityReport(req.Branch, req.TaskID, req.SnapshotReason)
	}

	if err != nil {
		serverError(w, "generate report", err)
		return
	}

	report.WebURL = s.reportWebURL(path)
	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"report":  report,
		"file":    path,
		"web_url": report.WebURL,
	})
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	rep := s.DoctorSvc.RunDiagnostics()
	jsonResponse(w, http.StatusOK, rep)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	yamlPath := filepath.Join(s.RepoDir, ".devtrail", "config.yaml")
	var yamlRaw string
	if data, err := os.ReadFile(yamlPath); err == nil {
		yamlRaw = string(data)
	}
	resp := map[string]interface{}{
		"config":   s.Config,
		"yaml_raw": yamlRaw,
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	sha := r.URL.Query().Get("sha")
	detail, err := s.Git.GetCommitDetail(sha)
	if err != nil {
		serverError(w, "read commit", err)
		return
	}
	jsonResponse(w, http.StatusOK, detail)
}

func (s *Server) handleGitGraph(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Git.GetCommitGraph(queryInt(r, "limit", 60, 500))
	if err != nil {
		serverError(w, "read commit graph", err)
		return
	}
	// CommitGraphNode.TaskID has existed since the first release and was never
	// populated; fill it from the stored commit links.
	if index, err := services.BuildCommitIndex(s.Store); err == nil {
		for i := range nodes {
			if ref, ok := index[nodes[i].SHA]; ok {
				nodes[i].TaskID = ref.ID
			}
		}
	}
	jsonResponse(w, http.StatusOK, nodes)
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := decodeBody(r, &req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if req.TaskID == "" {
		clientError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	if !validTaskStatuses[req.Status] {
		clientError(w, http.StatusBadRequest,
			"status must be one of: planned, in_progress, blocked, ready_for_review, testing, merged, released, closed")
		return
	}
	// Confirm the task exists; UpdateTaskStatus matches zero rows silently otherwise.
	if _, err := s.Store.GetTask(req.TaskID); err != nil {
		clientError(w, http.StatusNotFound, "task not found: "+req.TaskID)
		return
	}
	if err := s.Store.UpdateTaskStatus(req.TaskID, req.Status); err != nil {
		serverError(w, "update task status", err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "updated", "task_id": req.TaskID})
}

func (s *Server) handleTaskHandoff(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		taskID = r.URL.Query().Get("id")
	}
	if taskID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "task_id query parameter required"})
		return
	}
	pkg, err := s.HandoffSvc.GeneratePackage(taskID)
	if err != nil {
		serverError(w, "build handoff package", err)
		return
	}
	jsonResponse(w, http.StatusOK, pkg)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListTasks("default")
	if err != nil {
		tasks = []core.Task{}
	}

	commits, _ := s.Git.CommitsBetween("", "", 30)
	var filesMod, linesAdd, linesDel int
	filesMap := make(map[string]bool)
	for _, c := range commits {
		linesAdd += c.Insertions
		linesDel += c.Deletions
		for _, f := range c.ChangedFiles {
			filesMap[f] = true
		}
	}
	filesMod = len(filesMap)

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"parallel_tasks": len(tasks),
		"files_modified": filesMod,
		"lines_added":    linesAdd,
		"lines_deleted":  linesDel,
		"tasks":          tasks,
	})
}

// OpenBrowser opens the dashboard URL in the default browser.
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}
