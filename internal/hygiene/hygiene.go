// Package hygiene runs integrity sweeps over the squad store and the
// on-disk items directory: stale claims, ghost agents, orphan touches,
// broken file references, items in done/ still marked in_progress, and
// SQLite-integrity failures.
package hygiene

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

const (
	// DefaultStaleClaimSec matches scaffold + docs ("60 min"). Override via
	// hygiene.stale_claim_minutes in .squad/config.yaml; New() takes seconds.
	DefaultStaleClaimSec = 60 * 60
	StaleClaimLongSec    = 2 * 60 * 60
	GhostAgentSec        = 24 * 60 * 60
)

type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarn
	SeverityError
)

type Finding struct {
	Severity Severity
	Code     string
	Message  string
	Fix      string
}

type ItemRef struct {
	ID, Path, Status string
	Created, Updated string
	References       []string
	BlockedBy        []string
	EvidenceRequired []string
}

type BrokenRef struct {
	Path  string
	Error string
}

type Items interface {
	List(ctx context.Context) ([]ItemRef, error)
	Broken(ctx context.Context) ([]BrokenRef, error)
}

type Sweeper struct {
	db       *sql.DB
	repoID   string
	items    Items
	now      func() time.Time
	staleSec int64
}

func New(db *sql.DB, repoID string, items Items) *Sweeper {
	return &Sweeper{db: db, repoID: repoID, items: items, now: time.Now, staleSec: DefaultStaleClaimSec}
}

func NewWithClock(db *sql.DB, repoID string, items Items, clock func() time.Time) *Sweeper {
	return &Sweeper{db: db, repoID: repoID, items: items, now: clock, staleSec: DefaultStaleClaimSec}
}

// WithStaleSeconds returns a copy of sw with the stale-claim threshold
// overridden. A non-positive value falls back to the default.
func (sw *Sweeper) WithStaleSeconds(sec int64) *Sweeper {
	cp := *sw
	if sec > 0 {
		cp.staleSec = sec
	}
	return &cp
}

func (sw *Sweeper) nowUnix() int64 { return sw.now().Unix() }

func (sw *Sweeper) Sweep(ctx context.Context) ([]Finding, error) {
	var findings []Finding
	now := sw.nowUnix()

	rows, err := sw.db.QueryContext(ctx, `
		SELECT item_id, agent_id, last_touch, long FROM claims
		WHERE repo_id = ? AND ((long = 0 AND last_touch < ?) OR (long = 1 AND last_touch < ?))
	`, sw.repoID, now-sw.staleSec, now-StaleClaimLongSec)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item, agent string
		var lt, long int64
		if err := rows.Scan(&item, &agent, &lt, &long); err != nil {
			rows.Close()
			return nil, err
		}
		fix := "squad force-release " + item + " --reason \"stale\""
		if isProtectedEnvironment(item) {
			fix = "verify the holder task is stopped and external operations are terminal; then use `squad recover " + item + " --from " + agent + " ...`"
		}
		findings = append(findings, Finding{
			Severity: SeverityWarn,
			Code:     "stale_claim",
			Message:  "stale claim: " + item + " (agent=" + agent + ")",
			Fix:      fix,
		})
	}
	rows.Close()

	ghostRows, err := sw.db.QueryContext(ctx,
		`SELECT id, last_tick_at FROM agents WHERE repo_id = ? AND status='active' AND last_tick_at < ?`,
		sw.repoID, now-GhostAgentSec)
	if err != nil {
		return nil, err
	}
	for ghostRows.Next() {
		var id string
		var ts int64
		if err := ghostRows.Scan(&id, &ts); err != nil {
			ghostRows.Close()
			return nil, err
		}
		findings = append(findings, Finding{
			Severity: SeverityWarn,
			Code:     "ghost_agent",
			Message:  "ghost agent: " + id + " (no tick in over 24h, still marked active)",
			Fix:      "squad force-release --agent " + id,
		})
	}
	ghostRows.Close()

	orphRows, err := sw.db.QueryContext(ctx, `
		SELECT t.agent_id, t.path, t.item_id FROM touches t
		LEFT JOIN claims c ON c.item_id = t.item_id AND c.agent_id = t.agent_id AND c.repo_id = t.repo_id
		WHERE t.repo_id = ? AND t.released_at IS NULL AND t.item_id IS NOT NULL AND c.item_id IS NULL
	`, sw.repoID)
	if err != nil {
		return nil, err
	}
	for orphRows.Next() {
		var a, p, i string
		if err := orphRows.Scan(&a, &p, &i); err != nil {
			orphRows.Close()
			return nil, err
		}
		findings = append(findings, Finding{
			Severity: SeverityInfo,
			Code:     "orphan_touch",
			Message:  "orphan touch: " + p + " by " + a + " (item " + i + " not claimed)",
			Fix:      "squad untouch " + p,
		})
	}
	orphRows.Close()

	var ic string
	if err := sw.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ic); err != nil {
		findings = append(findings, Finding{
			Severity: SeverityError, Code: "integrity_check",
			Message: "integrity_check failed: " + err.Error(),
			Fix:     "squad backup; restore from last good snapshot",
		})
	} else if ic != "ok" {
		findings = append(findings, Finding{
			Severity: SeverityError, Code: "integrity_check",
			Message: "integrity_check: " + ic,
			Fix:     "squad backup; sqlite3 ~/.squad/global.db .recover",
		})
	}

	if sw.items != nil {
		refs, err := sw.items.List(ctx)
		if err != nil {
			return nil, err
		}

		// Phantom claim: claim row points at an item id no item file backs.
		known := make(map[string]struct{}, len(refs))
		for _, r := range refs {
			known[r.ID] = struct{}{}
		}
		phantomRows, err := sw.db.QueryContext(ctx,
			`SELECT item_id, agent_id FROM claims WHERE repo_id = ?`, sw.repoID)
		if err != nil {
			return nil, err
		}
		for phantomRows.Next() {
			var item, agent string
			if err := phantomRows.Scan(&item, &agent); err != nil {
				phantomRows.Close()
				return nil, err
			}
			if _, ok := known[item]; !ok {
				findings = append(findings, Finding{
					Severity: SeverityWarn,
					Code:     "phantom_claim",
					Message:  "phantom claim: " + item + " held by " + agent + " but no item file exists",
					Fix:      "squad force-release " + item + " --reason \"item file missing\"",
				})
			}
		}
		phantomRows.Close()

		// Build the id index once; re-used by duplicate-id and blocked-by checks.
		idCount := make(map[string][]string, len(refs))
		for _, r := range refs {
			idCount[r.ID] = append(idCount[r.ID], r.Path)
		}
		for id, paths := range idCount {
			if len(paths) > 1 {
				findings = append(findings, Finding{
					Severity: SeverityWarn,
					Code:     "duplicate_id",
					Message:  "duplicate item id " + id + " in " + strings.Join(paths, ", "),
					Fix:      "rename one of the conflicting files (or fix its frontmatter `id:`); ids must be unique repo-wide",
				})
			}
		}

		for _, r := range refs {
			if r.Status == "in_progress" && strings.Contains(r.Path, "/done/") {
				findings = append(findings, Finding{
					Severity: SeverityWarn, Code: "done_in_progress",
					Message: "item " + r.ID + " is in done/ but frontmatter status is in_progress",
					Fix:     "edit " + r.Path + " — set status: done",
				})
			}
			if r.Created != "" && !isValidDateField(r.Created) {
				findings = append(findings, Finding{
					Severity: SeverityInfo, Code: "malformed_date",
					Message: "item " + r.ID + " has malformed `created: " + r.Created + "` (expected YYYY-MM-DD)",
					Fix:     "edit " + r.Path + " — set created to ISO 8601 date",
				})
			}
			if r.Updated != "" && !isValidDateField(r.Updated) {
				findings = append(findings, Finding{
					Severity: SeverityInfo, Code: "malformed_date",
					Message: "item " + r.ID + " has malformed `updated: " + r.Updated + "` (expected YYYY-MM-DD)",
					Fix:     "edit " + r.Path + " — set updated to ISO 8601 date",
				})
			}
			for _, ref := range r.References {
				if _, err := os.Stat(stripLineSuffix(ref)); err != nil {
					findings = append(findings, Finding{
						Severity: SeverityInfo, Code: "broken_ref",
						Message: "item " + r.ID + " references missing file " + ref,
						Fix:     "edit " + r.Path + " — fix or remove the reference",
					})
				}
			}
			for _, b := range r.BlockedBy {
				b = strings.TrimSpace(b)
				if b == "" {
					continue
				}
				if b == r.ID {
					findings = append(findings, Finding{
						Severity: SeverityWarn, Code: "blocked_by_self",
						Message: "item " + r.ID + " is blocked-by itself (unbreakable)",
						Fix:     "edit " + r.Path + " — remove " + b + " from blocked-by",
					})
					continue
				}
				if _, exists := idCount[b]; !exists {
					findings = append(findings, Finding{
						Severity: SeverityWarn, Code: "blocked_by_unknown",
						Message: "item " + r.ID + " is blocked-by " + b + " — no item with that id exists",
						Fix:     "edit " + r.Path + " — fix or remove blocked-by " + b,
					})
				}
			}
			if len(r.EvidenceRequired) > 0 && (r.Status == "done" || strings.Contains(r.Path, "/done/")) {
				missing, err := evidenceMissing(ctx, sw.db, sw.repoID, r.ID, r.EvidenceRequired)
				if err != nil {
					return nil, err
				}
				if len(missing) > 0 {
					findings = append(findings, Finding{
						Severity: SeverityError,
						Code:     "evidence_missing",
						Message:  "item " + r.ID + " is done but evidence_required missing: " + strings.Join(missing, ", "),
						Fix:      "edit " + r.Path + " — either remove the unmet kinds from evidence_required, or run `squad attest --item " + r.ID + " --kind <kind> --command \"...\"` to record the missing artifact",
					})
				}
			}
		}

		// Surface files that filesystem-walk could see but Parse rejected
		// (CRLF, BOM, malformed YAML, etc.). Without this they are entirely
		// invisible to next/status/workspace status.
		broken, err := sw.items.Broken(ctx)
		if err == nil {
			for _, b := range broken {
				findings = append(findings, Finding{
					Severity: SeverityWarn, Code: "malformed_item",
					Message: "could not parse " + b.Path + ": " + b.Error,
					Fix:     "open the file and check the YAML frontmatter; CRLF and UTF-8 BOM are now tolerated, but YAML syntax errors and missing/non-string `id:` are not",
				})
			}
		}
	}

	return findings, nil
}

// MarkStaleAgents flips status to 'stale' for agents that haven't ticked
// in over GhostAgentSec. Does NOT release their claims — force-release stays
// user-initiated.
func (sw *Sweeper) MarkStaleAgents(ctx context.Context) error {
	now := sw.nowUnix()
	_, err := sw.db.ExecContext(ctx, `
		UPDATE agents SET status = 'stale'
		WHERE repo_id = ? AND status = 'active' AND last_tick_at < ?
	`, sw.repoID, now-GhostAgentSec)
	return err
}

// ReclaimStale removes claims that have exceeded the stale threshold,
// records each in claim_history with outcome="reclaimed", and releases
// any active touches the displaced agent held on the item. Returns the
// list of item IDs reclaimed.
func (sw *Sweeper) ReclaimStale(ctx context.Context) ([]string, error) {
	now := sw.nowUnix()
	var ids []string
	err := store.WithTxRetry(ctx, sw.db, func(tx *sql.Tx) error {
		ids = nil
		rows, err := tx.QueryContext(ctx, `
			SELECT item_id, agent_id, claimed_at, last_touch FROM claims
			WHERE repo_id = ?
			  AND item_id NOT LIKE 'ENV-%'
			  AND ((long = 0 AND last_touch < ?) OR (long = 1 AND last_touch < ?))
		`, sw.repoID, now-sw.staleSec, now-StaleClaimLongSec)
		if err != nil {
			return err
		}
		type stale struct {
			item, agent          string
			claimedAt, lastTouch int64
		}
		var stales []stale
		for rows.Next() {
			var s stale
			if err := rows.Scan(&s.item, &s.agent, &s.claimedAt, &s.lastTouch); err != nil {
				rows.Close()
				return err
			}
			stales = append(stales, s)
		}
		rows.Close()

		for _, s := range stales {
			res, err := tx.ExecContext(ctx,
				`DELETE FROM claims WHERE repo_id=? AND item_id=? AND agent_id=? AND last_touch=?`,
				sw.repoID, s.item, s.agent, s.lastTouch)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO claim_history (repo_id, item_id, agent_id, claimed_at, released_at, outcome)
				VALUES (?, ?, ?, ?, ?, 'reclaimed')
			`, sw.repoID, s.item, s.agent, s.claimedAt, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE touches SET released_at = ?
				WHERE repo_id = ? AND item_id = ? AND agent_id = ? AND released_at IS NULL
			`, now, sw.repoID, s.item, s.agent); err != nil {
				return err
			}
			// Surface the auto-reclaim on the chat stream so the SSE pump
			// notifies any connected dashboard. Without this row, a reclaimed
			// claim only becomes visible on the next manual refetch.
			body := "auto-reclaimed stale claim on " + s.item
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO messages (repo_id, ts, agent_id, thread, kind, body, mentions, priority)
				VALUES (?, ?, ?, 'global', 'release', ?, '[]', 'normal')
			`, sw.repoID, now, s.agent, body); err != nil {
				return err
			}
			ids = append(ids, s.item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func isProtectedEnvironment(itemID string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(itemID)), "ENV-")
}

// isValidDateField accepts the YYYY-MM-DD shape squad writes for `created`
// and `updated`. Items imported from elsewhere may use other formats; we
// surface those as info-level findings rather than rejecting at parse time.
func isValidDateField(s string) bool {
	if len(s) != 10 {
		return false
	}
	if s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func evidenceMissing(ctx context.Context, db *sql.DB, repoID, itemID string, required []string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT kind FROM attestations WHERE repo_id = ? AND item_id = ? AND exit_code = 0
	`, repoID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		have[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []string
	for _, k := range required {
		if _, ok := have[strings.TrimSpace(k)]; !ok {
			missing = append(missing, k)
		}
	}
	return missing, nil
}

func stripLineSuffix(ref string) string {
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		tail := ref[idx+1:]
		if tail == "" {
			return ref
		}
		for _, c := range tail {
			if c < '0' || c > '9' {
				return ref
			}
		}
		return ref[:idx]
	}
	return ref
}
