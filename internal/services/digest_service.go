package services

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/donieltripura/devtrail/internal/collector"
	"github.com/donieltripura/devtrail/internal/core"
)

// DigestService answers "what changed, and where did it come from" by deriving
// structure from git metadata that already exists in history: conventional
// commit subjects give the type and feature scope, Co-Authored-By trailers give
// agent attribution, and parent counts give merges. Nothing here requires the
// developer to adopt a new workflow, and it works retroactively on commits made
// before DevTrail was installed.
type DigestService struct {
	Store StorageInterface
	Git   *collector.GitCollector
}

func NewDigestService(store StorageInterface, git *collector.GitCollector) *DigestService {
	return &DigestService{Store: store, Git: git}
}

// GroupBy selects how changes are bucketed.
type GroupBy string

const (
	GroupByDay     GroupBy = "day"
	GroupByWeek    GroupBy = "week"
	GroupByMonth   GroupBy = "month"
	GroupBySprint  GroupBy = "sprint"
	GroupByFeature GroupBy = "feature"
	GroupByMerge   GroupBy = "merge"
	GroupByAuthor  GroupBy = "author"
)

// ValidGroupBy reports whether g is a grouping the service implements.
func ValidGroupBy(g string) bool {
	switch GroupBy(g) {
	case GroupByDay, GroupByWeek, GroupByMonth, GroupBySprint, GroupByFeature, GroupByMerge, GroupByAuthor:
		return true
	}
	return false
}

// Actor is a person or agent credited on a change.
type Actor struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	IsAgent bool   `json:"is_agent"`
	// Agent is a normalised identifier ("claude", "copilot") when IsAgent.
	Agent string `json:"agent,omitempty"`
}

// Stats summarises the size of a set of changes.
type Stats struct {
	Commits    int `json:"commits"`
	Files      int `json:"files"`
	Insertions int `json:"insertions"`
	Deletions  int `json:"deletions"`
}

// Change is one commit, enriched with everything we could infer about it.
type Change struct {
	SHA        string    `json:"sha"`
	ShortSHA   string    `json:"short_sha"`
	Subject    string    `json:"subject"`
	Headline   string    `json:"headline"` // subject with the conventional prefix removed
	Type       string    `json:"type"`     // feat, fix, chore, …; "other" when not conventional
	Scope      string    `json:"scope,omitempty"`
	Breaking   bool      `json:"breaking"`
	Timestamp  time.Time `json:"timestamp"`
	Author     Actor     `json:"author"`
	Agents     []Actor   `json:"agents,omitempty"`
	IsMerge    bool      `json:"is_merge"`
	MergedFrom string    `json:"merged_from,omitempty"`
	// Task is set when this commit is linked to a tracked task.
	Task  *TaskRef `json:"task,omitempty"`
	Stats Stats    `json:"stats"`
	Files []string `json:"files,omitempty"`
}

// AttributedTo returns the key this change is credited to when grouping by
// author: the first agent if one is credited, otherwise the human author.
func (c Change) AttributedTo() Actor {
	if len(c.Agents) > 0 {
		return c.Agents[0]
	}
	return c.Author
}

// Bucket is one group of changes.
type Bucket struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Start and End are set for time-based groupings.
	Start     *time.Time     `json:"start,omitempty"`
	End       *time.Time     `json:"end,omitempty"`
	Stats     Stats          `json:"stats"`
	Types     map[string]int `json:"types"`
	Agents    map[string]int `json:"agents"`
	HumanOnly int            `json:"human_only"`
	Changes   []Change       `json:"changes"`
}

// AgentSummary totals one actor's contribution across the whole digest.
type AgentSummary struct {
	Actor Actor `json:"actor"`
	Stats Stats `json:"stats"`
}

// Digest is the full response for a grouping request.
type Digest struct {
	GroupBy      string         `json:"group_by"`
	Ref          string         `json:"ref"`
	RangeStart   time.Time      `json:"range_start"`
	RangeEnd     time.Time      `json:"range_end"`
	Stats        Stats          `json:"stats"`
	Types        map[string]int `json:"types"`
	Buckets      []Bucket       `json:"buckets"`
	Contributors []AgentSummary `json:"contributors"`
	// Notes carries anything the reader should know about how the digest was
	// derived, for example that the history contains no merge commits.
	Notes []string `json:"notes,omitempty"`
}

// conventionalRe matches "type(scope)!: subject" with scope and ! optional.
var conventionalRe = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^)]*)\))?(!)?:\s*(.+)$`)

// trailerRe matches a git trailer line such as "Co-Authored-By: Name <email>".
var trailerRe = regexp.MustCompile(`(?mi)^\s*(Co-Authored-By|Assisted-By|Generated-By)\s*:\s*(.+?)\s*$`)

// actorRe splits "Name <email>".
var actorRe = regexp.MustCompile(`^(.*?)\s*<([^>]*)>\s*$`)

// mergeFromRe pulls the source branch out of common merge subjects.
var mergeFromRe = regexp.MustCompile(`(?i)^Merge (?:remote-tracking )?branch '([^']+)'|^Merge pull request #\d+ from (\S+)`)

// knownAgents maps a lowercase needle to a normalised agent id. Matched against
// both the credited name and the email.
var knownAgents = []struct{ needle, id string }{
	{"claude", "claude"},
	{"anthropic", "claude"},
	{"copilot", "copilot"},
	{"chatgpt", "chatgpt"},
	{"openai", "chatgpt"},
	{"codex", "chatgpt"},
	{"gemini", "gemini"},
	{"cursor", "cursor"},
	{"devin", "devin"},
	{"aider", "aider"},
}

// classifyActor builds an Actor, detecting whether the credit is an AI agent.
func classifyActor(name, email string) Actor {
	hay := strings.ToLower(name + " " + email)
	for _, a := range knownAgents {
		if strings.Contains(hay, a.needle) {
			return Actor{Name: strings.TrimSpace(name), Email: email, IsAgent: true, Agent: a.id}
		}
	}
	// A noreply bot address is an agent even when we cannot name it.
	if strings.Contains(hay, "[bot]") || strings.HasPrefix(strings.ToLower(email), "noreply@") {
		return Actor{Name: strings.TrimSpace(name), Email: email, IsAgent: true, Agent: "bot"}
	}
	return Actor{Name: strings.TrimSpace(name), Email: email}
}

// parseTrailers extracts credited co-authors from a commit message body.
func parseTrailers(message string) []Actor {
	var out []Actor
	seen := map[string]bool{}
	for _, m := range trailerRe.FindAllStringSubmatch(message, -1) {
		raw := strings.TrimSpace(m[2])
		name, email := raw, ""
		if am := actorRe.FindStringSubmatch(raw); am != nil {
			name, email = strings.TrimSpace(am[1]), strings.TrimSpace(am[2])
		}
		actor := classifyActor(name, email)
		key := strings.ToLower(actor.Name + "|" + actor.Email)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, actor)
	}
	return out
}

// Classify turns a raw commit into an enriched Change.
func Classify(c core.Commit) Change {
	ch := Change{
		SHA:       c.SHA,
		ShortSHA:  c.ShortSHA,
		Subject:   c.Subject,
		Headline:  c.Subject,
		Type:      "other",
		Timestamp: c.Timestamp,
		Author:    classifyActor(c.AuthorName, c.AuthorEmail),
		IsMerge:   len(c.ParentSHAs) > 1,
		Stats: Stats{
			Commits:    1,
			Files:      c.FilesChanged,
			Insertions: c.Insertions,
			Deletions:  c.Deletions,
		},
		Files: c.ChangedFiles,
	}

	if m := conventionalRe.FindStringSubmatch(c.Subject); m != nil {
		ch.Type = strings.ToLower(m[1])
		ch.Scope = m[2]
		ch.Breaking = m[3] == "!"
		ch.Headline = m[4]
	}
	if strings.Contains(c.Message, "BREAKING CHANGE") {
		ch.Breaking = true
	}

	// Credit agents from trailers, excluding a trailer that merely repeats the
	// commit author.
	for _, a := range parseTrailers(c.Message) {
		if !a.IsAgent {
			continue
		}
		ch.Agents = append(ch.Agents, a)
	}

	if ch.IsMerge {
		if m := mergeFromRe.FindStringSubmatch(c.Subject); m != nil {
			if m[1] != "" {
				ch.MergedFrom = m[1]
			} else {
				ch.MergedFrom = m[2]
			}
		}
	}

	return ch
}

// SprintSpec describes the sprint cadence used by GroupBySprint.
type SprintSpec struct {
	LengthDays int
	// Anchor is the first day of sprint 1. When zero, Build anchors to the
	// Monday on or before the oldest commit in range, so sprint grouping works
	// with no configuration.
	Anchor time.Time
	Prefix string
}

func (s SprintSpec) normalise(oldest time.Time) SprintSpec {
	if s.LengthDays <= 0 {
		s.LengthDays = 14
	}
	if s.Prefix == "" {
		s.Prefix = "Sprint"
	}
	if s.Anchor.IsZero() && !oldest.IsZero() {
		t := oldest.Local()
		offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
		s.Anchor = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, -offset)
	}
	s.Anchor = time.Date(s.Anchor.Year(), s.Anchor.Month(), s.Anchor.Day(), 0, 0, 0, 0, time.Local)
	return s
}

// SprintFor returns the 1-based sprint number and window containing t.
// Commits before the anchor fall into sprint 1 rather than a negative sprint.
func (s SprintSpec) SprintFor(t time.Time) (number int, start, end time.Time) {
	day := t.Local()
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())

	elapsed := int(day.Sub(s.Anchor).Hours() / 24)
	index := elapsed / s.LengthDays
	if elapsed < 0 {
		index = 0
	}
	start = s.Anchor.AddDate(0, 0, index*s.LengthDays)
	end = start.AddDate(0, 0, s.LengthDays).Add(-time.Nanosecond)
	return index + 1, start, end
}

// DigestOptions controls what Build collects.
type DigestOptions struct {
	GroupBy GroupBy
	Ref     string // branch or ref; "" means the current checkout, "--all" means every ref
	Since   time.Time
	Limit   int
	Sprint  SprintSpec
}

// Build assembles a digest for the requested grouping.
func (s *DigestService) Build(opts DigestOptions) (*Digest, error) {
	if opts.Limit <= 0 {
		opts.Limit = 400
	}
	if opts.GroupBy == "" {
		opts.GroupBy = GroupByWeek
	}

	commits, err := s.Git.LogSince(opts.Ref, opts.Since, opts.Limit)
	if err != nil {
		return nil, err
	}

	// Attach the task each commit delivered, where one is linked.
	index, err := BuildCommitIndex(s.Store)
	if err != nil {
		index = CommitIndex{}
	}

	changes := make([]Change, 0, len(commits))
	for _, c := range commits {
		ch := Classify(c)
		if ref, ok := index[ch.SHA]; ok {
			r := ref
			ch.Task = &r
		}
		changes = append(changes, ch)
	}

	d := &Digest{
		GroupBy: string(opts.GroupBy),
		Ref:     opts.Ref,
		Types:   map[string]int{},
		Buckets: []Bucket{},
	}
	if len(changes) == 0 {
		return d, nil
	}

	// Overall range and totals.
	d.RangeEnd = changes[0].Timestamp
	d.RangeStart = changes[len(changes)-1].Timestamp
	files := map[string]bool{}
	contrib := map[string]*AgentSummary{}
	for _, ch := range changes {
		d.Stats.Commits++
		d.Stats.Insertions += ch.Stats.Insertions
		d.Stats.Deletions += ch.Stats.Deletions
		d.Types[ch.Type]++
		for _, f := range ch.Files {
			files[f] = true
		}
		actor := ch.AttributedTo()
		key := actorKey(actor)
		if contrib[key] == nil {
			contrib[key] = &AgentSummary{Actor: actor}
		}
		contrib[key].Stats.Commits++
		contrib[key].Stats.Insertions += ch.Stats.Insertions
		contrib[key].Stats.Deletions += ch.Stats.Deletions
		contrib[key].Stats.Files += ch.Stats.Files
	}
	d.Stats.Files = len(files)

	for _, v := range contrib {
		d.Contributors = append(d.Contributors, *v)
	}
	sort.Slice(d.Contributors, func(i, j int) bool {
		return d.Contributors[i].Stats.Commits > d.Contributors[j].Stats.Commits
	})

	switch opts.GroupBy {
	case GroupBySprint:
		spec := opts.Sprint.normalise(d.RangeStart)
		d.Buckets = groupBySprint(changes, spec)
	case GroupByMerge:
		d.Buckets, d.Notes = s.groupByMerge(changes)
	case GroupByFeature:
		d.Buckets = groupByFeature(changes)
	case GroupByAuthor:
		d.Buckets = groupByAuthor(changes)
	default:
		d.Buckets = groupByPeriod(changes, opts.GroupBy)
	}

	return d, nil
}

func actorKey(a Actor) string {
	if a.IsAgent && a.Agent != "" {
		return "agent:" + a.Agent
	}
	return "human:" + strings.ToLower(a.Email+"|"+a.Name)
}

// finishBucket computes a bucket's rollups from its changes.
func finishBucket(b *Bucket) {
	files := map[string]bool{}
	b.Types = map[string]int{}
	b.Agents = map[string]int{}
	for _, ch := range b.Changes {
		b.Stats.Commits++
		b.Stats.Insertions += ch.Stats.Insertions
		b.Stats.Deletions += ch.Stats.Deletions
		b.Types[ch.Type]++
		for _, f := range ch.Files {
			files[f] = true
		}
		if len(ch.Agents) == 0 {
			b.HumanOnly++
		}
		for _, a := range ch.Agents {
			b.Agents[a.Agent]++
		}
	}
	b.Stats.Files = len(files)
}

// periodKey returns the bucket key, label and window for a timestamp.
func periodKey(t time.Time, g GroupBy) (key, label string, start, end time.Time) {
	t = t.Local()
	switch g {
	case GroupByDay:
		start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		end = start.AddDate(0, 0, 1).Add(-time.Nanosecond)
		return start.Format("2006-01-02"), start.Format("Mon 2 Jan 2006"), start, end
	case GroupByMonth:
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
		end = start.AddDate(0, 1, 0).Add(-time.Nanosecond)
		return start.Format("2006-01"), start.Format("January 2006"), start, end
	default: // week
		offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
		start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, -offset)
		end = start.AddDate(0, 0, 7).Add(-time.Nanosecond)
		isoYear, isoWeek := start.ISOWeek()
		return fmt.Sprintf("%d-W%02d", isoYear, isoWeek),
			fmt.Sprintf("Week %d · %s", isoWeek, start.Format("2 Jan 2006")), start, end
	}
}

func groupByPeriod(changes []Change, g GroupBy) []Bucket {
	order := []string{}
	byKey := map[string]*Bucket{}

	for _, ch := range changes {
		key, label, start, end := periodKey(ch.Timestamp, g)
		if byKey[key] == nil {
			s, e := start, end
			byKey[key] = &Bucket{Key: key, Label: label, Start: &s, End: &e}
			order = append(order, key)
		}
		byKey[key].Changes = append(byKey[key].Changes, ch)
	}

	out := make([]Bucket, 0, len(order))
	for _, k := range order {
		finishBucket(byKey[k])
		out = append(out, *byKey[k])
	}
	return out
}

func groupBySprint(changes []Change, spec SprintSpec) []Bucket {
	order := []string{}
	byKey := map[string]*Bucket{}

	for _, ch := range changes {
		n, start, end := spec.SprintFor(ch.Timestamp)
		key := fmt.Sprintf("sprint-%d", n)
		if byKey[key] == nil {
			s, e := start, end
			byKey[key] = &Bucket{
				Key:   key,
				Label: fmt.Sprintf("%s %d", spec.Prefix, n),
				Start: &s, End: &e,
			}
			order = append(order, key)
		}
		byKey[key].Changes = append(byKey[key].Changes, ch)
	}

	out := make([]Bucket, 0, len(order))
	for _, k := range order {
		finishBucket(byKey[k])
		out = append(out, *byKey[k])
	}
	return out
}

func groupByFeature(changes []Change) []Bucket {
	order := []string{}
	byKey := map[string]*Bucket{}

	for _, ch := range changes {
		key := ch.Scope
		label := ch.Scope
		if key == "" {
			// Fall back to the commit type so unscoped work still lands somewhere
			// meaningful rather than in one undifferentiated pile.
			key = "type:" + ch.Type
			label = ch.Type + " (no scope)"
			if ch.Type == "other" {
				key, label = "unclassified", "Unclassified"
			}
		}
		if byKey[key] == nil {
			byKey[key] = &Bucket{Key: key, Label: label}
			order = append(order, key)
		}
		byKey[key].Changes = append(byKey[key].Changes, ch)
	}

	out := make([]Bucket, 0, len(order))
	for _, k := range order {
		finishBucket(byKey[k])
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stats.Commits > out[j].Stats.Commits })
	return out
}

func groupByAuthor(changes []Change) []Bucket {
	order := []string{}
	byKey := map[string]*Bucket{}

	for _, ch := range changes {
		actor := ch.AttributedTo()
		key := actorKey(actor)
		label := actor.Name
		if actor.IsAgent {
			label = actor.Name + " (agent)"
		}
		if byKey[key] == nil {
			byKey[key] = &Bucket{Key: key, Label: label}
			order = append(order, key)
		}
		byKey[key].Changes = append(byKey[key].Changes, ch)
	}

	out := make([]Bucket, 0, len(order))
	for _, k := range order {
		finishBucket(byKey[k])
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stats.Commits > out[j].Stats.Commits })
	return out
}

// groupByMerge makes each merge commit a bucket containing the commits it
// brought in (first-parent..second-parent). Commits not reachable through any
// merge in range are collected under "Direct to branch".
func (s *DigestService) groupByMerge(changes []Change) ([]Bucket, []string) {
	var notes []string
	var out []Bucket

	claimed := map[string]bool{}
	merges := 0

	for _, ch := range changes {
		if !ch.IsMerge {
			continue
		}
		merges++
		claimed[ch.SHA] = true

		b := Bucket{Key: ch.ShortSHA, Label: mergeLabel(ch)}
		start, end := ch.Timestamp, ch.Timestamp
		b.Start, b.End = &start, &end

		// Ask git which commits this merge introduced.
		if brought, err := s.Git.CommitsBroughtInByMerge(ch.SHA, 200); err == nil {
			for _, c := range brought {
				sub := Classify(c)
				claimed[sub.SHA] = true
				b.Changes = append(b.Changes, sub)
			}
		}
		if len(b.Changes) == 0 {
			b.Changes = append(b.Changes, ch)
		}
		finishBucket(&b)
		out = append(out, b)
	}

	var direct []Change
	for _, ch := range changes {
		if !claimed[ch.SHA] {
			direct = append(direct, ch)
		}
	}
	if len(direct) > 0 {
		b := Bucket{Key: "direct", Label: "Direct to branch (no merge)", Changes: direct}
		finishBucket(&b)
		out = append(out, b)
	}

	if merges == 0 {
		notes = append(notes,
			"This history has no merge commits in range — it is linear (rebase or fast-forward). "+
				"Every change is listed under \"Direct to branch\". Group by week or feature instead.")
	}
	return out, notes
}

func mergeLabel(ch Change) string {
	if ch.MergedFrom != "" {
		return "Merge: " + ch.MergedFrom
	}
	return ch.Headline
}

// TypeLabel gives a human name for a conventional commit type.
func TypeLabel(t string) string {
	switch t {
	case "feat":
		return "Feature"
	case "fix":
		return "Fix"
	case "refactor":
		return "Refactor"
	case "perf":
		return "Performance"
	case "docs":
		return "Docs"
	case "test":
		return "Tests"
	case "chore":
		return "Chore"
	case "build":
		return "Build"
	case "ci":
		return "CI"
	case "style":
		return "Style"
	case "revert":
		return "Revert"
	default:
		return "Other"
	}
}
