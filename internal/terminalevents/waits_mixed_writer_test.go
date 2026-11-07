package terminalevents

import (
	"context"
	"testing"
)

// During a rollout an older binary (for example a session's pinned MCP
// server) can still set decisions. Its upsert does not know the wait columns,
// so a later revision written by it must not inherit an earlier hold's waits.
func TestOlderWriterRevisionDoesNotInheritStructuredWaits(t *testing.T) {
	s := waitFixture(t, "D1925", "D1927")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1925", 0, "merge", Wait{Reservation: "D1927", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	// The exact upsert of the pre-#166 binary (299df8db): a free-text hold at revision 2.
	if _, err := s.DB.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,merge_policy) VALUES(?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(repo_id,reservation_key,generation,item_id) DO UPDATE SET revision=excluded.revision,outcome_id=excluded.outcome_id,action=excluded.action,condition=excluded.condition,worker_agent=excluded.worker_agent,merge_policy=excluded.merge_policy`,
		"repo", "D1925", 1, "TASK-D1925", 2, say(t, s, "dispatcher", "TASK-D1925"), "hold", "free-text hold from an older binary", "worker-D1925", "standing"); err != nil {
		t.Fatal(err)
	}
	d, err := s.CurrentDecision(ctx, "D1925", 1, "native-D1925")
	if err != nil || d.Revision != 2 || len(d.WaitsFor) != 0 || d.BlocksPhase != "" {
		t.Fatalf("revision 2 inherited revision 1's waits: %+v %v", d, err)
	}
	// Nor do the stale waits take part in cycle checks for other assignments.
	if _, err = s.Decide(ctx, "dispatcher", hold(s, t, "D1927", 0, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatalf("stale waits caused a cycle refusal: %v", err)
	}
}
