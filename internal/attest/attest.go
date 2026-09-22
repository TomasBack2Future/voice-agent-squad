// Package attest owns the squad evidence ledger: durable rows in the
// attestations table plus the captured stdout/stderr they reference on disk.
// Each row binds (item_id, kind, command) to a sha256 of the artifact, so
// any later edit to the file is detectable by re-hashing.
package attest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/zsiec/squad/internal/chat"
)

type Kind string

const (
	KindTest      Kind = "test"
	KindLint      Kind = "lint"
	KindTypecheck Kind = "typecheck"
	KindBuild     Kind = "build"
	KindReview    Kind = "review"
	KindManual    Kind = "manual"
)

func (k Kind) Valid() bool {
	switch k {
	case KindTest, KindLint, KindTypecheck, KindBuild, KindReview, KindManual:
		return true
	}
	return false
}

type Record struct {
	Revocation *Revocation `json:"revocation,omitempty"`
	ID         int64
	ItemID     string
	Kind       Kind
	Command    string
	ExitCode   int
	OutputHash string
	OutputPath string
	CreatedAt  int64
	AgentID    string
	RepoID     string
}

type Ledger struct {
	db     *sql.DB
	repoID string
	now    func() time.Time
	bus    *chat.Bus
}

func New(db *sql.DB, repoID string, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{db: db, repoID: repoID, now: now}
}

// WithBus enables event publishing on Insert. Each successful fresh
// insert publishes a chat.Event of kind "attestation_recorded" so SSE
// subscribers (e.g. the TUI's evidence pane) can invalidate. CLI
// invocations don't share a bus and skip the publish silently.
func (l *Ledger) WithBus(b *chat.Bus) *Ledger {
	l.bus = b
	return l
}

func (l *Ledger) nowUnix() int64 { return l.now().Unix() }

func (l *Ledger) Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (l *Ledger) Insert(ctx context.Context, r Record) (int64, error) {
	if !r.Kind.Valid() {
		return 0, fmt.Errorf("invalid kind %q (want test|lint|typecheck|build|review|manual)", r.Kind)
	}
	created := r.CreatedAt
	if created == 0 {
		created = l.nowUnix()
	}
	res, err := l.db.ExecContext(ctx, `
		INSERT INTO attestations (item_id, kind, command, exit_code, output_hash, output_path, created_at, agent_id, repo_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		r.ItemID, string(r.Kind), r.Command, r.ExitCode,
		r.OutputHash, r.OutputPath, created, r.AgentID, l.repoID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			var existing int64
			var revoked bool
			if qerr := l.db.QueryRowContext(ctx,
				`SELECT id, EXISTS(SELECT 1 FROM attestation_revocations v WHERE v.attestation_id=attestations.id) FROM attestations WHERE repo_id = ? AND item_id = ? AND kind = ? AND output_hash = ?`,
				l.repoID, r.ItemID, string(r.Kind), r.OutputHash).Scan(&existing, &revoked); qerr == nil {
				if revoked {
					return 0, fmt.Errorf("output duplicates revoked attestation %d; original remains invalid", existing)
				}
				return existing, nil
			}
		}
		return 0, fmt.Errorf("insert attestation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if l.bus != nil {
		l.bus.Publish(chat.Event{
			Kind: "attestation_recorded",
			Payload: map[string]any{
				"item_id":    r.ItemID,
				"kind":       string(r.Kind),
				"id":         id,
				"created_at": created,
			},
		})
	}
	return id, nil
}

func isUniqueViolation(err error) bool {
	var sErr *sqlite.Error
	if errors.As(err, &sErr) {
		return sErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY ||
			sErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

var (
	ErrHashMismatch  = errors.New("attestation output hash mismatch")
	ErrOutputMissing = errors.New("attestation output file missing")
)

func (l *Ledger) Verify(ctx context.Context, itemID string) error {
	recs, err := l.ListForItem(ctx, itemID)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Revocation != nil {
			continue
		}
		data, err := os.ReadFile(r.OutputPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%w: %s (kind=%s, hash=%s)", ErrOutputMissing, r.OutputPath, r.Kind, r.OutputHash)
			}
			return fmt.Errorf("read attestation %d: %w", r.ID, err)
		}
		got := l.Hash(data)
		if got != r.OutputHash {
			return fmt.Errorf("%w: kind=%s file=%s recorded=%s actual=%s",
				ErrHashMismatch, r.Kind, r.OutputPath, r.OutputHash, got)
		}
	}
	return nil
}

func (l *Ledger) MissingKinds(ctx context.Context, itemID string, required []Kind) ([]Kind, error) {
	if len(required) == 0 {
		return nil, nil
	}
	rows, err := l.db.QueryContext(ctx, `
		SELECT kind FROM attestations
		WHERE repo_id = ? AND item_id = ? AND exit_code = 0
 AND NOT EXISTS (SELECT 1 FROM attestation_revocations v WHERE v.attestation_id=attestations.id)
	`, l.repoID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[Kind]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		have[Kind(k)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []Kind
	for _, k := range required {
		if _, ok := have[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing, nil
}

// DistinctReviewers returns the set of unique reviewer-agent ids that
// attested kind=review with exit_code=0 for itemID. Reviewer identity
// lives in the Command field as "review by <agent>" (the format the
// `squad attest --kind review --reviewer-agent <id>` flow writes).
// Rows whose Command does not match that prefix are skipped, so a
// stray manual insert won't inflate the count.
func (l *Ledger) DistinctReviewers(ctx context.Context, itemID string) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT command FROM attestations
		WHERE repo_id = ? AND item_id = ? AND kind = ? AND exit_code = 0
 AND NOT EXISTS (SELECT 1 FROM attestation_revocations v WHERE v.attestation_id=attestations.id)
	`, l.repoID, itemID, string(KindReview))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	const prefix = "review by "
	seen := map[string]struct{}{}
	var out []string
	for rows.Next() {
		var cmd string
		if err := rows.Scan(&cmd); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(cmd, prefix) {
			continue
		}
		who := strings.TrimSpace(cmd[len(prefix):])
		if who == "" {
			continue
		}
		if _, dup := seen[who]; dup {
			continue
		}
		seen[who] = struct{}{}
		out = append(out, who)
	}
	return out, rows.Err()
}

// ListForItem returns every attestation for itemID. A Ledger with
// repoID == "" widens the query across all repos (workspace mode).
func (l *Ledger) ListForItem(ctx context.Context, itemID string) ([]Record, error) {
	q := `SELECT a.id, a.item_id, a.kind, a.command, a.exit_code, a.output_hash, a.output_path, a.created_at, a.agent_id, a.repo_id,
 COALESCE(v.reason,''), COALESCE(v.agent_id,''), COALESCE(v.created_at,0), COALESCE(v.replacement_id,0)
 FROM attestations a LEFT JOIN attestation_revocations v ON v.attestation_id=a.id WHERE a.item_id = ?`
	args := []any{itemID}
	if l.repoID != "" {
		q += ` AND a.repo_id = ?`
		args = append(args, l.repoID)
	}
	q += ` ORDER BY a.created_at ASC, a.id ASC`
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var v Revocation
		var k string
		if err := rows.Scan(&r.ID, &r.ItemID, &k, &r.Command, &r.ExitCode, &r.OutputHash, &r.OutputPath, &r.CreatedAt, &r.AgentID, &r.RepoID, &v.Reason, &v.AgentID, &v.CreatedAt, &v.ReplacementID); err != nil {
			return nil, err
		}
		r.Kind = Kind(k)
		if v.Reason != "" {
			r.Revocation = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
