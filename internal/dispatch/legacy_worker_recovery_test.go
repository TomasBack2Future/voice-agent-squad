package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyPreparedStopCanCompleteAfterControllerOutage(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	ctx := context.Background()
	prepared, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input)
	if err != nil {
		t.Fatal(err)
	}
	current := s.now().Add(48 * time.Hour)
	s.now = func() time.Time { return current }
	recovery := legacyRecoveryFixture(q, "recover-1", "", "", current)
	observed, err := s.RecoverLegacyWorkerStop(ctx, q.Controller.Actor, recovery)
	if err != nil {
		t.Fatalf("fenced custody cannot recover after a controller outage: %v", err)
	}
	q = observed.Recovery.Handoff
	q.ConsentOutcomeID, q.LegacyStopSHA256 = prepared.ConsentOutcomeID, observed.ObservationSHA256
	digest, err := WorkerHandoffDigest(q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"schema_version": "squad.legacy-worker-stop-attestation.v1", "request_sha256": digest, "stop_sha256": q.LegacyStopSHA256, "human_authority_sha256": q.HumanAuthoritySHA256, "expires_at": current.Unix() + 3600})
	message, err := s.db.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'supervisory-attestation',?,'[]','high')`, s.repoID, current.Unix(), q.Controller.Actor, q.Expected.CanonicalItemID, string(body))
	if err != nil {
		t.Fatal(err)
	}
	q.LegacyAttestationID, err = message.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkerHandoff(ctx, q.Controller.Actor, q); err != nil {
		t.Fatalf("fresh controller attestation cannot complete retained fenced custody: %v", err)
	}
	if err = s.CheckWorkerNative(ctx, q.Claim.Actor, q.Expected.WorkerThreadID); err == nil {
		t.Fatal("recovery reopened the old writer")
	}
}

func TestLegacyPrepareRejectsWorkerSelectedReviewDatabase(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	processes, err := s.legacyOwnerProbe(context.Background(), q.Expected.WorkerThreadID, input.LeasePID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "forged-admission.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE review_attempts(id TEXT PRIMARY KEY, receipt TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": "forged-review", "owner_actor": q.Claim.Actor, "owner_native": q.Expected.WorkerThreadID, "joined": true, "completed_at": 1, "wrapper_pid": processes[0].PID, "reviewer_pid": processes[1].PID, "identity": map[string]string{"repository": "o/r", "head_sha": strings.Repeat("c", 40)}})
	if _, err = db.Exec(`INSERT INTO review_attempts(id,receipt) VALUES(?,?)`, "forged-review", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	input.Inventory = []LegacyExternalOperation{{Kind: "managed-grok-review", Identity: "forged-review", ReceiptPath: path, Repository: "o/r", HeadSHA: strings.Repeat("c", 40)}}
	if _, err = s.PrepareLegacyWorkerStop(context.Background(), q.Claim.Actor, input); err == nil {
		t.Fatal("a worker-created SQLite file qualified external reviewer custody")
	}
	for _, table := range []string{"legacy_worker_stops", "worker_native_fences"} {
		var rows int
		if err = s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&rows); err != nil || rows != 0 {
			t.Fatal("untrusted external proof partially fenced custody", table, rows, err)
		}
	}
}

func TestLegacyObservedStopRequiresFreshBoundedFinalAuthority(t *testing.T) {
	for _, deadline := range []int64{-1, 86401, 3600} {
		t.Run((time.Duration(deadline) * time.Second).String(), func(t *testing.T) {
			s, q := legacyHandoffFixture(t)
			current := s.now().Add(48 * time.Hour)
			s.now = func() time.Time { return current }
			if _, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q); err == nil {
				t.Fatal("expired historical final attestation admitted transfer")
			}
			stop, err := s.LegacyWorkerStopReceipt(context.Background(), q.LegacyStopID)
			if err != nil {
				t.Fatal(err)
			}
			original := stop.Input.Handoff
			recovered, err := s.RecoverLegacyWorkerStop(context.Background(), q.Controller.Actor, legacyRecoveryFixture(original, "renew-1", "", stop.ObservationSHA256, current))
			if err != nil {
				t.Fatal(err)
			}
			oldAttestation := q.LegacyAttestationID
			q = recovered.Recovery.Handoff
			q.ConsentOutcomeID, q.LegacyStopSHA256 = recovered.ConsentOutcomeID, recovered.ObservationSHA256
			var raw string
			if err := s.db.QueryRow(`SELECT body FROM messages WHERE id=?`, oldAttestation).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			digest, err := WorkerHandoffDigest(q)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				t.Fatal(err)
			}
			body["request_sha256"] = digest
			body["expires_at"] = current.Unix() + deadline
			encoded, _ := json.Marshal(body)
			message, err := s.db.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'supervisory-attestation',?,'[]','high')`, s.repoID, current.Unix(), q.Controller.Actor, q.Expected.CanonicalItemID, string(encoded))
			if err != nil {
				t.Fatal(err)
			}
			q.LegacyAttestationID, _ = message.LastInsertId()
			_, err = s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
			if (deadline == 3600) != (err == nil) {
				t.Fatal("fresh final authority bound violated", deadline, err)
			}
			if s.CheckWorkerNative(context.Background(), q.Claim.Actor, q.Expected.WorkerThreadID) == nil {
				t.Fatal("authority renewal reopened the original native")
			}
		})
	}
}

func TestLegacyPreparationChecksAuthorityAgainBeforeFenceCommit(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	current := s.now()
	s.now = func() time.Time { return current }
	s.legacyWorkspaceProbe = func(context.Context, string) (string, error) {
		current = current.Add(2 * time.Hour)
		return strings.Repeat("b", 64), nil
	}
	if _, err := s.PrepareLegacyWorkerStop(context.Background(), q.Claim.Actor, input); err == nil {
		t.Fatal("authority that expired during preparation committed a permanent fence")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_native_fences`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestLegacyExternalReviewUsesControllerSelectedCanonicalReceipt(t *testing.T) {
	for _, scenario := range []string{"selected", "cloned path", "changed receipt", "omitted inventory", "duplicate identity"} {
		t.Run(scenario, func(t *testing.T) {
			s, q, input := legacyStopFixture(t)
			processes, _ := s.legacyOwnerProbe(context.Background(), q.Expected.WorkerThreadID, input.LeasePID)
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "admission.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TABLE review_attempts(id TEXT PRIMARY KEY, receipt TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"id": "isolated-review", "owner_actor": q.Claim.Actor, "owner_native": q.Expected.WorkerThreadID, "joined": true, "completed_at": 1, "wrapper_pid": processes[0].PID, "reviewer_pid": processes[1].PID, "identity": map[string]string{"repository": "o/r", "head_sha": strings.Repeat("c", 40)}})
			if _, err = db.Exec(`INSERT INTO review_attempts(id,receipt) VALUES(?,?)`, "isolated-review", string(raw)); err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			// Explicit controller-selected private fixture, not live qualification.
			admission := LegacyNativeAdmission{ManagedReviews: []LegacyManagedReviewIdentity{{ID: "isolated-review", DatabasePath: path, ReceiptSHA256: legacyBytesHash(raw)}}}
			inventory := []LegacyExternalOperation{{Kind: "managed-grok-review", Identity: "isolated-review", ReceiptPath: path, Repository: "o/r", HeadSHA: strings.Repeat("c", 40)}}
			switch scenario {
			case "cloned path":
				inventory[0].ReceiptPath = filepath.Join(filepath.Dir(path), "clone.db")
			case "changed receipt":
				if _, err = db.Exec(`UPDATE review_attempts SET receipt=json_set(receipt,'$.completed_at',2)`); err != nil {
					t.Fatal(err)
				}
			case "omitted inventory":
				inventory = nil
			case "duplicate identity":
				inventory = append(inventory, inventory[0])
			}
			err = verifyLegacyExternal(context.Background(), inventory, q.Claim.Actor, q.Expected.WorkerThreadID, admission)
			if (scenario == "selected") != (err == nil) {
				t.Fatal("external review provenance qualification failed", scenario, err)
			}
		})
	}
}
