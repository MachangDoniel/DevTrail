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
	for _, g := range []string{"day", "week", "month", "feature", "merge", "author"} {
		if !services.ValidGroupBy(g) {
			t.Errorf("%q should be a valid grouping", g)
		}
	}
	for _, g := range []string{"", "year", "sprint", "bogus"} {
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
