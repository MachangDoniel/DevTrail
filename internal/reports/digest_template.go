package reports

// DigestHTMLTemplate renders a change digest as a self-contained page: no
// scripts, no external requests, and the same design tokens as the dashboard.
const DigestHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}} — {{.ProjectName}}</title>
<style>
:root {
  --bg-primary:   #090c13;
  --bg-card:      #131926;
  --bg-sunken:    #06080d;
  --border:       rgba(255, 255, 255, 0.09);
  --text-primary:   #f1f5f9;
  --text-secondary: #a8b5c6;
  --text-dim:       #8899ad;
  --accent-cyan:    #38bdf8;
  --accent-green:   #34d399;
  --accent-amber:   #fbbf24;
  --accent-red:     #fb7185;
  --accent-purple:  #c084fc;
  --font-mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Monaco, Consolas, monospace;
  --font-sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
}
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  background: var(--bg-primary); color: var(--text-primary);
  font-family: var(--font-sans); font-size: 14px; line-height: 1.5;
  padding: 2rem 1rem; -webkit-font-smoothing: antialiased;
}
.container { max-width: 1000px; margin: 0 auto; }
header { border-bottom: 1px solid var(--border); padding-bottom: 1.5rem; margin-bottom: 2rem; }
h1 { font-size: 1.75rem; font-weight: 700; letter-spacing: -0.02em; margin-bottom: 0.35rem; }
.sub { color: var(--text-dim); font-size: 0.8125rem; }
.badge {
  display: inline-flex; align-items: center; gap: 0.25rem;
  font-size: 0.75rem; font-weight: 700; padding: 2px 0.5rem;
  border-radius: 9999px; border: 1px solid transparent; white-space: nowrap;
}
.type-feat     { background: rgba(52,211,153,.13); color: var(--accent-green);  border-color: rgba(52,211,153,.3); }
.type-fix      { background: rgba(251,191,36,.13); color: var(--accent-amber);  border-color: rgba(251,191,36,.3); }
.type-refactor { background: rgba(192,132,252,.13); color: var(--accent-purple); border-color: rgba(192,132,252,.3); }
.type-perf     { background: rgba(56,189,248,.13); color: var(--accent-cyan);   border-color: rgba(56,189,248,.3); }
.type-other    { background: rgba(255,255,255,.07); color: var(--text-secondary); border-color: var(--border); }
.badge-scope   { background: rgba(56,189,248,.13); color: var(--accent-cyan);   border-color: rgba(56,189,248,.3); }
.badge-agent   { background: rgba(192,132,252,.13); color: var(--accent-purple); border-color: rgba(192,132,252,.3); }

.metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
.metric { background: var(--bg-card); border: 1px solid var(--border); border-radius: 10px; padding: 1rem; }
.metric-label { font-size: 0.75rem; font-weight: 700; color: var(--text-dim); text-transform: uppercase; letter-spacing: .06em; }
.metric-value { font-size: 1.5rem; font-weight: 800; font-family: var(--font-mono); margin-top: .25rem; }
.metric-note { font-size: 0.75rem; color: var(--text-dim); margin-top: .25rem; }

.split-bar { display: flex; height: 10px; border-radius: 9999px; overflow: hidden; background: rgba(255,255,255,.07); margin: .75rem 0 .5rem; }
.split-seg--human { background: var(--accent-cyan); }
.split-seg--agent { background: var(--accent-purple); }
.legend { display: flex; gap: 1.5rem; font-size: 0.75rem; color: var(--text-secondary); flex-wrap: wrap; }
.dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; margin-right: .35rem; }

.card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 10px; margin-bottom: 1.5rem; overflow: hidden; }
.card-head { padding: 1rem; border-bottom: 1px solid var(--border); display: flex; justify-content: space-between; align-items: baseline; gap: .75rem; flex-wrap: wrap; }
.card-title { font-size: 0.95rem; font-weight: 700; }
.card-sub { font-size: 0.75rem; color: var(--text-dim); margin-top: 2px; }
.chips { display: flex; gap: .25rem; flex-wrap: wrap; }
.card-body { padding: 1rem; }

.change { display: grid; grid-template-columns: 76px 1fr auto; gap: .75rem; padding: .75rem 1rem; border-bottom: 1px solid rgba(255,255,255,.045); }
.change:last-child { border-bottom: none; }
.change-sha { font-family: var(--font-mono); font-size: 0.75rem; color: var(--text-dim); }
.change-headline { font-size: 0.8125rem; font-weight: 600; }
.change-meta { display: flex; gap: .5rem; flex-wrap: wrap; align-items: center; margin-top: .5rem; font-size: 0.75rem; color: var(--text-dim); }
.change-stats { font-family: var(--font-mono); font-size: 0.75rem; text-align: right; white-space: nowrap; }
.add { color: var(--accent-green); } .del { color: var(--accent-red); }

.dist-item { margin-bottom: .75rem; }
.dist-head { display: flex; justify-content: space-between; font-size: 0.75rem; font-weight: 600; margin-bottom: .25rem; }
.dist-track { height: 6px; background: rgba(255,255,255,.06); border-radius: 9999px; overflow: hidden; }
.dist-fill { height: 100%; border-radius: 9999px; }

.note { background: rgba(251,191,36,.13); border: 1px solid rgba(251,191,36,.35); color: var(--accent-amber);
        border-radius: 8px; padding: .75rem 1rem; font-size: 0.8125rem; margin-bottom: 1.5rem; }
footer { margin-top: 2.5rem; padding-top: 1.25rem; border-top: 1px solid var(--border); color: var(--text-dim); font-size: 0.75rem; }
@media (max-width: 640px) { .change { grid-template-columns: 1fr; } .change-stats { text-align: left; } }
@media print { body { background: #fff; color: #000; } .card, .metric { border-color: #ccc; } }
</style>
</head>
<body>
<div class="container">
  <header>
    <h1>{{.Title}}</h1>
    <div class="sub">
      {{.ProjectName}} · grouped by {{groupLabel .GroupBy}} ·
      {{if not .Digest.RangeStart.IsZero}}{{shortDate .Digest.RangeStart}} – {{shortDate .Digest.RangeEnd}} · {{end}}
      generated {{dateTime .GeneratedAt}}
    </div>
  </header>

  {{range .Digest.Notes}}<div class="note">{{.}}</div>{{end}}

  <div class="metrics">
    <div class="metric">
      <div class="metric-label">Changes</div>
      <div class="metric-value">{{.Digest.Stats.Commits}}</div>
      <div class="metric-note">{{len .Digest.Buckets}} group(s)</div>
    </div>
    <div class="metric">
      <div class="metric-label">Files touched</div>
      <div class="metric-value" style="color:var(--accent-amber)">{{.Digest.Stats.Files}}</div>
    </div>
    <div class="metric">
      <div class="metric-label">Lines added</div>
      <div class="metric-value add">+{{.Digest.Stats.Insertions}}</div>
      <div class="metric-note">&minus;{{.Digest.Stats.Deletions}} removed</div>
    </div>
    <div class="metric">
      <div class="metric-label">With an agent</div>
      <div class="metric-value" style="color:var(--accent-purple)">{{agentCommits .Digest}}</div>
      <div class="metric-note">{{pct (agentCommits .Digest) .Digest.Stats.Commits}}% of changes</div>
    </div>
  </div>

  {{if .Digest.Contributors}}
  <div class="card">
    <div class="card-head"><div class="card-title">Who changed it</div></div>
    <div class="card-body">
      <div class="split-bar">
        <div class="split-seg--human" style="width:{{pct (humanCommits .Digest) .Digest.Stats.Commits}}%"></div>
        <div class="split-seg--agent" style="width:{{pct (agentCommits .Digest) .Digest.Stats.Commits}}%"></div>
      </div>
      <div class="legend">
        <span><span class="dot" style="background:var(--accent-cyan)"></span>{{humanCommits .Digest}} by people</span>
        <span><span class="dot" style="background:var(--accent-purple)"></span>{{agentCommits .Digest}} with an agent</span>
      </div>
      <div style="margin-top:1rem">
      {{range .Digest.Contributors}}
        <div class="dist-item">
          <div class="dist-head">
            <span>{{.Actor.Name}}{{if .Actor.IsAgent}} <span class="badge badge-agent">agent</span>{{end}}</span>
            <span>{{.Stats.Commits}} change(s) · <span class="add">+{{.Stats.Insertions}}</span> <span class="del">&minus;{{.Stats.Deletions}}</span></span>
          </div>
          <div class="dist-track">
            <div class="dist-fill" style="width:{{pct .Stats.Commits $.Digest.Stats.Commits}}%;background:{{if .Actor.IsAgent}}var(--accent-purple){{else}}var(--accent-cyan){{end}}"></div>
          </div>
        </div>
      {{end}}
      </div>
    </div>
  </div>
  {{end}}

  {{range .Digest.Buckets}}
  <div class="card">
    <div class="card-head">
      <div>
        <div class="card-title">{{.Label}}</div>
        <div class="card-sub">
          {{if .Start}}{{shortDate .Start}} – {{shortDate .End}} · {{end}}
          {{.Stats.Commits}} change(s) · {{.Stats.Files}} file(s) ·
          <span class="add">+{{.Stats.Insertions}}</span> <span class="del">&minus;{{.Stats.Deletions}}</span>
        </div>
      </div>
      <div class="chips">
        {{range $t, $n := .Types}}<span class="badge {{typeClass $t}}">{{typeLabel $t}} {{$n}}</span>{{end}}
        {{range $a, $n := .Agents}}<span class="badge badge-agent">{{$a}} {{$n}}</span>{{end}}
      </div>
    </div>
    {{range .Changes}}
    <div class="change">
      <span class="change-sha">{{.ShortSHA}}</span>
      <div>
        <div class="change-headline">{{.Headline}}</div>
        <div class="change-meta">
          <span class="badge {{typeClass .Type}}">{{typeLabel .Type}}</span>
          {{if .Scope}}<span class="badge badge-scope">{{.Scope}}</span>{{end}}
          {{if .Breaking}}<span class="badge" style="background:rgba(251,113,133,.13);color:var(--accent-red);border-color:rgba(251,113,133,.3)">Breaking</span>{{end}}
          {{range .Agents}}<span class="badge badge-agent">{{.Name}}</span>{{end}}
          <span>{{dateTime .Timestamp}}</span>
        </div>
      </div>
      <div class="change-stats">
        <span class="add">+{{.Stats.Insertions}}</span> <span class="del">&minus;{{.Stats.Deletions}}</span>
        <div style="color:var(--text-dim)">{{.Stats.Files}} file(s)</div>
      </div>
    </div>
    {{end}}
  </div>
  {{end}}

  <footer>
    Generated by DevTrail · derived from local git history · no external requests
  </footer>
</div>
</body>
</html>`
