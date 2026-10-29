package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/donieltripura/devtrail/internal/api"
	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/config"
	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/reports"
	"github.com/donieltripura/devtrail/internal/services"
	"github.com/donieltripura/devtrail/internal/storage"
	"github.com/donieltripura/devtrail/web"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	rootCmd = &cobra.Command{
		Use:   "devtrail",
		Short: "DevTrail — Local-first developer activity intelligence for Git-based projects",
		Long: `DevTrail captures, correlates, and reports on developer activity across
branches, worktrees, tasks, evidence, and integration releases with zero external dependencies.`,
	}
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(reportCmd)
	rootCmd.AddCommand(taskCmd)
	rootCmd.AddCommand(checkpointCmd)
	rootCmd.AddCommand(evidenceCmd)
	rootCmd.AddCommand(activityCmd)
	rootCmd.AddCommand(handoffCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(dashboardCmd)

	// Config subcommands
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configValidateCmd)

	// Report subcommands
	reportCmd.AddCommand(reportActivityCmd)
	reportCmd.AddCommand(reportReleaseCmd)
	reportCmd.AddCommand(reportListCmd)
	reportCmd.AddCommand(reportOpenCmd)

	// Task subcommands
	taskCmd.AddCommand(taskCreateCmd)
	taskCmd.AddCommand(taskListCmd)
	taskCmd.AddCommand(taskShowCmd)
	taskCmd.AddCommand(taskAttachCmd)
	taskCmd.AddCommand(taskNoteCmd)

	// Evidence subcommands
	evidenceCmd.AddCommand(evidenceAttachCmd)
	evidenceCmd.AddCommand(evidenceListCmd)

	// Init flags
	initCmd.Flags().String("name", "", "Custom project name")
	initCmd.Flags().Bool("auto-ignore", true, "Automatically update .gitignore with DevTrail private paths")

	// Report flags
	reportActivityCmd.Flags().String("branch", "", "Branch to generate report for (default: current)")
	reportActivityCmd.Flags().String("task", "", "Linked task ID (optional)")
	reportActivityCmd.Flags().String("snapshot", "", "Save an immutable snapshot with this trigger reason")
	reportReleaseCmd.Flags().String("from", "", "Starting tag/ref for versioned release (e.g. v1.0.0)")
	reportReleaseCmd.Flags().String("to", "", "Ending tag/ref for versioned release (e.g. v1.1.0)")
	reportReleaseCmd.Flags().String("target", "main", "Target branch for integration release")
	reportListCmd.Flags().String("type", "", "Filter by report type (activity, release_integration, release_versioned)")

	// Task flags
	taskCreateCmd.Flags().String("type", "feature", "Task type: feature, fix, refactor, chore")
	taskCreateCmd.Flags().String("objective", "", "Objective or summary")
	taskCreateCmd.Flags().Bool("attach", true, "Auto-attach current branch")
	taskCreateCmd.Flags().String("provider", "claude", "AI provider: claude, chatgpt, gemini, copilot, local, human")
	taskAttachCmd.Flags().String("branch", "", "Branch to attach (default: current)")

	// Checkpoint flags
	checkpointCmd.Flags().String("task", "", "Task ID to link checkpoint to")

	// Evidence flags
	evidenceAttachCmd.Flags().String("type", "test", "Evidence type: test, build, ci, manual")
	evidenceAttachCmd.Flags().String("result", "passed", "Result: passed, failed, error")
	evidenceAttachCmd.Flags().String("commit", "HEAD", "Git commit SHA")
	evidenceAttachCmd.Flags().String("branch", "", "Git branch name")
	evidenceAttachCmd.Flags().String("command", "", "Command executed")
	evidenceAttachCmd.Flags().Int64("duration", 0, "Execution duration in ms")

	// Activity flags
	activityCmd.Flags().String("branch", "", "Filter by branch")
	activityCmd.Flags().Int("limit", 25, "Maximum number of events to show")

	// Serve & Dashboard flags
	serveCmd.Flags().Int("port", 41820, "Port for localhost HTTP server")
	dashboardCmd.Flags().Int("port", 41820, "Port for localhost HTTP server")
}

func getRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	gitCol := collector.NewGitCollector(wd)
	isGit, root := gitCol.IsGitRepo()
	if !isGit {
		return "", fmt.Errorf("current directory is not inside a git repository")
	}
	return root, nil
}

func getDB(repoRoot string, cfg *config.Config) (*storage.DB, error) {
	dbPath := filepath.Join(repoRoot, cfg.Storage.Database)
	return storage.Open(dbPath)
}

// -------------------------------------------------------------
// INIT COMMAND
// -------------------------------------------------------------
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize DevTrail in the current repository",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		autoIgnore, _ := cmd.Flags().GetBool("auto-ignore")

		cfg, missingIgnore, err := services.InitProject(repoRoot, name, autoIgnore)
		if err != nil {
			return fmt.Errorf("initialization failed: %w", err)
		}

		// Initialize DB
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return fmt.Errorf("failed to initialize sqlite store: %w", err)
		}
		defer db.Close()

		// Save project record
		proj := &core.Project{
			ID:            "default",
			Name:          cfg.Project.Name,
			RootPath:      repoRoot,
			DefaultBranch: cfg.Project.DefaultBranch,
			Timezone:      cfg.Project.Timezone,
			ConfigVersion: cfg.Version,
		}
		_ = db.UpsertProject(proj)

		// Sync initial state
		ps := services.NewProjectService(repoRoot, cfg, db)
		_ = ps.SyncCurrentState()

		fmt.Println("✓ DevTrail initialized successfully!")
		fmt.Printf("  • Project: %s\n", cfg.Project.Name)
		fmt.Printf("  • Config:  %s\n", filepath.Join(repoRoot, config.DefaultDirName, config.DefaultConfigFilename))
		fmt.Printf("  • Store:   %s\n", filepath.Join(repoRoot, cfg.Storage.Database))

		if autoIgnore && len(missingIgnore) > 0 {
			fmt.Printf("  • Updated .gitignore with %d DevTrail private paths\n", len(missingIgnore))
		}

		fmt.Println("\nRun 'devtrail dashboard' to launch the interactive intelligence dashboard.")
		return nil
	},
}

// -------------------------------------------------------------
// STATUS COMMAND
// -------------------------------------------------------------
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Display repository intelligence status and activity counts",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		ps := services.NewProjectService(repoRoot, cfg, db)
		_ = ps.SyncCurrentState()
		status, err := ps.GetStatus()
		if err != nil {
			return err
		}

		fmt.Print(status.FormatStatusText())
		return nil
	},
}

// -------------------------------------------------------------
// DOCTOR COMMAND
// -------------------------------------------------------------
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics on repository, git, database, and permissions",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, _ := config.LoadConfig(repoRoot)
		doc := services.NewDoctorService(repoRoot, cfg)
		report := doc.RunDiagnostics()
		fmt.Print(report.FormatDoctorText())
		return nil
	},
}

// -------------------------------------------------------------
// CONFIG COMMANDS
// -------------------------------------------------------------
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and validate DevTrail configuration",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		data, _ := yaml.Marshal(cfg)
		fmt.Println(string(data))
		return nil
	},
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check config.yaml for syntax and schema errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return fmt.Errorf("validation error: %w", err)
		}
		fmt.Printf("✓ Configuration is valid (version %d, project '%s')\n", cfg.Version, cfg.Project.Name)
		return nil
	},
}

// -------------------------------------------------------------
// REPORT COMMANDS
// -------------------------------------------------------------
var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate, list, inspect, and open HTML reports",
}

var reportActivityCmd = &cobra.Command{
	Use:   "activity",
	Short: "Generate or refresh an Activity Report for ongoing work",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		branch, _ := cmd.Flags().GetString("branch")
		taskID, _ := cmd.Flags().GetString("task")
		snapshot, _ := cmd.Flags().GetString("snapshot")

		engine := reports.NewReportEngine(repoRoot, cfg, db)
		report, path, err := engine.GenerateActivityReport(branch, taskID, snapshot)
		if err != nil {
			return fmt.Errorf("failed to generate activity report: %w", err)
		}

		fmt.Printf("✓ Activity Report generated:\n")
		fmt.Printf("  • Title: %s\n", report.Title)
		fmt.Printf("  • File:  %s\n", path)
		if snapshot != "" {
			fmt.Printf("  • Snapshot created: %s\n", snapshot)
		}
		return nil
	},
}

var reportReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Generate an Integration or Versioned Release report",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		fromRef, _ := cmd.Flags().GetString("from")
		toRef, _ := cmd.Flags().GetString("to")
		target, _ := cmd.Flags().GetString("target")

		engine := reports.NewReportEngine(repoRoot, cfg, db)
		report, path, err := engine.GenerateReleaseReport(fromRef, toRef, target)
		if err != nil {
			return fmt.Errorf("failed to generate release report: %w", err)
		}

		fmt.Printf("✓ Release Report generated:\n")
		fmt.Printf("  • Title: %s\n", report.Title)
		fmt.Printf("  • File:  %s\n", path)
		return nil
	},
}

var reportListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all generated reports in the local archive",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		rType, _ := cmd.Flags().GetString("type")
		list, err := db.ListReports(rType, 50)
		if err != nil {
			return err
		}

		if len(list) == 0 {
			fmt.Println("No reports found. Generate one with 'devtrail report activity' or 'devtrail report release'.")
			return nil
		}

		fmt.Println("DevTrail Report Archive:")
		for _, r := range list {
			fmt.Printf("  [%s] %-25s | Type: %-15s | Scope: %s (%s)\n",
				r.CreatedAt.Format("2006-01-02 15:04"), r.Title, r.ReportType, r.HeadRef, truncate(r.HeadSHA, 7))
			fmt.Printf("         Path: %s\n", r.FilePath)
		}
		return nil
	},
}

var reportOpenCmd = &cobra.Command{
	Use:   "open [latest|REPORT_PATH]",
	Short: "Open an HTML report in the browser",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		target := "latest"
		if len(args) > 0 {
			target = args[0]
		}

		targetPath := target
		if target == "latest" {
			reportsList, err := db.ListReports("", 1)
			if err != nil || len(reportsList) == 0 {
				return fmt.Errorf("no reports found in archive")
			}
			targetPath = reportsList[0].FilePath
		}

		api.OpenBrowser(targetPath)
		fmt.Printf("Opened %s in browser.\n", targetPath)
		return nil
	},
}

// -------------------------------------------------------------
// TASK COMMANDS
// -------------------------------------------------------------
var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Track and organize features, fixes, and milestones",
}

var taskCreateCmd = &cobra.Command{
	Use:   "create <title>",
	Short: "Create a new task item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		tType, _ := cmd.Flags().GetString("type")
		objective, _ := cmd.Flags().GetString("objective")
		attach, _ := cmd.Flags().GetBool("attach")
		provider, _ := cmd.Flags().GetString("provider")

		svc := services.NewTaskService(db, collector.NewGitCollector(repoRoot))
		task, err := svc.CreateTask(args[0], tType, objective, provider, attach)
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		fmt.Printf("✓ Created task %s: %s\n", task.ID, task.Title)
		if len(task.Branches) > 0 {
			fmt.Printf("  • Linked to branch: %s\n", strings.Join(task.Branches, ", "))
		}
		return nil
	},
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tracked tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		tasks, err := db.ListTasks("default")
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			fmt.Println("No tasks found. Create one with 'devtrail task create \"Title\"'.")
			return nil
		}

		fmt.Println("Tracked Tasks:")
		for _, t := range tasks {
			fmt.Printf("  [%s] %-10s %-30s (Status: %s)\n", t.ID, t.TaskType, t.Title, t.Status)
		}
		return nil
	},
}

var taskShowCmd = &cobra.Command{
	Use:   "show <task-id>",
	Short: "Inspect a task and its linked branches/commits",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		task, err := db.GetTask(args[0])
		if err != nil {
			return fmt.Errorf("task %s not found: %w", args[0], err)
		}

		fmt.Printf("Task: %s\n", task.ID)
		fmt.Printf("Title: %s\n", task.Title)
		fmt.Printf("Type: %s | Status: %s\n", task.TaskType, task.Status)
		if task.Objective != "" {
			fmt.Printf("Objective: %s\n", task.Objective)
		}
		if len(task.Branches) > 0 {
			fmt.Printf("Branches: %s\n", strings.Join(task.Branches, ", "))
		}
		if task.Notes != "" {
			fmt.Printf("Notes:\n%s\n", task.Notes)
		}
		return nil
	},
}

var taskAttachCmd = &cobra.Command{
	Use:   "attach <task-id>",
	Short: "Attach a branch to a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		branch, _ := cmd.Flags().GetString("branch")
		svc := services.NewTaskService(db, collector.NewGitCollector(repoRoot))
		if err := svc.AttachBranch(args[0], branch); err != nil {
			return err
		}
		fmt.Printf("✓ Attached task %s to branch %s\n", args[0], branch)
		return nil
	},
}

var taskNoteCmd = &cobra.Command{
	Use:   "note <task-id> <note>",
	Short: "Add a note to a task",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		svc := services.NewTaskService(db, collector.NewGitCollector(repoRoot))
		if err := svc.AddNote(args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("✓ Added note to task %s\n", args[0])
		return nil
	},
}

// -------------------------------------------------------------
// CHECKPOINT COMMAND
// -------------------------------------------------------------
var checkpointCmd = &cobra.Command{
	Use:   "checkpoint [message]",
	Short: "Record a development checkpoint milestone",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		message := "Checkpoint reached"
		if len(args) > 0 {
			message = strings.Join(args, " ")
		}
		taskID, _ := cmd.Flags().GetString("task")

		svc := services.NewTaskService(db, collector.NewGitCollector(repoRoot))
		cp, err := svc.RecordCheckpoint(taskID, message)
		if err != nil {
			return fmt.Errorf("failed to record checkpoint: %w", err)
		}

		fmt.Printf("✓ Checkpoint recorded at %s (%s):\n", cp.Branch, truncate(cp.CommitSHA, 7))
		fmt.Printf("  • Message: %s\n", cp.Message)
		return nil
	},
}

// -------------------------------------------------------------
// EVIDENCE COMMANDS
// -------------------------------------------------------------
var evidenceCmd = &cobra.Command{
	Use:   "evidence",
	Short: "Record and inspect test and build verification outcomes",
}

var evidenceAttachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Attach test or build outcome evidence",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		evType, _ := cmd.Flags().GetString("type")
		result, _ := cmd.Flags().GetString("result")
		commit, _ := cmd.Flags().GetString("commit")
		branch, _ := cmd.Flags().GetString("branch")
		command, _ := cmd.Flags().GetString("command")
		dur, _ := cmd.Flags().GetInt64("duration")

		svc := services.NewEvidenceService(db, collector.NewGitCollector(repoRoot))
		ev, err := svc.Record(evType, result, command, commit, branch, dur, "")
		if err != nil {
			return fmt.Errorf("failed to record evidence: %w", err)
		}

		fmt.Printf("✓ Recorded %s evidence (%s) for commit %s\n", ev.EvidenceType, ev.Result, truncate(ev.CommitSHA, 7))
		return nil
	},
}

var evidenceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded evidence",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		list, err := db.ListEvidence("", 50)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("No evidence recorded. Run 'devtrail evidence attach' or attach via dashboard.")
			return nil
		}

		fmt.Println("Recorded Evidence:")
		for _, e := range list {
			fmt.Printf("  [%s] %-6s %-10s | Result: %-6s | Commit: %s (%dms)\n",
				e.CreatedAt.Format("15:04:05"), e.EvidenceType, e.Command, e.Result, truncate(e.CommitSHA, 7), e.DurationMs)
		}
		return nil
	},
}

// -------------------------------------------------------------
// ACTIVITY COMMAND
// -------------------------------------------------------------
var activityCmd = &cobra.Command{
	Use:   "activity",
	Short: "Display chronological activity timeline",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		branch, _ := cmd.Flags().GetString("branch")
		limit, _ := cmd.Flags().GetInt("limit")

		evts, err := db.ListEvents(branch, "", limit)
		if err != nil {
			return err
		}
		if len(evts) == 0 {
			fmt.Println("No activity recorded yet.")
			return nil
		}

		fmt.Println("Activity Timeline:")
		for _, e := range evts {
			fmt.Printf("  [%s] %-20s | Src: %-12s | Branch: %s %s [%s]\n",
				e.OccurredAt.Format("2006-01-02 15:04:05"), e.EventType, e.Source, e.Branch, truncate(e.CommitSHA, 7), e.AttributionLevel)
		}
		return nil
	},
}

// -------------------------------------------------------------
// HANDOFF COMMAND
// -------------------------------------------------------------
var handoffCmd = &cobra.Command{
	Use:   "handoff <task-id>",
	Short: "Export an evidence-backed AI context handoff package for another model/agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := getRepoRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadConfig(repoRoot)
		if err != nil {
			return err
		}
		db, err := getDB(repoRoot, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		svc := services.NewHandoffService(db, collector.NewGitCollector(repoRoot))
		pkg, err := svc.GeneratePackage(args[0])
		if err != nil {
			return err
		}

		fmt.Print(pkg.FormattedPrompt)
		return nil
	},
}

// -------------------------------------------------------------
// SERVE & DASHBOARD COMMANDS
// -------------------------------------------------------------
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the local DevTrail HTTP API and dashboard server",
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		return runServer(port, false)
	},
}

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Launch the DevTrail interactive dashboard in your browser",
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		return runServer(port, true)
	},
}

func runServer(port int, autoOpen bool) error {
	repoRoot, err := getRepoRoot()
	if err != nil {
		return err
	}
	cfg, err := config.LoadConfig(repoRoot)
	if err != nil {
		return err
	}
	db, err := getDB(repoRoot, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	fsys, err := web.GetFS()
	if err != nil {
		return fmt.Errorf("failed to load web filesystem: %w", err)
	}

	srv := api.NewServer(repoRoot, cfg, db, fsys)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	url := fmt.Sprintf("http://%s", addr)

	// Run the schedules declared in config.yaml. Until now these were stored
	// and displayed but never executed.
	ctx, stopScheduler := context.WithCancel(context.Background())
	defer stopScheduler()
	srv.Scheduler.Start(ctx, time.Minute)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("DevTrail server running at %s\n", url)
	fmt.Println("Press Ctrl+C to stop.")

	if autoOpen {
		api.OpenBrowser(url)
	}

	// Shut down cleanly on Ctrl+C so the SQLite connection is closed properly.
	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-sigCh:
		fmt.Println("\nShutting down…")
		stopScheduler()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

func truncate(s string, l int) string {
	if len(s) > l {
		return s[:l]
	}
	return s
}
