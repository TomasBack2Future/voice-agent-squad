package claims

import (
	"context"
	"testing"
	"time"
)

func TestWorkerHeartbeatFencesAndRenewal(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	start := s.nowUnix()
	_, err := db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,canonical_item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id) VALUES('repo-test','D-1','BUG-1','test#1','dispatcher',?, ?,?,'dispatched',1,'session-1')`, start, start, start+3600)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ item, agent string }{{"BUG-1", "agent-a"}, {"ENV-001", "agent-a"}, {"BUG-2", "agent-b"}} {
		if err := s.Claim(ctx, r.item, r.agent, "", nil, false); err != nil {
			t.Fatal(err)
		}
	}
	s.now = func() time.Time { return time.Unix(start+65*60, 0) }
	assertTouch := func(item string, want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRow(`SELECT last_touch FROM claims WHERE item_id=?`, item).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s got %d want %d", item, got, want)
		}
	}
	if err := s.WorkerHeartbeat(ctx, "agent-b", "D-1", "session-1", 1, false); err == nil {
		t.Fatal("accepted mismatched canonical claim holder")
	}
	assertTouch("BUG-2", start)
	if err := s.WorkerHeartbeat(ctx, "agent-a", "D-1", "session-1", 1, true); err != nil {
		t.Fatal(err)
	}
	assertTouch("BUG-1", start)
	for _, v := range []struct {
		session string
		gen     int64
	}{{"session-1", 2}, {"old-session", 1}} {
		if err := s.WorkerHeartbeat(ctx, "agent-a", "D-1", v.session, v.gen, false); err == nil {
			t.Fatal("accepted stale identity")
		}
		assertTouch("BUG-1", start)
	}
	if err := s.WorkerHeartbeat(ctx, "agent-a", "D-1", "session-1", 1, false); err != nil {
		t.Fatal(err)
	}
	assertTouch("BUG-1", s.nowUnix())
	assertTouch("ENV-001", s.nowUnix())
	assertTouch("BUG-2", start)
	if _, err := db.Exec(`UPDATE claims SET state='recovering',last_touch=? WHERE item_id='ENV-001'`, start); err != nil {
		t.Fatal(err)
	}
	if err := s.WorkerHeartbeat(ctx, "agent-a", "D-1", "session-1", 1, false); err != nil {
		t.Fatal(err)
	}
	assertTouch("ENV-001", start)
	if _, err := db.Exec(`UPDATE dispatch_reservations SET state='completed'`); err != nil {
		t.Fatal(err)
	}
	if err := s.WorkerHeartbeat(ctx, "agent-a", "D-1", "session-1", 1, false); err == nil {
		t.Fatal("renewed terminal dispatch")
	}
}

func TestWaitRenewsMainClaimPastHour(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	start := s.nowUnix()
	if err := s.Claim(ctx, "BUG-1", "agent-a", "", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, "ENV-001", "agent-b", "", nil, true); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(start+65*60, 0) }
	if _, err := s.Wait(ctx, "wait-1", "agent-a", "ENV-001", "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := db.QueryRow(`SELECT last_touch FROM claims WHERE item_id='BUG-1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != s.nowUnix() {
		t.Fatalf("waiting did not renew main: %d", got)
	}
}
