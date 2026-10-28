package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

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
	Assets       fs.FS
}

func NewServer(repoDir string, cfg *config.Config, store core.StorageInterface, assets fs.FS) *Server {
	gitCol := collector.NewGitCollector(repoDir)
	return &Server{
		RepoDir:      repoDir,
		Config:       cfg,
		Store:        store,
		Git:          gitCol,
		ReportEngine: reports.NewReportEngine(repoDir, cfg, store),
		TaskService:  services.NewTaskService(store, gitCol),
		EvidenceSvc:  services.NewEvidenceService(store, gitCol),
		DoctorSvc:    services.NewDoctorService(repoDir, cfg),
		ProjectSvc:   services.NewProjectService(repoDir, cfg, store),
		HandoffSvc:   services.NewHandoffService(store, gitCol),
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
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/status", s.handleTaskStatus)
	mux.HandleFunc("/api/tasks/attach", s.handleTaskAttach)
	mux.HandleFunc("/api/tasks/note", s.handleTaskNote)
	mux.HandleFunc("/api/tasks/handoff", s.handleTaskHandoff)
	mux.HandleFunc("/api/timeline", s.handleTimeline)
	mux.HandleFunc("/api/checkpoints", s.handleCheckpoints)
	mux.HandleFunc("/api/evidence", s.handleEvidence)
	mux.HandleFunc("/api/reports", s.handleReports)
	mux.HandleFunc("/api/reports/generate", s.handleReportGenerate)
	mux.HandleFunc("/api/doctor", s.handleDoctor)
	mux.HandleFunc("/api/config", s.handleConfig)

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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.ProjectSvc.GetStatus()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, status)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	branch := r.URL.Query().Get("branch")
	evtType := r.URL.Query().Get("type")
	evts, err := s.Store.ListEvents(branch, evtType, 100)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, evts)
}

func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	branches, err := s.Git.ListBranches()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
			Title     string `json:"title"`
			TaskType  string `json:"task_type"`
			Objective string `json:"objective"`
			Attach    bool   `json:"attach_branch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
			return
		}
		task, err := s.TaskService.CreateTask(req.Title, req.TaskType, req.Objective, req.Attach)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, http.StatusCreated, task)
		return
	}

	tasks, err := s.Store.ListTasks("default")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if err := s.TaskService.AttachBranch(req.TaskID, req.Branch); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if err := s.TaskService.AddNote(req.TaskID, req.Note); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
			return
		}
		cp, err := s.TaskService.RecordCheckpoint(req.TaskID, req.Message)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, http.StatusCreated, cp)
		return
	}

	cps, err := s.Store.ListCheckpoints("", 50)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
			return
		}
		ev, err := s.EvidenceSvc.Record(req.Type, req.Result, req.Command, req.CommitSHA, req.Branch, req.DurationMs, req.LogPath)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		jsonResponse(w, http.StatusCreated, ev)
		return
	}

	evList, err := s.Store.ListEvidence("", 50)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, evList)
}

func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	rType := r.URL.Query().Get("type")
	reportsList, err := s.Store.ListReports(rType, 50)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, reportsList)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
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
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Transform absolute path into relative web path for iframe/opening
	webPath := strings.TrimPrefix(path, s.RepoDir+"/.devtrail/reports")
	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"report":   report,
		"file":     path,
		"web_url":  "/reports" + webPath,
	})
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	rep := s.DoctorSvc.RunDiagnostics()
	jsonResponse(w, http.StatusOK, rep)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, s.Config)
}

func (s *Server) handleGitGraph(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Git.GetCommitGraph(60)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if err := s.Store.UpdateTaskStatus(req.TaskID, req.Status); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleTaskHandoff(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "task_id query parameter required"})
		return
	}
	pkg, err := s.HandoffSvc.GeneratePackage(taskID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
