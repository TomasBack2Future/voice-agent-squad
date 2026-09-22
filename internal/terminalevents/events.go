// Package terminalevents delivers bounded worker outcomes without terminal input.
package terminalevents

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var eventPattern = regexp.MustCompile(`worker-terminal-v1/([A-Za-z0-9_-]+)/([1-9][0-9]*)/([A-Za-z0-9_-]+)/(issue-closed|handoff-complete|blocked)/([1-9][0-9]*)`)

type Event struct {
	ID          string `json:"event_id"`
	Item        string `json:"item_id"`
	Kind        string `json:"kind"`
	OutcomeID   int64  `json:"outcome_id"`
	SourceID    int64  `json:"source_message_id"`
	DeliveredAt int64  `json:"delivered_at"`
	ProcessedAt int64  `json:"processed_at"`
}

type Store struct {
	DB              *sql.DB
	Repo, Recipient string
}

// Discover imports only outcomes for this recipient's current dispatched
// reservations. Compatibility with existing durable callback prose means a
// running Worker need not be restarted to adopt the receiver.
func (s Store) Discover(ctx context.Context) error {
	if s.Repo == "" || s.Recipient == "" {
		return fmt.Errorf("repo and recipient required")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id,m.thread,m.body,m.agent_id,r.item_id,r.generation,r.worker_thread_id
 FROM messages m JOIN dispatch_reservations r ON r.repo_id=m.repo_id AND r.canonical_item_id=m.thread
 WHERE m.repo_id=? AND r.reserved_by=? AND r.state='dispatched' AND m.body LIKE '%worker-terminal-v1/%'
 ORDER BY m.id`, s.Repo, s.Recipient)
	if err != nil {
		return err
	}
	type candidate struct {
		id, gen                        int64
		item, body, actor, key, worker string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.item, &c.body, &c.actor, &c.key, &c.gen, &c.worker); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		for _, parts := range eventPattern.FindAllStringSubmatch(c.body, -1) {
			gen, _ := strconv.ParseInt(parts[2], 10, 64)
			outcome, _ := strconv.ParseInt(parts[5], 10, 64)
			if parts[1] != c.key || gen != c.gen || parts[3] != c.worker || outcome > c.id {
				continue
			}
			// Check again in the write: discovery must not race a new generation/binding.
			_, err = s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO terminal_event_receipts
   (repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id)
   SELECT ?,?,?,?,?,?,?,?,?,? WHERE EXISTS (
    SELECT 1 FROM messages m JOIN dispatch_reservations r ON r.repo_id=m.repo_id AND r.canonical_item_id=m.thread
    WHERE m.id=? AND m.repo_id=? AND m.thread=? AND m.agent_id=? AND r.item_id=?
    AND r.reserved_by=? AND r.generation=? AND r.worker_thread_id=? AND r.state='dispatched')`,
				s.Repo, s.Recipient, parts[0], c.key, gen, c.worker, c.item, parts[4], outcome, c.id,
				outcome, s.Repo, c.item, c.actor, c.key, s.Recipient, gen, c.worker)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Pending never advances ordinary mailbox cursors. Stale generations and
// transferred reservations cannot be delivered to an old owner.
func (s Store) Pending(ctx context.Context, session string, retry time.Duration) ([]Event, error) {
	if s.Repo == "" || s.Recipient == "" || session == "" {
		return nil, fmt.Errorf("repo, recipient and delivery session required")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT e.event_id,e.item_id,e.kind,e.outcome_id,e.source_message_id,e.delivered_at,e.processed_at
 FROM terminal_event_receipts e JOIN dispatch_reservations r
 ON r.repo_id=e.repo_id AND r.item_id=e.reservation_key AND r.generation=e.generation
 AND r.worker_thread_id=e.worker_session AND r.reserved_by=e.recipient
 WHERE e.repo_id=? AND e.recipient=? AND e.processed_at=0
 AND (e.delivered_session!=? OR e.delivered_at<=?) ORDER BY e.source_message_id LIMIT 16`, s.Repo, s.Recipient, session, time.Now().Add(-retry).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.ID, &e.Item, &e.Kind, &e.OutcomeID, &e.SourceID, &e.DeliveredAt, &e.ProcessedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s Store) Delivered(ctx context.Context, id, session string) error {
	if session == "" {
		return fmt.Errorf("delivery session required")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE terminal_event_receipts SET delivered_session=?,delivered_at=?
 WHERE repo_id=? AND recipient=? AND event_id=? AND processed_at=0`, session, time.Now().Unix(), s.Repo, s.Recipient, id)
	return err
}

// Ack is an explicit recipient action after reconciliation, not an output/read
// cursor. It never closes a reservation, releases a claim or dispatches work.
func (s Store) Ack(ctx context.Context, id, note string) error {
	if s.Repo == "" || s.Recipient == "" || strings.TrimSpace(note) == "" {
		return fmt.Errorf("repo, recipient and reconciliation note required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE terminal_event_receipts SET processed_at=?,processed_note=?
 WHERE repo_id=? AND recipient=? AND event_id=? AND processed_at=0 AND delivered_at>0
 AND EXISTS(SELECT 1 FROM dispatch_reservations r WHERE r.repo_id=terminal_event_receipts.repo_id
 AND r.item_id=terminal_event_receipts.reservation_key AND r.generation=terminal_event_receipts.generation
 AND r.worker_thread_id=terminal_event_receipts.worker_session AND r.reserved_by=terminal_event_receipts.recipient)`, time.Now().Unix(), note, s.Repo, s.Recipient, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var previous string
	err = s.DB.QueryRowContext(ctx, `SELECT processed_note FROM terminal_event_receipts WHERE repo_id=? AND recipient=? AND event_id=? AND processed_at>0`, s.Repo, s.Recipient, id).Scan(&previous)
	if err == nil && previous == note {
		return nil
	}
	return fmt.Errorf("event missing, not delivered, stale, or already acknowledged with another note")
}
