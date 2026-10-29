package reports

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/services"
	"github.com/google/uuid"
)

// DigestReportData is what the digest template renders.
type DigestReportData struct {
	Title       string
	ProjectName string
	GroupBy     string
	GeneratedAt time.Time
	Digest      *services.Digest
}

// digestFuncs are the helpers the template needs; keeping the logic here means
// the template stays declarative.
var digestFuncs = template.FuncMap{
	"typeLabel": services.TypeLabel,
	"typeClass": func(t string) string {
		switch t {
		case "feat", "fix", "refactor", "perf":
			return "type-" + t
		}
		return "type-other"
	},
	"shortDate": func(t time.Time) string { return t.Format("2 Jan 2006") },
	"dateTime":  func(t time.Time) string { return t.Format("2 Jan 2006 15:04") },
	"pct": func(n, total int) int {
		if total == 0 {
			return 0
		}
		return int(float64(n) / float64(total) * 100)
	},
	"agentCommits": func(d *services.Digest) int {
		n := 0
		for _, c := range d.Contributors {
			if c.Actor.IsAgent {
				n += c.Stats.Commits
			}
		}
		return n
	},
	"humanCommits": func(d *services.Digest) int {
		n := 0
		for _, c := range d.Contributors {
			if !c.Actor.IsAgent {
				n += c.Stats.Commits
			}
		}
		return n
	},
	"groupLabel": func(g string) string {
		switch g {
		case "day":
			return "day"
		case "month":
			return "month"
		case "feature":
			return "feature"
		case "merge":
			return "merge"
		case "author":
			return "author"
		default:
			return "week"
		}
	},
}

// GenerateDigestReport builds a self-contained HTML digest and records it.
// The output makes no external requests, matching the rest of DevTrail's
// reports and the project's local-first privacy posture.
func (e *ReportEngine) GenerateDigestReport(digest *services.Digest, projectName string) (*core.Report, string, error) {
	title := fmt.Sprintf("Change digest by %s", digestFuncs["groupLabel"].(func(string) string)(digest.GroupBy))

	data := DigestReportData{
		Title:       title,
		ProjectName: projectName,
		GroupBy:     digest.GroupBy,
		GeneratedAt: time.Now(),
		Digest:      digest,
	}

	tmpl, err := template.New("digest").Funcs(digestFuncs).Parse(DigestHTMLTemplate)
	if err != nil {
		return nil, "", fmt.Errorf("parse digest template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, "", fmt.Errorf("render digest: %w", err)
	}

	dir := filepath.Join(e.RepoDir, ".devtrail", "reports", "digest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create digest directory: %w", err)
	}
	name := fmt.Sprintf("digest_%s_%d.html", sanitizeFilename(digest.GroupBy), time.Now().Unix())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return nil, "", fmt.Errorf("write digest: %w", err)
	}

	report := &core.Report{
		ID:             uuid.NewString(),
		ProjectID:      "default",
		ReportType:     core.ReportTypeDigest,
		Title:          title,
		Status:         "generated",
		HeadRef:        digest.Ref,
		FilePath:       path,
		ProfileVersion: 1,
		CreatedAt:      time.Now().UTC(),
	}
	if !digest.RangeStart.IsZero() {
		start, end := digest.RangeStart, digest.RangeEnd
		report.SinceTime, report.UntilTime = &start, &end
	}
	if err := e.Store.CreateReport(report); err != nil {
		return nil, "", fmt.Errorf("store digest report: %w", err)
	}

	return report, path, nil
}
