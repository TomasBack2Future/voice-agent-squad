package dispatch

import (
	"context"
	"slices"
	"testing"
	"time"
)

func sweepRow(t *testing.T, report *SweepReport, key string) SweepRow {
	t.Helper()
	for _, row := range report.Rows {
		if row.Reservation == key {
			return row
		}
	}
	t.Fatalf("no row %s in %+v", key, report)
	return SweepRow{}
}

// One read over the fleet must find what the per-reservation read cannot:
// which assignments are stuck, why, and whose move it is.
func TestSweepFlagsStuckAssignmentsAcrossTheFleet(t *testing.T) {
	now := time.Unix(100000, 0)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	for _, k := range []string{"QUIET", "BUSY", "ASKED", "PROSE", "WAITS", "DONE"} {
		if _, err := s.Reserve(ctx, k, "github:o/r#"+k, "dispatcher", "", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE dispatch_reservations SET canonical_item_id='T-'||item_id,state='dispatched',worker_thread_id='native-'||item_id,reserved_at=1000`)
	exec(`UPDATE dispatch_reservations SET state='completed' WHERE item_id='DONE'`)
	for _, k := range []string{"QUIET", "BUSY", "ASKED", "PROSE", "WAITS"} {
		exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long) VALUES('repo-test',?,?,1000,1000,'',0)`, "T-"+k, "worker-"+k)
	}
	msg := func(id int64, ts int64, agent, thread, kind string) {
		exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,'repo-test',?,?,?,?,'m','[]','normal')`, id, ts, agent, thread, kind)
	}
	decide := func(key string, outcome int64, action, waits string, marker int64) {
		exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,waits_for,waits_revision,on_satisfied_revision) VALUES('repo-test',?,1,?,1,?,?,'c',?,?,1,?)`,
			key, "T-"+key, outcome, action, "worker-"+key, waits, marker)
		exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,delivered_session,delivered_at) VALUES('repo-test',?,?,?,1,?,?,'decision-resolved',?,?,'x',?)`,
			"worker-"+key, "e-"+key, key, "native-"+key, "T-"+key, outcome, outcome, 2000)
	}
	// QUIET proceeds but recorded nothing for four hours.
	msg(1, 2000, "dispatcher", "T-QUIET", "say")
	decide("QUIET", 1, "proceed", "[]", 0)
	msg(2, 100000-4*3600, "worker-QUIET", "T-QUIET", "say")
	// BUSY reported ten minutes ago.
	msg(3, 2000, "dispatcher", "T-BUSY", "say")
	decide("BUSY", 3, "proceed", "[]", 0)
	msg(4, 100000-600, "worker-BUSY", "T-BUSY", "say")
	// ASKED sent a decision request half an hour ago that nobody handled.
	msg(5, 2000, "dispatcher", "T-ASKED", "say")
	decide("ASKED", 5, "proceed", "[]", 0)
	msg(6, 100000-1800, "worker-ASKED", "T-ASKED", "ask")
	exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','dispatcher','req-ASKED','ASKED',1,'native-ASKED','T-ASKED','decision-request',6,6)`)
	// PROSE is held by a sentence; WAITS by a checkable, pre-authorized wait.
	msg(7, 100000-3*3600, "dispatcher", "T-PROSE", "say")
	decide("PROSE", 7, "hold", "[]", 0)
	msg(8, 100000-3*3600, "dispatcher", "T-WAITS", "say")
	decide("WAITS", 8, "hold", `[{"reservation":"BUSY","phase":"merge","until":"executed"}]`, 1)

	report, err := s.Sweep(ctx, SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 5 || report.Flagged != 3 {
		t.Fatalf("active %d flagged %d: %+v", report.Active, report.Flagged, report.Rows)
	}
	if row := sweepRow(t, report, "QUIET"); !slices.Contains(row.Drift, "owner-quiet") || row.QuietSeconds != 4*3600 {
		t.Fatalf("quiet owner hidden: %+v", row)
	}
	// BUSY's plain progress message is a note, not something to answer.
	if row := sweepRow(t, report, "BUSY"); len(row.Drift) != 0 || !slices.Contains(row.Notes, "worker-report-without-event") {
		t.Fatalf("healthy owner flagged: %+v", row)
	}
	if row := sweepRow(t, report, "ASKED"); !slices.Contains(row.Drift, "decision-request-unanswered") || row.OpenRequests != 1 {
		t.Fatalf("unanswered request hidden: %+v", row)
	}
	if row := sweepRow(t, report, "PROSE"); !slices.Contains(row.Drift, "hold-without-checkable-wait") {
		t.Fatalf("prose hold hidden: %+v", row)
	}
	if row := sweepRow(t, report, "WAITS"); len(row.Drift) != 0 || row.OnSatisfied != "proceed" || len(row.Waits) != 1 {
		t.Fatalf("checkable hold flagged: %+v", row)
	}
	// Flagged rows come first.
	for i, row := range report.Rows {
		if (len(row.Drift) > 0) != (i < report.Flagged) {
			t.Fatalf("flagged rows not first: %+v", report.Rows)
		}
	}
	// A decision nobody received is drift for the Dispatcher.
	exec(`UPDATE terminal_event_receipts SET delivered_at=0 WHERE event_id='e-BUSY'`)
	if report, err = s.Sweep(ctx, SweepOptions{}); err != nil || !slices.Contains(sweepRow(t, report, "BUSY").Drift, "decision-undelivered") {
		t.Fatalf("undelivered decision hidden: %+v %v", report, err)
	}
	// An automatic release must reach the Worker under its own event: the old
	// hold's delivered receipt does not count for the released revision.
	exec(`UPDATE dispatch_decisions SET revision=2,action='proceed',waits_for='[]',waits_revision=2,on_satisfied_revision=0 WHERE reservation_key='WAITS'`)
	exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','worker-WAITS','worker-terminal-v1/WAITS/1/native-WAITS/decision-resolved/8/waits-released-r2','WAITS',1,'native-WAITS','T-WAITS','decision-resolved',8,8)`)
	if report, err = s.Sweep(ctx, SweepOptions{}); err != nil || !slices.Contains(sweepRow(t, report, "WAITS").Drift, "decision-undelivered") {
		t.Fatalf("undelivered release hidden: %+v %v", report, err)
	}
	exec(`UPDATE terminal_event_receipts SET delivered_session='x',delivered_at=99000 WHERE event_id LIKE '%/waits-released-r2'`)
	if report, err = s.Sweep(ctx, SweepOptions{QuietAfter: 1000 * time.Hour}); err != nil || len(sweepRow(t, report, "WAITS").Drift) != 0 {
		t.Fatalf("delivered release flagged: %+v %v", report, err)
	}
	// An owner whose hold ended a minute ago is not quiet, however long it was held.
	exec(`UPDATE dispatch_decision_history SET decided_at=? WHERE reservation_key='WAITS'`, 100000-60)
	exec(`INSERT OR REPLACE INTO dispatch_decision_history(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,decided_by,decided_at) VALUES('repo-test','WAITS',1,'T-WAITS',2,8,'proceed','c','waits-satisfied',?)`, 100000-60)
	if report, err = s.Sweep(ctx, SweepOptions{}); err != nil || slices.Contains(sweepRow(t, report, "WAITS").Drift, "owner-quiet") || sweepRow(t, report, "WAITS").QuietSeconds != 60 {
		t.Fatalf("freshly released owner flagged quiet: %+v %v", sweepRow(t, report, "WAITS"), err)
	}
	// A decision copied by a takeover has no receipt; that is not an undelivered wake.
	exec(`DELETE FROM terminal_event_receipts WHERE reservation_key='PROSE'`)
	if report, err = s.Sweep(ctx, SweepOptions{}); err != nil || slices.Contains(sweepRow(t, report, "PROSE").Drift, "decision-undelivered") {
		t.Fatalf("receiptless decision flagged undelivered: %+v %v", report, err)
	}
	// A reservation not yet bound to a Worker is listed without Worker checks.
	if _, err = s.Reserve(ctx, "FRESH", "github:o/r#FRESH", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if report, err = s.Sweep(ctx, SweepOptions{}); err != nil || sweepRow(t, report, "FRESH").State != "reserved" || len(sweepRow(t, report, "FRESH").Drift) != 0 {
		t.Fatalf("reserved row: %+v %v", report, err)
	}
	// A tracked operation in flight explains a quiet ledger.
	exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',?,'worker-QUIET','T-QUIET','owned-operation',?,'[]','normal')`, 100000-4*3600,
		`{"request":{"reservation":"QUIET","generation":1,"worker_session":"native-QUIET","request_key":"deploy-watch"},"state":"started"}`)
	if report, err = s.Sweep(ctx, SweepOptions{}); err != nil || slices.Contains(sweepRow(t, report, "QUIET").Drift, "owner-quiet") {
		t.Fatalf("owner with an operation in flight flagged quiet: %+v %v", report, err)
	}
	// A Dispatcher sweeps its own reservations, not leftovers of earlier controllers.
	exec(`UPDATE dispatch_reservations SET reserved_by='retired-dispatcher' WHERE item_id IN ('PROSE','ASKED')`)
	if report, err = s.Sweep(ctx, SweepOptions{ReservedBy: "dispatcher"}); err != nil || report.Active != 4 {
		t.Fatalf("owner filter: %+v %v", report, err)
	}
	for _, row := range report.Rows {
		if row.Reservation == "PROSE" || row.Reservation == "ASKED" {
			t.Fatalf("another controller's reservation swept: %+v", row)
		}
	}
	exec(`UPDATE dispatch_reservations SET reserved_by='dispatcher'`)
	// Thresholds are the caller's.
	if report, err = s.Sweep(ctx, SweepOptions{QuietAfter: time.Minute}); err != nil || !slices.Contains(sweepRow(t, report, "ASKED").Drift, "owner-quiet") {
		t.Fatalf("custom threshold ignored: %+v %v", report, err)
	}
}
