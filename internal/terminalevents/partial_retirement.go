package terminalevents

import (
	"context"
	"database/sql"
	"time"
)

// Older retire-partial implementations could strand an already-delivered HOLD.
// Reconcile only that original receipt, never a new delivery or work decision.
func (s Store) ackPartialRetiredHold(ctx context.Context, tx *sql.Tx, id, note string) (bool, error) {
	if s.NativeSession == "" {
		return false, nil
	}
	var eligible int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts e
 JOIN dispatch_reservations r ON r.repo_id=e.repo_id AND r.item_id=e.reservation_key
 AND r.generation=e.generation AND r.worker_thread_id=e.worker_session
 JOIN dispatch_decisions d ON d.repo_id=r.repo_id AND d.reservation_key=r.item_id
 AND d.generation=r.generation AND d.item_id=r.canonical_item_id
 AND d.worker_agent=e.recipient AND d.action='hold' AND d.outcome_id=e.outcome_id
 WHERE e.repo_id=? AND e.recipient=? AND e.event_id=? AND e.kind='decision-resolved'
 AND e.item_id=r.canonical_item_id AND e.delivered_at>0 AND e.processed_at=0 AND r.state='failed'
 AND r.worker_thread_id=?
 AND EXISTS(SELECT 1 FROM messages m WHERE m.repo_id=r.repo_id AND m.agent_id=r.reserved_by
 AND m.thread=r.canonical_item_id AND m.kind='partial-retirement' AND json_valid(m.body)
 AND json_extract(m.body,'$.request.expected.reservation_key')=r.item_id
 AND json_extract(m.body,'$.request.expected.generation')=r.generation
 AND json_extract(m.body,'$.request.expected.worker_thread_id')=r.worker_thread_id)
 AND EXISTS(SELECT 1 FROM claim_history c WHERE c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id
 AND c.agent_id=e.recipient AND c.claimed_at>=r.reserved_at)
 AND NOT EXISTS(SELECT 1 FROM claim_history c WHERE c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id
 AND c.agent_id!=e.recipient AND c.claimed_at>=r.reserved_at)
 AND NOT EXISTS(SELECT 1 FROM claims c WHERE c.repo_id=r.repo_id AND (c.item_id=r.canonical_item_id OR c.agent_id=e.recipient))
 AND NOT EXISTS(SELECT 1 FROM execution_authorizations a WHERE a.repo_id=r.repo_id AND a.holder=e.recipient AND a.state='active')`, s.Repo, s.Recipient, id, s.NativeSession).Scan(&eligible)
	if err != nil || eligible != 1 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE terminal_event_receipts SET processed_at=?,processed_note=? WHERE repo_id=? AND recipient=? AND event_id=? AND processed_at=0`, time.Now().Unix(), note, s.Repo, s.Recipient, id)
	return err == nil, err
}
