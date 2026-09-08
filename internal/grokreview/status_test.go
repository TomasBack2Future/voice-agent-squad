package grokreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewStatusWriterAtomicallyTracksSafeLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "grok-reviews")
	writer, err := NewReviewStatusWriter(dir, "shadow", 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1_788_800_000, 0)
	writer.started = started
	writer.deadline = started.Add(10 * time.Minute)
	writer.now = func() time.Time { return started.Add(12 * time.Second) }

	pending := ReviewObservation{
		State:    ReviewStateFreezing,
		Snapshot: PullRequestSnapshot{Repository: "TomasBack2Future/voice-agent-studio", Number: 717},
	}
	if err := writer.Observe(pending); err != nil {
		t.Fatal(err)
	}
	oldPath := writer.path
	if !strings.Contains(filepath.Base(oldPath), "pending") {
		t.Fatalf("pending path = %q", oldPath)
	}

	result := FindingsResult{
		SchemaVersion: FindingsSchemaVersion,
		Verdict:       VerdictApproved,
		Summary:       "No blocking findings.",
		Findings:      []Finding{},
	}
	observation := ReviewObservation{
		State: ReviewStateApproved,
		Snapshot: PullRequestSnapshot{
			Repository: "TomasBack2Future/voice-agent-studio", Number: 717,
			BaseRef: "main", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40),
			Description: "must not be persisted", Diff: "private diff",
		},
		Result: result,
		Audit: CLIAudit{
			ResolvedModel: "grok-4.6", Usage: TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			CostUSD: 0.012, FailureKind: CLIFailurePromptFileFormat,
		},
		Publication: Publication{
			CommentURL: "https://github.com/owner/repo/pull/717#issuecomment-1",
			CheckURL:   "https://github.com/owner/repo/runs/2",
		},
	}
	if err := writer.Observe(observation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("pending record still exists: %v", err)
	}
	raw, err := os.ReadFile(writer.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must not be persisted", "private diff", "request_id", "session_id", "stderr"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, raw)
		}
	}
	var status ReviewStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != ReviewStateApproved || status.HeadSHA != strings.Repeat("b", 40) || status.TotalTokens != 15 {
		t.Fatalf("status = %#v", status)
	}
	if status.FailureKind != CLIFailurePromptFileFormat {
		t.Fatalf("failure kind = %q", status.FailureKind)
	}
	if status.CompletedAt != started.Add(12*time.Second).Unix() || status.DurationMS != 12_000 {
		t.Fatalf("completion = %#v", status)
	}
	info, err := os.Stat(writer.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".review-status-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}

func TestReviewStatusWriterDoesNotPersistUnvalidatedModelText(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "grok-reviews")
	writer, err := NewReviewStatusWriter(dir, "required", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observation := ReviewObservation{
		State:    ReviewStateValidating,
		Snapshot: PullRequestSnapshot{Repository: "owner/repo", Number: 3, HeadSHA: "abc1234"},
		Result:   FindingsResult{SchemaVersion: "wrong", Verdict: VerdictApproved, Summary: "untrusted hidden text"},
	}
	if err := writer.Observe(observation); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(writer.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "untrusted hidden text") {
		t.Fatalf("unvalidated result persisted: %s", raw)
	}
}

func TestReviewStatusWriterRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReviewStatusWriter(link, "shadow", time.Minute); err == nil {
		t.Fatal("expected symlink directory rejection")
	}
}
