// Package terminalevents delivers bounded worker outcomes without terminal input.
package terminalevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

var eventPattern = regexp.MustCompile(`worker-terminal-v1/([A-Za-z0-9_-]+)/([1-9][0-9]*)/([A-Za-z0-9_-]+)/(issue-closed|handoff-complete|blocked|decision-request|decision-resolved|reconcile-needed)/([1-9][0-9]*)`)

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

// PublishRequest carries pointers, never a command or authority from the sender.
type PublishRequest struct {
	Reservation   string `json:"reservation"`
	Generation    int64  `json:"generation"`
	WorkerSession string `json:"worker_session"`
	Kind          string `json:"kind"`
	OutcomeID     int64  `json:"outcome_id"`
}

var ErrInvalidEvent = errors.New("event rejected: verify reservation, generation, worker, actor and outcome")

// Publish validates and inserts atomically. Recipient and item come from the ledger.
func (s Store) Publish(ctx context.Context, actor string, q PublishRequest) (string, error) {
	return s.record(ctx, actor, q, q.OutcomeID)
}

func (s Store) record(ctx context.Context, actor string, q PublishRequest, source int64) (string, error) {
	var id string
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var e error
		id, e = s.recordTx(ctx, tx, actor, q, source)
		return e
	})
	return id, err
}

func (s Store) recordTx(ctx context.Context, tx *sql.Tx, actor string, q PublishRequest, source int64) (string, error) {
	id := fmt.Sprintf("worker-terminal-v1/%s/%d/%s/%s/%d", q.Reservation, q.Generation, q.WorkerSession, q.Kind, q.OutcomeID)
	if eventPattern.FindString(id) != id || s.Repo == "" || actor == "" || q.OutcomeID > source {
		return "", ErrInvalidEvent
	}
	// A global legacy message is valid only with task custody. Same-thread prose
	// alone is also insufficient: another actor must not forge this Worker's event.
	var item, owner, recipient string
	err := tx.QueryRowContext(ctx, `SELECT r.canonical_item_id,r.reserved_by FROM dispatch_reservations r
 JOIN messages m ON m.id=? AND m.repo_id=r.repo_id AND m.agent_id=?
 JOIN messages src ON src.id=? AND src.repo_id=r.repo_id AND src.agent_id=m.agent_id
 WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.worker_thread_id=?
 AND r.state IN ('dispatched','completed') AND m.ts>=r.reserved_at AND src.ts>=r.reserved_at
 AND m.thread IN (r.canonical_item_id,'global') AND src.thread IN (r.canonical_item_id,'global')
 AND ((?='decision-resolved' AND m.agent_id=r.reserved_by AND r.state='dispatched') OR
 (?!='decision-resolved' AND (EXISTS(SELECT 1 FROM claims c WHERE c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id AND c.agent_id=m.agent_id AND c.claimed_at>=r.reserved_at)
 OR EXISTS(SELECT 1 FROM claim_history c WHERE c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id AND c.agent_id=m.agent_id AND c.claimed_at>=r.reserved_at))))
 AND (? NOT IN ('decision-request','reconcile-needed') OR r.state='dispatched')`,
		q.OutcomeID, actor, source, s.Repo, q.Reservation, q.Generation, q.WorkerSession, q.Kind, q.Kind, q.Kind).Scan(&item, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidEvent
	}
	if err != nil {
		return "", err
	}
	recipient = owner
	if q.Kind == "decision-resolved" {
		err = tx.QueryRowContext(ctx, `SELECT agent_id FROM claims WHERE repo_id=? AND item_id=?`, s.Repo, item).Scan(&recipient)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrInvalidEvent
		}
		if err != nil {
			return "", err
		}
	}
	if s.Recipient != "" && recipient != s.Recipient {
		return "", ErrInvalidEvent
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO terminal_event_receipts
 (repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, s.Repo, recipient, id, q.Reservation, q.Generation, q.WorkerSession, item, q.Kind, q.OutcomeID, source)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Discover supports old canonical/global callbacks and observes durable done/ask
// transitions. A done observation requests reconciliation; it never proves closure.
func (s Store) Discover(ctx context.Context) error {
	if s.Repo == "" || s.Recipient == "" {
		return fmt.Errorf("repo and recipient required")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id,m.agent_id,m.thread,m.kind,COALESCE(m.body,''),COALESCE(m.mentions,'[]'),r.item_id,r.generation,r.worker_thread_id,r.canonical_item_id,r.state
 FROM messages m JOIN dispatch_reservations r ON r.repo_id=m.repo_id
 AND (m.thread=r.canonical_item_id OR m.thread='global')
 WHERE m.repo_id=? AND r.reserved_by=? AND r.state IN ('dispatched','completed')
 AND m.ts>=r.reserved_at AND (m.body LIKE '%worker-terminal-v1/%' OR (m.thread=r.canonical_item_id AND m.kind IN ('done','ask')))
 ORDER BY m.id`, s.Repo, s.Recipient)
	if err != nil {
		return err
	}
	type candidate struct {
		id, gen                                                       int64
		actor, thread, kind, body, mentions, key, worker, item, state string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.actor, &c.thread, &c.kind, &c.body, &c.mentions, &c.key, &c.gen, &c.worker, &c.item, &c.state); err != nil {
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
			if parts[1] != c.key || gen != c.gen || parts[3] != c.worker {
				continue
			}
			_, err = s.record(ctx, c.actor, PublishRequest{c.key, gen, c.worker, parts[4], outcome}, c.id)
			if err != nil && !errors.Is(err, ErrInvalidEvent) {
				return err
			}
		}
		kind := ""
		if c.thread == c.item && c.state == "dispatched" {
			if c.kind == "done" {
				kind = "reconcile-needed"
			}
			var mentions []string
			if c.kind == "ask" && json.Unmarshal([]byte(c.mentions), &mentions) == nil {
				for _, m := range mentions {
					if m == s.Recipient {
						kind = "decision-request"
					}
				}
			}
		}
		if kind != "" {
			_, err = s.record(ctx, c.actor, PublishRequest{c.key, c.gen, c.worker, kind, c.id}, c.id)
			if err != nil && !errors.Is(err, ErrInvalidEvent) {
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
 AND r.worker_thread_id=e.worker_session
 AND (r.reserved_by=e.recipient OR (e.kind='decision-resolved' AND r.state='dispatched' AND EXISTS
 (SELECT 1 FROM claims c WHERE c.repo_id=e.repo_id AND c.item_id=e.item_id AND c.agent_id=e.recipient AND c.claimed_at>=r.reserved_at)))
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
 AND r.worker_thread_id=terminal_event_receipts.worker_session
 AND (r.reserved_by=terminal_event_receipts.recipient OR (terminal_event_receipts.kind='decision-resolved' AND r.state='dispatched' AND EXISTS
 (SELECT 1 FROM claims c WHERE c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id AND c.agent_id=terminal_event_receipts.recipient AND c.claimed_at>=r.reserved_at))))`, time.Now().Unix(), note, s.Repo, s.Recipient, id)
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
