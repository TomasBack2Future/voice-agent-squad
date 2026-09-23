package chat

import (
	"context"
	"database/sql"

	"github.com/zsiec/squad/internal/store"
)

func (c *Chat) Tick(ctx context.Context, agentID string) (Digest, error) {
	now := c.nowUnix()
	if err := store.WithTxRetry(ctx, c.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET last_tick_at=?, status='active' WHERE id=? AND repo_id=?`, now, agentID, c.repoID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE claims SET last_touch=? WHERE repo_id=? AND agent_id=? AND state='held'`, now, c.repoID, agentID)
		return err
	}); err != nil {
		return Digest{}, err
	}

	dg, err := c.Digest(ctx, agentID)
	if err != nil {
		return dg, err
	}

	updateRead := func(thread string, msgs []DigestMessage) error {
		if len(msgs) == 0 {
			return nil
		}
		var maxID int64
		for _, m := range msgs {
			if m.ID > maxID {
				maxID = m.ID
			}
		}
		_, err := c.db.ExecContext(ctx, `
			INSERT INTO reads (agent_id, thread, last_msg_id) VALUES (?, ?, ?)
			ON CONFLICT(agent_id, thread) DO UPDATE SET last_msg_id = excluded.last_msg_id
		`, agentID, thread, maxID)
		return err
	}

	globalAll := append([]DigestMessage(nil), dg.Mentions...)
	globalAll = append(globalAll, dg.Handoffs...)
	globalAll = append(globalAll, dg.Global...)
	if err := updateRead(ThreadGlobal, globalAll); err != nil {
		return dg, err
	}

	threadMsgs := map[string][]DigestMessage{}
	for _, m := range dg.YourThreads {
		threadMsgs[m.Thread] = append(threadMsgs[m.Thread], m)
	}
	for thread, msgs := range threadMsgs {
		if err := updateRead(thread, msgs); err != nil {
			return dg, err
		}
	}
	return dg, nil
}
