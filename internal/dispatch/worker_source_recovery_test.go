package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func sourceRecoveryFixture(t *testing.T) (*Store, WorkerHandoffRequest) {
	t.Helper()
	s, q := workerHandoffFixture(t)
	if _, err := s.db.Exec(`DELETE FROM messages WHERE id=?; INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','old-worker','worker-terminal-v1/D/1/old-session/decision-resolved/4','D',1,'old-session','T','decision-resolved',4,4)`, q.ConsentOutcomeID); err != nil {
		t.Fatal(err)
	}
	q.ConsentOutcomeID = 0
	raw, _ := json.Marshal(q)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	authority := strings.Repeat("a", 64)
	expires := s.now().Unix() + 300
	object["source_recovery"] = map[string]any{"human_authority_sha256": authority, "expires_at": expires,
		"pending_event_ids": []string{"worker-terminal-v1/D/1/old-session/decision-resolved/4"}, "attestation_outcome_id": 0}
	raw, _ = json.Marshal(object)
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	digest, err := WorkerHandoffDigest(q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"schema_version": "squad.source-worker-recovery-attestation.v1", "request_sha256": digest, "human_authority_sha256": authority, "expires_at": expires})
	result, err := s.db.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'supervisory-attestation',?,'[]','high')`, s.repoID, s.now().Unix(), q.Controller.Actor, q.Claim.Item, string(body))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	object["source_recovery"].(map[string]any)["attestation_outcome_id"] = id
	raw, _ = json.Marshal(object)
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	return s, q
}

func TestSourceRecoveryJoinsWithoutOldModelConsentOrForgedAck(t *testing.T) {
	s, q := sourceRecoveryFixture(t)
	receipt, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
	if err != nil {
		t.Fatal("joined failed source must recover under exact human-authorized controller attestation:", err)
	}
	if receipt.Reservation.Generation != 2 || receipt.PreviousActor != "old-worker" {
		t.Fatal(receipt)
	}
	var processed int64
	if err := s.db.QueryRow(`SELECT processed_at FROM terminal_event_receipts WHERE recipient='old-worker' AND event_id='worker-terminal-v1/D/1/old-session/decision-resolved/4'`).Scan(&processed); err != nil || processed != 0 {
		t.Fatal("old delivery was forged", processed, err)
	}
	var action string
	if err := s.db.QueryRow(`SELECT action FROM dispatch_decisions WHERE generation=2`).Scan(&action); err != nil || action != "hold" {
		t.Fatal("replacement HOLD lost", action, err)
	}
	again, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
	if err != nil || again != receipt {
		t.Fatal("immutable retry", again, err)
	}
}

func TestSourceRecoveryRejectsChangedAuthorityCustodyAndPendingReceipts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*WorkerHandoffRequest)
		statement string
		foreign   bool
	}{
		{name: "missing authority", mutate: func(q *WorkerHandoffRequest) { q.SourceRecovery.HumanAuthoritySHA256 = "" }},
		{name: "changed authority", mutate: func(q *WorkerHandoffRequest) { q.SourceRecovery.HumanAuthoritySHA256 = strings.Repeat("b", 64) }},
		{name: "expired", mutate: func(q *WorkerHandoffRequest) { q.SourceRecovery.ExpiresAt = 1 }},
		{name: "unbounded expiry", mutate: func(q *WorkerHandoffRequest) { q.SourceRecovery.ExpiresAt = 999999999999 }},
		{name: "changed pending snapshot", mutate: func(q *WorkerHandoffRequest) { q.SourceRecovery.PendingEventIDs = nil }},
		{name: "duplicate pending snapshot", mutate: func(q *WorkerHandoffRequest) {
			q.SourceRecovery.PendingEventIDs = append(q.SourceRecovery.PendingEventIDs, q.SourceRecovery.PendingEventIDs[0])
		}},
		{name: "original consent ambiguity", mutate: func(q *WorkerHandoffRequest) { q.ConsentOutcomeID = 1 }},
		{name: "legacy ambiguity", mutate: func(q *WorkerHandoffRequest) { q.LegacyStopID = "stopped" }},
		{name: "foreign caller", foreign: true},
		{name: "foreign attestation", statement: `UPDATE messages SET agent_id='intruder' WHERE kind='supervisory-attestation'`},
		{name: "changed digest", statement: `UPDATE messages SET body='{}' WHERE kind='supervisory-attestation'`},
		{name: "unjoined pin", statement: `UPDATE execution_authorizations SET state='authorized'`},
		{name: "live original native", statement: fmt.Sprintf(`UPDATE execution_authorizations SET binding=json_set(binding,'$.client_pid',%d)`, os.Getpid())},
		{name: "changed primary claim", statement: `UPDATE claims SET generation=2`},
		{name: "changed controller", statement: `UPDATE dispatch_controller_bindings SET epoch=2`},
		{name: "proceed is not hold", statement: `UPDATE dispatch_decisions SET action='proceed'`},
		{name: "pending result", statement: `UPDATE terminal_event_receipts SET kind='blocked' WHERE processed_at=0`},
		{name: "wrong decision recipient", statement: `UPDATE terminal_event_receipts SET recipient='other' WHERE processed_at=0`},
		{name: "wrong decision native", statement: `UPDATE terminal_event_receipts SET worker_session='other' WHERE processed_at=0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := sourceRecoveryFixture(t)
			if tc.mutate != nil {
				tc.mutate(&q)
			}
			if tc.statement != "" {
				if _, err := s.db.Exec(tc.statement); err != nil {
					t.Fatal(err)
				}
			}
			actor := q.Controller.Actor
			if tc.foreign {
				actor = "intruder"
			}
			if _, err := s.WorkerHandoff(context.Background(), actor, q); err == nil {
				t.Fatal("unsafe source recovery admitted")
			}
			var generation, count int64
			if err := s.db.QueryRow(`SELECT generation FROM dispatch_reservations WHERE item_id='D'`).Scan(&generation); err != nil || generation != 1 {
				t.Fatal("partial custody transfer", generation, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM worker_handoffs`).Scan(&count); err != nil || count != 0 {
				t.Fatal("partial receipt", count, err)
			}
		})
	}
}
