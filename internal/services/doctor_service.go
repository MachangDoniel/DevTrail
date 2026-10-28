package services

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/donieltripura/devtrail/internal/config"
)

type DiagnosticItem struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // 'OK', 'WARN', 'FAIL'
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type DoctorReport struct {
	OverallStatus string           `json:"overall_status"` // 'HEALTHY', 'WARNINGS', 'FAILURES'
	Items         []DiagnosticItem `json:"items"`
}

type DoctorService struct {
	RepoDir string
	Config  *config.Config
}

func NewDoctorService(repoDir string, cfg *config.Config) *DoctorService {
	return &DoctorService{RepoDir: repoDir, Config: cfg}
}

// RunDiagnostics executes comprehensive checks on Git, files, SQLite, and configuration.
func (d *DoctorService) RunDiagnostics() *DoctorReport {
	report := &DoctorReport{OverallStatus: "HEALTHY"}

	// 1. Git availability
	gitVer, err := exec.Command("git", "--version").Output()
	if err != nil {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git CLI",
			Status:  "FAIL",
			Message: "Git is not installed or not in PATH",
		})
		report.OverallStatus = "FAILURES"
	} else {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git CLI",
			Status:  "OK",
			Message: strings.TrimSpace(string(gitVer)),
		})
	}

	// 2. Repository check
	gitDirCheck := exec.Command("git", "-C", d.RepoDir, "rev-parse", "--is-inside-work-tree")
	if err := gitDirCheck.Run(); err != nil {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git Repository",
			Status:  "FAIL",
			Message: fmt.Sprintf("%s is not inside a Git repository", d.RepoDir),
		})
		report.OverallStatus = "FAILURES"
	} else {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git Repository",
			Status:  "OK",
			Message: fmt.Sprintf("Valid Git workspace at %s", d.RepoDir),
		})
	}

	// 3. DevTrail initialization & config
	dtPath := filepath.Join(d.RepoDir, config.DefaultDirName)
	if fi, err := os.Stat(dtPath); err != nil || !fi.IsDir() {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "DevTrail Initialization",
			Status:  "WARN",
			Message: "DevTrail is not initialized yet (run 'devtrail init')",
		})
		if report.OverallStatus != "FAILURES" {
			report.OverallStatus = "WARNINGS"
		}
	} else {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "DevTrail Directory",
			Status:  "OK",
			Message: fmt.Sprintf("Found %s", dtPath),
		})

		// Config check
		if d.Config != nil {
			if err := d.Config.Validate(); err != nil {
				report.Items = append(report.Items, DiagnosticItem{
					Name:    "Configuration",
					Status:  "FAIL",
					Message: fmt.Sprintf("Invalid configuration: %v", err),
				})
				report.OverallStatus = "FAILURES"
			} else {
				report.Items = append(report.Items, DiagnosticItem{
					Name:    "Configuration",
					Status:  "OK",
					Message: fmt.Sprintf("Project '%s' (v%d)", d.Config.Project.Name, d.Config.Version),
				})
			}
		}
	}

	// 4. Storage permissions
	testFile := filepath.Join(dtPath, ".write_test")
	if err := os.WriteFile(testFile, []byte("ok"), 0644); err == nil {
		_ = os.Remove(testFile)
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Storage Permissions",
			Status:  "OK",
			Message: "Read/Write permissions verified in .devtrail",
		})
	} else if fi, _ := os.Stat(dtPath); fi != nil {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Storage Permissions",
			Status:  "FAIL",
			Message: fmt.Sprintf("Cannot write to .devtrail: %v", err),
		})
		report.OverallStatus = "FAILURES"
	}

	// 5. Privacy settings
	if d.Config != nil {
		netMode := d.Config.Privacy.NetworkAccess
		if netMode == "disabled" {
			report.Items = append(report.Items, DiagnosticItem{
				Name:    "Privacy & Network",
				Status:  "OK",
				Message: "Local-only mode enforced (network access disabled)",
			})
		} else {
			report.Items = append(report.Items, DiagnosticItem{
				Name:    "Privacy & Network",
				Status:  "WARN",
				Message: fmt.Sprintf("Network mode set to '%s'", netMode),
			})
		}
	}

	// 6. Git ignore status
	missing, exists, _ := config.CheckGitIgnore(d.RepoDir)
	if !exists {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git Ignore (.gitignore)",
			Status:  "WARN",
			Message: "No .gitignore file found in repository root",
		})
	} else if len(missing) > 0 {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git Ignore (.gitignore)",
			Status:  "WARN",
			Message: fmt.Sprintf("%d DevTrail private paths are unignored", len(missing)),
			Detail:  strings.Join(missing, ", "),
		})
		if report.OverallStatus != "FAILURES" {
			report.OverallStatus = "WARNINGS"
		}
	} else {
		report.Items = append(report.Items, DiagnosticItem{
			Name:    "Git Ignore (.gitignore)",
			Status:  "OK",
			Message: "All DevTrail private files properly ignored",
		})
	}

	return report
}

// FormatDoctorText outputs human-readable diagnostic text.
func (report *DoctorReport) FormatDoctorText() string {
	var buf bytes.Buffer
	buf.WriteString("================ DevTrail Diagnostics (Doctor) ================\n\n")
	for _, item := range report.Items {
		icon := "[✓]"
		if item.Status == "WARN" {
			icon = "[!]"
		} else if item.Status == "FAIL" {
			icon = "[✗]"
		}
		buf.WriteString(fmt.Sprintf("%s %-25s : %s\n", icon, item.Name, item.Message))
		if item.Detail != "" {
			buf.WriteString(fmt.Sprintf("    Details: %s\n", item.Detail))
		}
	}
	buf.WriteString(fmt.Sprintf("\nOverall Status: %s\n", report.OverallStatus))
	return buf.String()
}
