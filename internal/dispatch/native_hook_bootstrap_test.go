package dispatch

import (
	"context"
	"testing"
	"time"
)

func bootstrapFixture(t *testing.T) *Store {
	t.Helper()
	now := time.Unix(10000, 0)
	s := newDispatchStore(t, &now)
	if _, err := s.db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','dispatcher',1,1,0,'dispatched',1,'new-native','','T')`); err != nil {
		t.Fatal(err)
	}
	return s
}

func record(s *Store, actor, native string, tool NativeHookTool) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := s.recordNativeHookEligibility(context.Background(), tx, actor, native, tool, nativeHookObservation{}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func TestFreshBoundNativeMayOnlyReadAndClaimBeforeFirstClaim(t *testing.T) {
	claim := NativeHookTool{Name: "Bash", Command: "squad claim T --intent work"}
	for _, row := range []struct {
		name   string
		setup  string
		actor  string
		native string
		tool   NativeHookTool
		ok     bool
	}{
		{"first claim", "", "worker", "new-native", claim, true},
		{"read assignment", "", "worker", "new-native", NativeHookTool{Name: "Read"}, true},
		{"skill", "", "worker", "new-native", NativeHookTool{Name: "Skill"}, true},
		{"claim mcp", "", "worker", "new-native", NativeHookTool{Name: "mcp__squad__squad_claim"}, true},
		{"installed absolute path", "", "worker", "new-native", NativeHookTool{Name: "Bash", Command: "/Users/x/.local/bin/squad claim T --intent work"}, true},
		{"coordination wrapper", "", "worker", "new-native", NativeHookTool{Name: "Bash", Command: "squad-coordination claim T"}, true},
		{"other binary named claim", "", "worker", "new-native", NativeHookTool{Name: "Bash", Command: "curl claim T"}, false},
		{"arbitrary bash", "", "worker", "new-native", NativeHookTool{Name: "Bash", Command: "rm -rf x"}, false},
		{"chained claim", "", "worker", "new-native", NativeHookTool{Name: "Bash", Command: "squad claim T; rm -rf x"}, false},
		{"substitution", "", "worker", "new-native", NativeHookTool{Name: "Bash", Command: "squad claim $(rm x)"}, false},
		{"edit", "", "worker", "new-native", NativeHookTool{Name: "Edit"}, false},
		{"write mcp", "", "worker", "new-native", NativeHookTool{Name: "mcp__squad__squad_done"}, false},
		{"unassigned native", "", "worker", "other-native", claim, false},
		{"dispatcher as worker", "", "dispatcher", "new-native", claim, false},
		{"fenced native", `INSERT INTO worker_native_fences(repo_id,native_session,actor,reservation_key,generation,stop_id) VALUES('repo-test','new-native','worker','D',1,1)`, "worker", "new-native", claim, false},
		{"closed reservation", `UPDATE dispatch_reservations SET state='failed'`, "worker", "new-native", claim, false},
		{"claimed by another", `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','intruder',2,2,'held',1)`, "worker", "new-native", claim, false},
		{"decision for another worker", `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',1,1,'hold','paused','other')`, "worker", "new-native", claim, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := bootstrapFixture(t)
			if row.setup != "" {
				if _, err := s.db.Exec(row.setup); err != nil {
					t.Fatal(err)
				}
			}
			err := record(s, row.actor, row.native, row.tool)
			if (err == nil) != row.ok {
				t.Fatalf("ok=%v err=%v", row.ok, err)
			}
		})
	}
}

func TestBootstrapWindowClosesAfterClaimAndHoldStaysAdmissible(t *testing.T) {
	s := bootstrapFixture(t)
	if _, err := s.db.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','worker',2,2,'held',1)`); err != nil {
		t.Fatal(err)
	}
	// With the claim held the ordinary path applies: any tool is eligible and no bootstrap marker is recorded.
	if err := record(s, "worker", "new-native", NativeHookTool{Name: "Edit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',2,0,'hold','paused','worker')`); err != nil {
		t.Fatal(err)
	}
	if err := record(s, "worker", "new-native", NativeHookTool{Name: "Edit"}); err != nil {
		t.Fatal(err)
	}
	// A stale actor cannot ride the native's claim.
	if err := record(s, "impostor", "new-native", NativeHookTool{Name: "Edit"}); err == nil {
		t.Fatal("impostor admitted")
	}
}
