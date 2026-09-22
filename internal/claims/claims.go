// Package claims is the atomic claim ledger: who holds which item, since
// when, and the takeover audit trail. Claims are acquired with SQLite's
// BEGIN IMMEDIATE so two agents racing for the same item see exactly one
// winner; the loser gets a clean "already claimed by X" error rather than
// torn state. Released and force-released claims move to claim_history.
package claims

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/zsiec/squad/internal/store"
)

// maxTouchPathLen mirrors touch.MaxPathLen so claim --touches and squad
// touch enforce identical limits at every entry point.
const maxTouchPathLen = 4096

type Store struct {
	db     *sql.DB
	now    func() time.Time
	repoID string
}

func New(db *sql.DB, repoID string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, repoID: repoID, now: now}
}

func (s *Store) nowUnix() int64 { return s.now().Unix() }

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return store.WithTxRetry(ctx, s.db, fn)
}

type ClaimOption func(*claimOpts)

type claimOpts struct {
	resourceScope     string
	preflightItemsDir string
	preflightDoneDir  string
	worktreePath      string
}

func ClaimWithPreflight(itemsDir, doneDir string) ClaimOption {
	return func(o *claimOpts) {
		o.preflightItemsDir = itemsDir
		o.preflightDoneDir = doneDir
	}
}

// ClaimWithWorktree records the absolute path of the per-claim worktree
// in the claim row so done/handoff can tear it down without re-deriving.
// Empty path is a no-op (preserves the default-empty column for the
// majority of users who don't opt in).
func ClaimWithWorktree(path string) ClaimOption {
	return func(o *claimOpts) {
		o.worktreePath = path
	}
}

func (s *Store) Claim(ctx context.Context, itemID, agentID, intent string, touches []string, long bool, opts ...ClaimOption) error {
	var co claimOpts
	for _, opt := range opts {
		opt(&co)
	}
	if co.preflightItemsDir != "" {
		if err := preflightBlockers(co.preflightItemsDir, co.preflightDoneDir, itemID); err != nil {
			return err
		}
	}
	var conflictPaths []string
	if co.preflightItemsDir != "" {
		if paths, err := conflictsWithPaths(co.preflightItemsDir, itemID); err == nil {
			conflictPaths = paths
		}
	}
	now := s.nowUnix()
	longVal := 0
	if long {
		longVal = 1
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		group, scope, err := resolveResource(ctx, tx, s.repoID, itemID, co.resourceScope)
		if err != nil {
			return err
		}
		blockers, err := resourceBlockers(ctx, tx, s.repoID, itemID, group, scope)
		if err != nil {
			return err
		}
		for _, b := range blockers {
			if b.AgentID == agentID {
				return ErrAlreadyHeld
			}
		}
		if len(blockers) > 0 {
			if group == "" {
				return ErrClaimTaken
			}
			return &ResourceConflictError{Blockers: blockers}
		}

		if len(conflictPaths) > 0 {
			placeholders := make([]string, 0, len(conflictPaths))
			args := []any{s.repoID, itemID}
			for _, p := range conflictPaths {
				placeholders = append(placeholders, "?")
				args = append(args, p)
			}
			q := `
SELECT c.item_id
FROM claims c
JOIN items i ON i.repo_id = c.repo_id AND i.item_id = c.item_id
WHERE c.repo_id = ? AND c.item_id != ?
  AND EXISTS (
    SELECT 1 FROM json_each(i.conflicts_with) je
    WHERE je.value IN (` + strings.Join(placeholders, ",") + `)
  )
LIMIT 1`
			var other string
			err := tx.QueryRowContext(ctx, q, args...).Scan(&other)
			if err == nil {
				return fmt.Errorf("%w: %s overlaps with active claim on %s",
					ErrConflictsWithActive, itemID, other)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("conflicts_with check: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long, worktree, resource_group, resource_scope)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, s.repoID, itemID, agentID, now, now, intent, longVal, co.worktreePath, group, scope)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrClaimTaken
			}
			return fmt.Errorf("insert claim: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM claim_waits WHERE repo_id=? AND agent_id=? AND item_id=?`, s.repoID, agentID, itemID); err != nil {
			return err
		}
		cycle, err := deadlockCycle(ctx, tx, s.repoID, s.nowUnix())
		if err != nil {
			return err
		}
		if len(cycle) > 0 {
			return &DeadlockError{Cycle: cycle}
		}
		for _, p := range touches {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if len(p) > maxTouchPathLen {
				return fmt.Errorf("touch path too long (%d bytes, max %d, prefix=%s…)", len(p), maxTouchPathLen, p[:64])
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO touches (repo_id, agent_id, item_id, path, started_at)
				VALUES (?, ?, ?, ?, ?)
			`, s.repoID, agentID, itemID, p, now); err != nil {
				return fmt.Errorf("insert touch: %w", err)
			}
		}
		for _, p := range conflictPaths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if len(p) > maxTouchPathLen {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO touches (repo_id, agent_id, item_id, path, started_at)
				VALUES (?, ?, ?, ?, ?)
			`, s.repoID, agentID, itemID, p, now); err != nil {
				return fmt.Errorf("insert conflicts_with touch: %w", err)
			}
		}
		body := claimBody(itemID, intent)
		if err := postSystemMessage(ctx, tx, s.repoID, now, agentID, "global", "claim", body, nil, "normal"); err != nil {
			return err
		}
		return postSystemMessage(ctx, tx, s.repoID, now, agentID, itemID, "claim", body, nil, "normal")
	})
}

func claimBody(itemID, intent string) string {
	body := "claimed " + itemID
	if intent != "" {
		body += ": " + intent
	}
	return body
}

func isUniqueViolation(err error) bool {
	var sErr *sqlite.Error
	if errors.As(err, &sErr) {
		return sErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY ||
			sErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}
