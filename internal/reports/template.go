package reports

import (
	"html/template"
	"time"

	"github.com/donieltripura/devtrail/internal/core"
)

type ReportData struct {
	Title           string
	ReportType      string
	ProjectName     string
	GeneratedAt     time.Time
	BaseRef         string
	HeadRef         string
	BaseSHA         string
	HeadSHA         string
	Branch          string
	Task            *core.Task
	Commits         []core.Commit
	TotalInsertions int
	TotalDeletions  int
	TotalFiles      int
	Evidence        []core.Evidence
	Checkpoints     []core.Checkpoint
	WorkingTree     string
	Warnings        []string
}

const BaseHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}} — DevTrail</title>
<style>
:root {
  /* These values mirror the dashboard's tokens in web/index.html so a report
     opened from the cockpit reads as the same product. Keep them in step. */
  --bg-primary:   #090c13;
  --bg-secondary: #0f1420;
  --bg-card:      #131926;
  --border:       rgba(255, 255, 255, 0.09);
  --text-primary:   #f1f5f9;
  --text-secondary: #a8b5c6;
  --text-dim:       #8899ad;
  --accent-cyan:    #38bdf8;
  --accent-blue:    #60a5fa;
  --accent-green:   #34d399;
  --accent-amber:   #fbbf24;
  --accent-red:     #fb7185;
  --accent-purple:  #c084fc;
  --radius-sm: 6px;
  --radius-md: 10px;
  --font-mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Monaco, Consolas, monospace;
  --font-sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
}
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  background: var(--bg-primary);
  color: var(--text-primary);
  font-family: var(--font-sans);
  line-height: 1.5;
  font-size: 14px;
  padding: 2rem 1rem;
  -webkit-font-smoothing: antialiased;
}
.container { max-width: 1000px; margin: 0 auto; }
header {
  border-bottom: 1px solid var(--border);
  padding-bottom: 1.5rem;
  margin-bottom: 2rem;
}
.badge-row { display: flex; gap: 0.5rem; align-items: center; margin-bottom: 0.75rem; flex-wrap: wrap; }
.badge {
  font-size: 0.75rem;
  font-weight: 600;
  padding: 0.2rem 0.6rem;
  border-radius: 9999px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.badge--info   { background: rgba(56, 189, 248, 0.13); color: var(--accent-cyan);  border: 1px solid rgba(56, 189, 248, 0.3); }
.badge--ok     { background: rgba(52, 211, 153, 0.13); color: var(--accent-green); border: 1px solid rgba(52, 211, 153, 0.3); }
.badge--warn   { background: rgba(251, 191, 36, 0.13); color: var(--accent-amber); border: 1px solid rgba(251, 191, 36, 0.3); }
.badge--danger { background: rgba(251, 113, 133, 0.13); color: var(--accent-red);  border: 1px solid rgba(251, 113, 133, 0.3); }
h1 { font-size: 1.875rem; font-weight: 700; margin-bottom: 0.5rem; }
.meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 1rem;
  margin: 1.5rem 0;
  background: var(--bg-card);
  padding: 1.25rem;
  border-radius: 12px;
  border: 1px solid var(--border);
  backdrop-filter: blur(8px);
}
.meta-item { display: flex; flex-direction: column; }
.meta-label { font-size: 0.75rem; color: var(--text-secondary); text-transform: uppercase; }
.meta-value { font-size: 0.95rem; font-weight: 600; font-family: var(--font-mono); }
.section { margin-bottom: 2.5rem; }
.section-title {
  font-size: 1.25rem;
  font-weight: 600;
  margin-bottom: 1rem;
  border-left: 4px solid var(--accent-cyan);
  padding-left: 0.75rem;
}
.card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 1.25rem;
  margin-bottom: 1rem;
}
.card-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 0.5rem; }
.commit-sha { font-family: var(--font-mono); color: var(--accent-cyan); font-weight: 600; }
.commit-msg { font-size: 1rem; font-weight: 500; margin-bottom: 0.5rem; }
.commit-stats { font-size: 0.8rem; color: var(--text-secondary); font-family: var(--font-mono); }
.ins { color: var(--accent-green); }
.del { color: var(--accent-red); }
.file-list { margin-top: 0.5rem; font-family: var(--font-mono); font-size: 0.8rem; color: var(--text-secondary); }
.evidence-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.75rem 0;
  border-bottom: 1px solid var(--border);
}
.evidence-row:last-child { border-bottom: none; }
.warning-box {
  background: rgba(245, 158, 11, 0.1);
  border: 1px solid rgba(245, 158, 11, 0.3);
  color: #fde68a;
  padding: 1rem;
  border-radius: 8px;
  margin-bottom: 1.5rem;
}
footer {
  border-top: 1px solid var(--border);
  padding-top: 1.5rem;
  text-align: center;
  font-size: 0.8rem;
  color: var(--text-secondary);
}
</style>
</head>
<body>
<div class="container">
  <header>
    <div class="badge-row">
      <span class="badge badge--info">{{.ReportType}}</span>
      <span class="badge badge--ok">DevTrail Evidence-Linked</span>
      {{if .WorkingTree}}<span class="badge badge--warn">{{.WorkingTree}}</span>{{end}}
    </div>
    <h1>{{.Title}}</h1>
    <div class="meta-grid">
      <div class="meta-item">
        <span class="meta-label">Project</span>
        <span class="meta-value">{{.ProjectName}}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">Branch</span>
        <span class="meta-value">{{.Branch}}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">Scope (HEAD)</span>
        <span class="meta-value">{{truncate .HeadSHA 8}}</span>
      </div>
      <div class="meta-item">
        <span class="meta-label">Generated At</span>
        <span class="meta-value">{{.GeneratedAt.Format "2006-01-02 15:04:05 MST"}}</span>
      </div>
    </div>
  </header>

  {{if .Warnings}}
  <div class="warning-box">
    <strong>Evidence Notices:</strong>
    <ul style="margin-left: 1.25rem; margin-top: 0.5rem;">
      {{range .Warnings}}
      <li>{{.}}</li>
      {{end}}
    </ul>
  </div>
  {{end}}

  {{if .Task}}
  <section class="section">
    <h2 class="section-title">Linked Feature / Task</h2>
    <div class="card">
      <div class="card-header">
        <span class="commit-sha">[{{.Task.ID}}] {{.Task.Title}}</span>
        <span class="badge badge--info">{{.Task.Status}}</span>
      </div>
      {{if .Task.Objective}}<p style="margin-top:0.5rem; color: var(--text-secondary);"><strong>Objective:</strong> {{.Task.Objective}}</p>{{end}}
      {{if .Task.Notes}}<p style="margin-top:0.5rem; color: var(--text-secondary);"><strong>Notes:</strong> {{.Task.Notes}}</p>{{end}}
    </div>
  </section>
  {{end}}

  <section class="section">
    <h2 class="section-title">Commits & Evolution ({{len .Commits}})</h2>
    <div style="margin-bottom: 1rem; color: var(--text-secondary); font-size: 0.9rem;">
      Total: <span class="ins">+{{.TotalInsertions}}</span> / <span class="del">-{{.TotalDeletions}}</span> across {{.TotalFiles}} files.
    </div>
    {{range .Commits}}
    <div class="card">
      <div class="card-header">
        <span class="commit-sha">{{.ShortSHA}} &bull; {{.AuthorName}}</span>
        <span style="font-size: 0.8rem; color: var(--text-secondary);">{{.Timestamp.Format "2006-01-02 15:04"}}</span>
      </div>
      <div class="commit-msg">{{.Subject}}</div>
      <div class="commit-stats">
        {{if .FilesChanged}}
          {{.FilesChanged}} files changed (<span class="ins">+{{.Insertions}}</span>, <span class="del">-{{.Deletions}}</span>)
        {{end}}
      </div>
      {{if .ChangedFiles}}
      <div class="file-list">
        {{range .ChangedFiles}}
        <div>&bull; {{.}}</div>
        {{end}}
      </div>
      {{end}}
    </div>
    {{else}}
    <div class="card"><p style="color:var(--text-secondary);">No commits in this scope boundary.</p></div>
    {{end}}
  </section>

  {{if .Evidence}}
  <section class="section">
    <h2 class="section-title">Test & Build Evidence</h2>
    <div class="card">
      {{range .Evidence}}
      <div class="evidence-row">
        <div>
          <strong>{{.EvidenceType}} ({{.Command}})</strong>
          <div style="font-size:0.75rem; color:var(--text-secondary); font-family:var(--font-mono);">
            Commit: {{truncate .CommitSHA 8}} &bull; {{.CreatedAt.Format "15:04:05"}} ({{.DurationMs}}ms)
          </div>
        </div>
        <span class="badge {{if eq .Result "passed"}}badge--ok{{else}}badge--danger{{end}}">{{.Result}}</span>
      </div>
      {{end}}
    </div>
  </section>
  {{end}}

  {{if .Checkpoints}}
  <section class="section">
    <h2 class="section-title">Recorded Checkpoints</h2>
    <div class="card">
      {{range .Checkpoints}}
      <div class="evidence-row">
        <div>
          <div><strong>{{.Message}}</strong></div>
          <div style="font-size:0.75rem; color:var(--text-secondary); font-family:var(--font-mono);">
            {{.Branch}} ({{truncate .CommitSHA 8}}) &bull; {{.CreatedAt.Format "2006-01-02 15:04"}}
          </div>
        </div>
      </div>
      {{end}}
    </div>
  </section>
  {{end}}

  <footer>
    Generated by DevTrail &bull; Local-First Developer Activity Intelligence &bull; Verified Git Evidence
  </footer>
</div>
</body>
</html>`

func GetReportTemplate() (*template.Template, error) {
	funcMap := template.FuncMap{
		"truncate": func(s string, l int) string {
			if len(s) > l {
				return s[:l]
			}
			return s
		},
	}
	return template.New("report").Funcs(funcMap).Parse(BaseHTMLTemplate)
}
