package tests

import (
	"testing"
	"time"

	"github.com/donieltripura/devtrail/internal/core"
	"github.com/donieltripura/devtrail/internal/services"
)

// Classify derives everything the Changes view shows from a raw commit, so
// these tests pin the parsing rules: conventional subjects give type/scope,
// trailers give agent attribution, and parent counts give merges.

func TestClassifyConventionalCommit(t *testing.T) {
	cases := []struct {
		subject      string
		wantType     string
		wantScope    string
		wantHeadline string
		wantBreaking bool
	}{
		{"feat(cockpit): add digest view", "feat", "cockpit", "add digest view", false},
		{"fix: correct off-by-one", "fix", "", "correct off-by-one", false},
		{"refactor(api)!: drop legacy route", "refactor", "api", "drop legacy route", true},
		{"chore(deps): bump sqlite", "chore", "deps", "bump sqlite", false},
		{"Merge branch 'feature/x'", "other", "", "Merge branch 'feature/x'", false},
		{"just a plain message", "other", "", "just a plain message", false},
	}

	for _, tc := range cases {
		got := services.Classify(core.Commit{Subject: tc.subject, Message: tc.subject})
		if got.Type != tc.wantType {
			t.Errorf("%q: type = %q, want %q", tc.subject, got.Type, tc.wantType)
		}
		if got.Scope != tc.wantScope {
			t.Errorf("%q: scope = %q, want %q", tc.subject, got.Scope, tc.wantScope)
		}
		if got.Headline != tc.wantHeadline {
			t.Errorf("%q: headline = %q, want %q", tc.subject, got.Headline, tc.wantHeadline)
		}
		if got.Breaking != tc.wantBreaking {
			t.Errorf("%q: breaking = %v, want %v", tc.subject, got.Breaking, tc.wantBreaking)
		}
	}
}

func TestClassifyDetectsBreakingChangeFooter(t *testing.T) {
	got := services.Classify(core.Commit{
		Subject: "feat(api): new shape",
		Message: "feat(api): new shape\n\nBREAKING CHANGE: /api/status is now flat",
	})
	if !got.Breaking {
		t.Error("expected BREAKING CHANGE in the body to mark the change as breaking")
	}
}

// Agent attribution must work on commits written before DevTrail existed,
// which is the whole point of reading it from trailers.
func TestClassifyAttributesAgentFromTrailer(t *testing.T) {
	got := services.Classify(core.Commit{
		Subject:     "feat(core): add engine",
		AuthorName:  "Doniel Tripura",
		AuthorEmail: "dev@example.com",
		Message: "feat(core): add engine\n\nSome body text.\n\n" +
			"Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>",
	})

	if got.Author.IsAgent {
		t.Error("the human author must not be classified as an agent")
	}
	if len(got.Agents) != 1 {
		t.Fatalf("expected 1 credited agent, got %d", len(got.Agents))
	}
	if got.Agents[0].Agent != "claude" {
		t.Errorf("agent = %q, want %q", got.Agents[0].Agent, "claude")
	}
	if got.AttributedTo().Agent != "claude" {
		t.Error("a change co-authored by an agent should be attributed to that agent")
	}
}

func TestClassifyRecognisesSeveralAgents(t *testing.T) {
	for _, tc := range []struct{ trailer, want string }{
		{"Co-Authored-By: Claude <noreply@anthropic.com>", "claude"},
		{"Co-Authored-By: Copilot <copilot@github.com>", "copilot"},
		{"Co-Authored-By: ChatGPT <bot@openai.com>", "chatgpt"},
		{"Co-Authored-By: Gemini <gemini@google.com>", "gemini"},
		{"Co-Authored-By: Cursor Agent <agent@cursor.sh>", "cursor"},
	} {
		got := services.Classify(core.Commit{Subject: "fix: x", Message: "fix: x\n\n" + tc.trailer})
		if len(got.Agents) != 1 || got.Agents[0].Agent != tc.want {
			t.Errorf("%q -> %+v, want agent %q", tc.trailer, got.Agents, tc.want)
		}
	}
}

func TestClassifyWithoutTrailerIsHumanAttributed(t *testing.T) {
	got := services.Classify(core.Commit{
		Subject:     "fix: typo",
		AuthorName:  "Doniel Tripura",
		AuthorEmail: "dev@example.com",
		Message:     "fix: typo",
	})
	if len(got.Agents) != 0 {
		t.Errorf("expected no agents, got %+v", got.Agents)
	}
	if got.AttributedTo().IsAgent {
		t.Error("a commit with no agent trailer must be attributed to the human")
	}
}

func TestClassifyDeduplicatesRepeatedTrailers(t *testing.T) {
	got := services.Classify(core.Commit{
		Subject: "fix: x",
		Message: "fix: x\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n" +
			"Co-Authored-By: Claude <noreply@anthropic.com>",
	})
	if len(got.Agents) != 1 {
		t.Errorf("expected the duplicate trailer to collapse, got %d agents", len(got.Agents))
	}
}

func TestClassifyDetectsMerge(t *testing.T) {
	got := services.Classify(core.Commit{
		Subject:    "Merge branch 'feature/digest'",
		ParentSHAs: []string{"aaa", "bbb"},
		Message:    "Merge branch 'feature/digest'",
	})
	if !got.IsMerge {
		t.Error("two parents should mark the commit as a merge")
	}
	if got.MergedFrom != "feature/digest" {
		t.Errorf("merged_from = %q, want %q", got.MergedFrom, "feature/digest")
	}

	pr := services.Classify(core.Commit{
		Subject:    "Merge pull request #42 from acme/fix-login",
		ParentSHAs: []string{"aaa", "bbb"},
		Message:    "Merge pull request #42 from acme/fix-login",
	})
	if pr.MergedFrom != "acme/fix-login" {
		t.Errorf("merged_from = %q, want %q", pr.MergedFrom, "acme/fix-login")
	}

	single := services.Classify(core.Commit{Subject: "fix: x", ParentSHAs: []string{"aaa"}})
	if single.IsMerge {
		t.Error("a single-parent commit is not a merge")
	}
}

func TestValidGroupBy(t *testing.T) {
	for _, g := range []string{"day", "week", "month", "sprint", "feature", "merge", "author"} {
		if !services.ValidGroupBy(g) {
			t.Errorf("%q should be a valid grouping", g)
		}
	}
	for _, g := range []string{"", "year", "quarter", "bogus"} {
		if services.ValidGroupBy(g) {
			t.Errorf("%q should not be a valid grouping", g)
		}
	}
}

// A digest built over a real repository must group, total and attribute
// consistently: bucket totals have to add up to the digest total.
func TestDigestGroupingsAreConsistent(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	svc := services.NewDigestService(db, newCollector(repo))

	for _, group := range []services.GroupBy{
		services.GroupByDay, services.GroupByWeek, services.GroupByMonth,
		services.GroupByFeature, services.GroupByMerge, services.GroupByAuthor,
	} {
		d, err := svc.Build(services.DigestOptions{GroupBy: group})
		if err != nil {
			t.Fatalf("group %s: %v", group, err)
		}
		if d.Stats.Commits == 0 {
			t.Fatalf("group %s: expected at least the fixture commit", group)
		}

		total := 0
		for _, b := range d.Buckets {
			total += b.Stats.Commits
		}
		if total != d.Stats.Commits {
			t.Errorf("group %s: bucket commits sum to %d, digest reports %d",
				group, total, d.Stats.Commits)
		}
	}
}

// A linear history has no merges; the digest must say so rather than render
// an empty page with no explanation.
func TestDigestExplainsLinearHistory(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	svc := services.NewDigestService(db, newCollector(repo))
	d, err := svc.Build(services.DigestOptions{GroupBy: services.GroupByMerge})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Notes) == 0 {
		t.Error("expected a note explaining that the history has no merge commits")
	}
}

func TestDigestSinceFiltersOldCommits(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	svc := services.NewDigestService(db, newCollector(repo))
	d, err := svc.Build(services.DigestOptions{
		GroupBy: services.GroupByWeek,
		Since:   time.Now().AddDate(1, 0, 0), // a year in the future
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stats.Commits != 0 {
		t.Errorf("expected no commits after a future cutoff, got %d", d.Stats.Commits)
	}
}

// Sprint boundaries must be stable and contiguous: every day belongs to
// exactly one sprint, and sprint N+1 starts the instant sprint N ends.
func TestSprintBoundariesAreContiguous(t *testing.T) {
	anchor := time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local) // a Monday
	spec := services.SprintSpec{LengthDays: 14, Anchor: anchor, Prefix: "Sprint"}

	cases := []struct {
		day        time.Time
		wantNumber int
	}{
		{anchor, 1},
		{anchor.AddDate(0, 0, 13), 1}, // last day of sprint 1
		{anchor.AddDate(0, 0, 14), 2}, // first day of sprint 2
		{anchor.AddDate(0, 0, 27), 2},
		{anchor.AddDate(0, 0, 28), 3},
	}
	for _, tc := range cases {
		n, start, end := spec.SprintFor(tc.day)
		if n != tc.wantNumber {
			t.Errorf("%s: sprint %d, want %d", tc.day.Format("2006-01-02"), n, tc.wantNumber)
		}
		if tc.day.Before(start) || tc.day.After(end) {
			t.Errorf("%s falls outside its own sprint window %s..%s",
				tc.day.Format("2006-01-02"), start.Format("2006-01-02"), end.Format("2006-01-02"))
		}
	}

	// Sprint N+1 must begin exactly where sprint N ended.
	_, _, end1 := spec.SprintFor(anchor)
	_, start2, _ := spec.SprintFor(anchor.AddDate(0, 0, 14))
	if start2.Sub(end1) != time.Nanosecond {
		t.Errorf("gap between sprints: sprint 1 ends %v, sprint 2 starts %v", end1, start2)
	}
}

func TestSprintHonoursConfiguredLength(t *testing.T) {
	anchor := time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local)
	weekly := services.SprintSpec{LengthDays: 7, Anchor: anchor}

	if n, _, _ := weekly.SprintFor(anchor.AddDate(0, 0, 7)); n != 2 {
		t.Errorf("with a 7-day cadence, day 7 should be sprint 2, got %d", n)
	}
	if n, _, _ := weekly.SprintFor(anchor.AddDate(0, 0, 6)); n != 1 {
		t.Errorf("with a 7-day cadence, day 6 should still be sprint 1, got %d", n)
	}
}

// Commits older than the anchor must not land in a zero or negative sprint.
func TestSprintClampsCommitsBeforeAnchor(t *testing.T) {
	anchor := time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local)
	spec := services.SprintSpec{LengthDays: 14, Anchor: anchor}

	n, _, _ := spec.SprintFor(anchor.AddDate(0, 0, -30))
	if n != 1 {
		t.Errorf("a commit before the anchor should clamp to sprint 1, got %d", n)
	}
}

func TestSprintGroupingWorksWithoutConfiguredAnchor(t *testing.T) {
	repo := newTestRepo(t)
	db, cleanup := newTestStore(t)
	defer cleanup()

	svc := services.NewDigestService(db, newCollector(repo))
	d, err := svc.Build(services.DigestOptions{
		GroupBy: services.GroupBySprint,
		Sprint:  services.SprintSpec{}, // no anchor, no length: all defaults
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Buckets) == 0 {
		t.Fatal("expected at least one sprint bucket with zero configuration")
	}
	if d.Buckets[0].Start == nil || d.Buckets[0].End == nil {
		t.Error("sprint buckets must carry their window")
	}

	total := 0
	for _, b := range d.Buckets {
		total += b.Stats.Commits
	}
	if total != d.Stats.Commits {
		t.Errorf("sprint buckets sum to %d, digest reports %d", total, d.Stats.Commits)
	}
}
