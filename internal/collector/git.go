package collector

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/donieltripura/devtrail/internal/core"
)

type GitCollector struct {
	RepoDir string
}

func NewGitCollector(repoDir string) *GitCollector {
	return &GitCollector{RepoDir: repoDir}
}

// run runs a read-only git command and returns standard output.
func (g *GitCollector) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.RepoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// IsGitRepo verifies that the directory is inside a Git repository.
func (g *GitCollector) IsGitRepo() (bool, string) {
	out, err := g.run("rev-parse", "--show-toplevel")
	if err != nil {
		return false, ""
	}
	return true, out
}

// CurrentBranch returns the name of the currently checked out branch or detached ref.
func (g *GitCollector) CurrentBranch() (string, error) {
	out, err := g.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return out, nil
}

// CurrentHeadSHA returns the full SHA of HEAD.
func (g *GitCollector) CurrentHeadSHA() (string, error) {
	out, err := g.run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return out, nil
}

// MergeBase finds the best common ancestor commit between two refs.
func (g *GitCollector) MergeBase(baseRef, headRef string) (string, error) {
	out, err := g.run("merge-base", baseRef, headRef)
	if err != nil {
		return "", err
	}
	return out, nil
}

// WorkingTreeSummary returns counts of staged, modified, and untracked files.
func (g *GitCollector) WorkingTreeSummary() (staged int, modified int, untracked int, clean bool, err error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return 0, 0, 0, false, err
	}
	if out == "" {
		return 0, 0, 0, true, nil
	}

	lines := strings.Split(out, "\n")
	for _, l := range lines {
		if len(l) < 2 {
			continue
		}
		x := l[0]
		y := l[1]
		if x == '?' && y == '?' {
			untracked++
		} else {
			if x != ' ' && x != '?' {
				staged++
			}
			if y != ' ' && y != '?' {
				modified++
			}
		}
	}
	return staged, modified, untracked, false, nil
}

// ListWorktrees discovers all active Git worktrees using --porcelain.
func (g *GitCollector) ListWorktrees() ([]core.Worktree, error) {
	out, err := g.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var worktrees []core.Worktree
	var current core.Worktree
	lines := strings.Split(out, "\n")
	now := time.Now().UTC()

	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			if current.Path != "" {
				current.LastObservedAt = now
				current.Status = "active"
				worktrees = append(worktrees, current)
				current = core.Worktree{}
			}
			continue
		}
		parts := strings.SplitN(l, " ", 2)
		key := parts[0]
		val := ""
		if len(parts) > 1 {
			val = parts[1]
		}

		switch key {
		case "worktree":
			current.Path = val
		case "HEAD":
			current.HeadSHA = val
		case "branch":
			current.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "bare":
			current.IsBare = true
		case "locked":
			current.IsLocked = true
		}
	}
	if current.Path != "" {
		current.LastObservedAt = now
		current.Status = "active"
		worktrees = append(worktrees, current)
	}

	return worktrees, nil
}

// ListBranches returns all local branches.
func (g *GitCollector) ListBranches() ([]core.GitRef, error) {
	format := "%(refname:short)|%(objectname)|%(upstream:short)"
	out, err := g.run("for-each-ref", "--format="+format, "refs/heads/")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var refs []core.GitRef
	now := time.Now().UTC()
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		parts := strings.Split(l, "|")
		if len(parts) < 2 {
			continue
		}
		ref := core.GitRef{
			RefName:       parts[0],
			RefType:       "branch",
			TargetSHA:     parts[1],
			LastUpdatedAt: now,
		}
		if len(parts) > 2 && parts[2] != "" {
			ref.UpstreamRef = parts[2]
			ahead, behind := g.AheadBehind(parts[0], parts[2])
			ref.AheadCount = ahead
			ref.BehindCount = behind
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// ListTags returns all Git tags.
func (g *GitCollector) ListTags() ([]core.GitRef, error) {
	format := "%(refname:short)|%(objectname)"
	out, err := g.run("for-each-ref", "--format="+format, "refs/tags/")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var tags []core.GitRef
	now := time.Now().UTC()
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		parts := strings.Split(l, "|")
		if len(parts) < 2 {
			continue
		}
		tags = append(tags, core.GitRef{
			RefName:       parts[0],
			RefType:       "tag",
			TargetSHA:     parts[1],
			LastUpdatedAt: now,
		})
	}
	return tags, nil
}

// AheadBehind returns ahead and behind counts between a local ref and its upstream.
func (g *GitCollector) AheadBehind(localRef, upstreamRef string) (ahead int, behind int) {
	out, err := g.run("rev-list", "--left-right", "--count", localRef+"..."+upstreamRef)
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(out)
	if len(parts) == 2 {
		ahead, _ = strconv.Atoi(parts[0])
		behind, _ = strconv.Atoi(parts[1])
	}
	return ahead, behind
}

// CommitsBetween collects detailed commits between two refs (e.g. base..head) or recent commits.
func (g *GitCollector) CommitsBetween(baseRef, headRef string, limit int) ([]core.Commit, error) {
	var args []string
	if baseRef != "" && headRef != "" {
		args = []string{"log", fmt.Sprintf("%s..%s", baseRef, headRef)}
	} else if headRef != "" {
		args = []string{"log", headRef}
	} else {
		args = []string{"log"}
	}

	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}

	// Format: %H (sha)|%h (short)|%P (parents)|%an|%ae|%cn|%ce|%aI (ISO 8601)|%s (subject)|%B (body)
	// We use custom record and field separators
	fieldSep := "§§§"
	recordSep := "¶¶¶"
	format := fmt.Sprintf("%s%%H%s%%h%s%%P%s%%an%s%%ae%s%%cn%s%%ce%s%%aI%s%%s%s%%b",
		recordSep, fieldSep, fieldSep, fieldSep, fieldSep, fieldSep, fieldSep, fieldSep, fieldSep, fieldSep)

	args = append(args, "--format="+format, "--numstat")

	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	return parseGitLogOutput(out, recordSep, fieldSep), nil
}

// logFormat is the structured git log format shared by the log readers below.
// The separators are deliberately unlikely to occur in a commit message.
const (
	logFieldSep  = "\u00a7\u00a7\u00a7"
	logRecordSep = "\u00b6\u00b6\u00b6"
)

func structuredLogFormat() string {
	return fmt.Sprintf("%s%%H%s%%h%s%%P%s%%an%s%%ae%s%%cn%s%%ce%s%%aI%s%%s%s%%b",
		logRecordSep, logFieldSep, logFieldSep, logFieldSep, logFieldSep,
		logFieldSep, logFieldSep, logFieldSep, logFieldSep, logFieldSep)
}

// LogSince reads commits on ref (empty = current checkout, "--all" = every ref)
// that are newer than since. A zero `since` means no lower bound.
func (g *GitCollector) LogSince(ref string, since time.Time, limit int) ([]core.Commit, error) {
	args := []string{"log"}
	switch ref {
	case "":
		// current checkout
	case "--all":
		args = append(args, "--all")
	default:
		args = append(args, ref)
	}
	if !since.IsZero() {
		args = append(args, "--since="+since.Format(time.RFC3339))
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	// --numstat reports nothing for a merge by default; -m would explode merges
	// into one record per parent, so merges intentionally carry no line stats
	// here and the digest takes their contents from the merged range instead.
	args = append(args, "--format="+structuredLogFormat(), "--numstat")

	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return parseGitLogOutput(out, logRecordSep, logFieldSep), nil
}

// RootCommit returns the repository's first commit, used to bound a release
// that has no preceding tag.
func (g *GitCollector) RootCommit() (string, error) {
	out, err := g.run("rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) == 0 {
		return "", fmt.Errorf("no root commit")
	}
	return lines[len(lines)-1], nil
}

// PreviousTag returns the tag immediately preceding the given one, or "" when
// it is the first. Used to bound a versioned release report.
func (g *GitCollector) PreviousTag(tag string) (string, error) {
	out, err := g.run("describe", "--tags", "--abbrev=0", tag+"^")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitsBroughtInByMerge lists the commits a merge introduced, i.e. those
// reachable from the merge's second parent but not its first.
func (g *GitCollector) CommitsBroughtInByMerge(mergeSHA string, limit int) ([]core.Commit, error) {
	parents, err := g.run("rev-list", "--parents", "-n1", mergeSHA)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(parents)
	if len(fields) < 3 {
		return nil, fmt.Errorf("%s is not a merge commit", mergeSHA)
	}
	first, second := fields[1], fields[2]

	args := []string{"log", fmt.Sprintf("%s..%s", first, second)}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	args = append(args, "--format="+structuredLogFormat(), "--numstat")

	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return parseGitLogOutput(out, logRecordSep, logFieldSep), nil
}

// parseGitLogOutput parses our structured git log output.
func parseGitLogOutput(out, recordSep, fieldSep string) []core.Commit {
	var commits []core.Commit
	records := strings.Split(out, recordSep)

	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}

		parts := strings.Split(rec, fieldSep)
		if len(parts) < 10 {
			continue
		}

		sha := parts[0]
		shortSHA := parts[1]
		parentSHAs := strings.Fields(parts[2])
		authorName := parts[3]
		authorEmail := parts[4]
		committerName := parts[5]
		committerEmail := parts[6]
		tsStr := parts[7]
		subject := parts[8]
		bodyAndStats := parts[9]

		timestamp, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			timestamp = time.Now().UTC()
		}

		// Separate commit message body from --numstat output
		statLines := strings.Split(bodyAndStats, "\n")
		var bodyLines []string
		var changedFiles []string
		insertions := 0
		deletions := 0

		for _, sl := range statLines {
			fields := strings.Fields(sl)
			if len(fields) == 3 && isNumericOrDash(fields[0]) && isNumericOrDash(fields[1]) {
				ins, _ := strconv.Atoi(fields[0])
				del, _ := strconv.Atoi(fields[1])
				insertions += ins
				deletions += del
				changedFiles = append(changedFiles, fields[2])
			} else {
				bodyLines = append(bodyLines, sl)
			}
		}

		message := strings.TrimSpace(strings.Join(bodyLines, "\n"))
		if message == "" {
			message = subject
		}

		commits = append(commits, core.Commit{
			SHA:            sha,
			ShortSHA:       shortSHA,
			ParentSHAs:     parentSHAs,
			AuthorName:     authorName,
			AuthorEmail:    authorEmail,
			CommitterName:  committerName,
			CommitterEmail: committerEmail,
			Timestamp:      timestamp,
			Subject:        subject,
			Message:        message,
			FilesChanged:   len(changedFiles),
			Insertions:     insertions,
			Deletions:      deletions,
			ChangedFiles:   changedFiles,
		})
	}

	return commits
}

func isNumericOrDash(s string) bool {
	if s == "-" {
		return true
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

// GetCommitGraph retrieves commits across all branches with lane allocation for visual graph rendering.
func (g *GitCollector) GetCommitGraph(limit int) ([]core.CommitGraphNode, error) {
	if limit <= 0 {
		limit = 100
	}

	headSHA, _ := g.CurrentHeadSHA()

	// Map of commit SHA -> list of ref names (branches, tags)
	refMap := make(map[string][]string)
	if branches, err := g.ListBranches(); err == nil {
		for _, b := range branches {
			refMap[b.TargetSHA] = append(refMap[b.TargetSHA], b.RefName)
		}
	}
	if tags, err := g.ListTags(); err == nil {
		for _, t := range tags {
			refMap[t.TargetSHA] = append(refMap[t.TargetSHA], t.RefName)
		}
	}

	// Fetch raw commits across all heads
	// Format: %H|%h|%P|%an|%aI|%s
	format := "%H|%h|%P|%an|%aI|%s"
	out, err := g.run("log", "--all", "--topo-order", fmt.Sprintf("-n%d", limit), "--format="+format)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	lines := strings.Split(out, "\n")
	var nodes []core.CommitGraphNode

	// Lane tracking
	lanes := make([]string, 0) // active SHA in each lane
	getFreeLane := func(targetSHA string) int {
		for i, sha := range lanes {
			if sha == targetSHA {
				return i
			}
		}
		for i, sha := range lanes {
			if sha == "" {
				lanes[i] = targetSHA
				return i
			}
		}
		lanes = append(lanes, targetSHA)
		return len(lanes) - 1
	}

	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		parts := strings.Split(l, "|")
		if len(parts) < 6 {
			continue
		}

		sha := parts[0]
		shortSHA := parts[1]
		parents := strings.Fields(parts[2])
		author := parts[3]
		tsStr := parts[4]
		subject := parts[5]

		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			ts = time.Now().UTC()
		}

		lane := getFreeLane(sha)

		// Update lane state for parents
		if len(parents) > 0 {
			lanes[lane] = parents[0]
			for _, p := range parents[1:] {
				getFreeLane(p)
			}
		} else {
			lanes[lane] = ""
		}

		nodes = append(nodes, core.CommitGraphNode{
			SHA:        sha,
			ShortSHA:   shortSHA,
			ParentSHAs: parents,
			AuthorName: author,
			Timestamp:  ts,
			Subject:    subject,
			Lane:       lane,
			Refs:       refMap[sha],
			IsHead:     sha == headSHA,
		})
	}

	return nodes, nil
}

// GetCommitDetail returns deep inspection data for a commit including file list, stats, and diff.
func (g *GitCollector) GetCommitDetail(sha string) (*core.CommitDetail, error) {
	if sha == "" {
		sha = "HEAD"
	}

	// 1. Get raw commit metadata.
	// A "|" separator would be corrupted by any commit message containing one,
	// so use the same unlikely field separator CommitsBetween relies on.
	const fieldSep = "§§§"
	format := strings.Join([]string{"%H", "%h", "%P", "%an", "%ae", "%aI", "%s", "%B"}, fieldSep)
	out, err := g.run("log", "-1", "--format="+format, sha)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(out, fieldSep)
	if len(parts) < 7 {
		return nil, fmt.Errorf("unexpected git log format for %s", sha)
	}

	fullSHA := parts[0]
	shortSHA := parts[1]
	parents := strings.Fields(parts[2])
	author := parts[3]
	email := parts[4]
	ts, _ := time.Parse(time.RFC3339, parts[5])
	subject := parts[6]
	message := ""
	if len(parts) > 7 {
		message = strings.TrimRight(parts[7], "\n")
	}

	// 2. Get changed files
	nameStatusOut, _ := g.run("show", "--name-status", "--oneline", "-1", fullSHA)
	var changedFiles []string
	if nameStatusOut != "" {
		lines := strings.Split(nameStatusOut, "\n")
		if len(lines) > 1 {
			for _, l := range lines[1:] {
				l = strings.TrimSpace(l)
				if l != "" {
					changedFiles = append(changedFiles, l)
				}
			}
		}
	}

	// 3. Get diff and stat
	diffOut, _ := g.run("show", "--patch", "--stat", "-1", fullSHA)

	return &core.CommitDetail{
		Commit: core.Commit{
			SHA:          fullSHA,
			ShortSHA:     shortSHA,
			ParentSHAs:   parents,
			AuthorName:   author,
			AuthorEmail:  email,
			Timestamp:    ts,
			Subject:      subject,
			Message:      message,
			FilesChanged: len(changedFiles),
			ChangedFiles: changedFiles,
		},
		Diff: diffOut,
	}, nil
}
