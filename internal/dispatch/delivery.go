package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// IssueObservation is a timestamped external observation, never closure authority.
type IssueObservation struct {
	Source     string `json:"source_ref"`
	State      string `json:"state"`
	VerifiedAt int64  `json:"verified_at"`
}

type DeliveryStatus struct {
	ExecutionDisposition  string      `json:"execution_disposition"`
	WorkerState           string      `json:"worker_state"`
	Phase                 string      `json:"phase"`
	Revision              string      `json:"revision"`
	NextAction            string      `json:"next_action"`
	OperationReference    string      `json:"operation_reference,omitempty"`
	OperationAttestation  int64       `json:"operation_attestation,omitempty"`
	OperationState        string      `json:"operation_state"`
	OperationRecordedAt   int64       `json:"operation_recorded_at,omitempty"`
	UnresolvedOperations  []string    `json:"unresolved_operations"`
	PendingOperationExits int         `json:"pending_operation_exits"`
	Reservation           Reservation `json:"reservation"`
	IssueState            string      `json:"issue_state"`
	IssueVerifiedAt       int64       `json:"issue_verified_at,omitempty"`
	ItemState             string      `json:"item_state"`
	Claims                []string    `json:"claims"`
	DecisionAction        string      `json:"decision_action,omitempty"`
	RemainingCondition    string      `json:"remaining_condition,omitempty"`
	NextOwner             string      `json:"next_owner"`
	LastResult            *int        `json:"last_exit_code,omitempty"`
	LastResultAt          int64       `json:"last_result_at,omitempty"`
	VerifiedAt            int64       `json:"verified_at"`
	Drift                 []string    `json:"drift"`

	// Derived, never edited: the facts the single reservation state cannot express.
	WorkerPhase          string     `json:"worker_phase"`
	Continuation         string     `json:"continuation"`
	Custody              Custody    `json:"custody"`
	Acceptance           Acceptance `json:"acceptance"`
	OperationHandledAt   int64      `json:"operation_handled_at,omitempty"`
	OperationHandledNote string     `json:"operation_handled_note,omitempty"`
}

// Custody lists what still binds this assignment, separately from its state.
type Custody struct {
	ClaimsOnItem         int               `json:"claims_on_item"`
	OtherClaims          []string          `json:"other_claims"`
	ExecutionPins        int               `json:"execution_pins"`
	UnresolvedOperations int               `json:"unresolved_operations"`
	PendingEvents        int               `json:"pending_events"`
	NativeFenced         bool              `json:"native_fenced"`
	ClientRetirement     *RetirementStatus `json:"client_retirement,omitempty"`
}

// Acceptance states how much of the original goal an owner recorded as met. It is
// an assertion, never closure authority; unrecorded is the default.
type Acceptance struct {
	Coverage    string `json:"coverage"`
	Ref         string `json:"ref,omitempty"`
	GoalReached bool   `json:"goal_reached"`
}

// AcceptanceCoverages are the values an owner may record with closeout or partial retirement.
var AcceptanceCoverages = map[string]bool{"source-complete": true, "diagnostic-archived": true, "limited-sample": true, "full": true}

// Delivery reads one explicit reservation in this ledger. It does not mirror
// item files, update observation timestamps or infer native process liveness.
func (s *Store) Delivery(ctx context.Context, key string, observed IssueObservation) (*DeliveryStatus, error) {
	r, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	out := &DeliveryStatus{Reservation: *r, ExecutionDisposition: r.State, IssueState: "unknown", WorkerState: "unknown", OperationState: "unknown", ItemState: "unknown", Claims: []string{}, Drift: []string{}, NextOwner: r.ReservedBy, VerifiedAt: s.now().Unix()}
	out.Phase, out.Revision, out.NextAction = "unknown", "unknown", "unknown"
	if observed.Source != "" && observed.Source != r.SourceRef {
		return nil, fmt.Errorf("issue observation source does not match reservation")
	}
	if observed.Source != "" {
		if observed.State != "OPEN" && observed.State != "CLOSED" {
			return nil, fmt.Errorf("issue state must be OPEN or CLOSED")
		}
		if observed.VerifiedAt <= 0 || observed.VerifiedAt > out.VerifiedAt {
			return nil, fmt.Errorf("invalid issue observation time")
		}
		out.IssueVerifiedAt = observed.VerifiedAt
		if out.VerifiedAt-observed.VerifiedAt <= int64((5 * time.Minute).Seconds()) {
			out.IssueState = strings.ToLower(observed.State)
		} else {
			out.Drift = append(out.Drift, "issue-observation-stale")
		}
	}
	err = s.db.QueryRowContext(ctx, `SELECT status FROM items WHERE repo_id=? AND item_id=?`, s.repoID, r.CanonicalItemID).Scan(&out.ItemState)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT action,condition,worker_agent FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.repoID, key, r.Generation, r.CanonicalItemID).Scan(&out.DecisionAction, &out.RemainingCondition, &out.NextOwner)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	// A retained decision records the last execution scope, not ownership of
	// terminal reconciliation after that execution has ended.
	if r.State == "completed" || r.State == "failed" || r.State == "cancelled" {
		out.NextOwner = r.ReservedBy
	}
	rows, err := s.db.QueryContext(ctx, `SELECT item_id FROM claims WHERE repo_id=? AND agent_id IN (SELECT agent_id FROM claims WHERE repo_id=? AND item_id=? UNION SELECT agent_id FROM claim_history WHERE repo_id=? AND item_id=? AND claimed_at>=?) ORDER BY item_id LIMIT 101`, s.repoID, s.repoID, r.CanonicalItemID, s.repoID, r.CanonicalItemID, r.ReservedAt)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item string
		if err = rows.Scan(&item); err != nil {
			rows.Close()
			return nil, err
		}
		out.Claims = append(out.Claims, item)
	}
	if len(out.Claims) > 100 {
		rows.Close()
		return nil, fmt.Errorf("claim projection exceeds bounded limit")
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var exit int
	var at int64
	err = s.db.QueryRowContext(ctx, `SELECT exit_code,created_at FROM attestations WHERE repo_id=? AND item_id=? AND created_at>=? ORDER BY created_at DESC,id DESC LIMIT 1`, s.repoID, r.CanonicalItemID, r.ReservedAt).Scan(&exit, &at)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		out.LastResult = &exit
		out.LastResultAt = at
	}
	var operation string
	err = s.db.QueryRowContext(ctx, `SELECT body,ts FROM messages WHERE repo_id=? AND kind='owned-operation' AND json_valid(body) AND json_extract(body,'$.request.reservation')=? AND json_extract(body,'$.request.generation')=? AND json_extract(body,'$.request.worker_session')=? ORDER BY id DESC LIMIT 1`, s.repoID, key, r.Generation, r.WorkerThreadID).Scan(&operation, &out.OperationRecordedAt)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		var observed struct {
			State         string `json:"state"`
			AttestationID int64  `json:"attestation_id"`
			Request       struct {
				Phase      string `json:"phase"`
				Revision   string `json:"revision"`
				ResumeStep string `json:"resume_step"`
				RequestKey string `json:"request_key"`
			} `json:"request"`
		}
		if json.Unmarshal([]byte(operation), &observed) == nil {
			if observed.Request.Phase != "" {
				out.Phase = observed.Request.Phase
			}
			if observed.Request.Revision != "" {
				out.Revision = observed.Request.Revision
			}
			if observed.Request.ResumeStep != "" {
				out.NextAction = observed.Request.ResumeStep
			}
			out.OperationReference = observed.Request.RequestKey
			out.OperationAttestation = observed.AttestationID
			switch observed.State {
			case "started":
				out.OperationState = "started-unresolved"
			case "exited", "result-unavailable", "not-started", "exited-without-evidence":
				out.OperationState = observed.State
			}
		}
	}
	out.UnresolvedOperations, err = store.UnresolvedOperationKeys(ctx, s.db, s.repoID, "", key, r.WorkerThreadID, r.Generation)
	if err != nil {
		return nil, err
	}
	if len(out.UnresolvedOperations) > 0 {
		out.Drift = append(out.Drift, "owned-operation-unresolved")
	}
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND worker_session=? AND kind='operation-exit' AND processed_at=0`, s.repoID, key, r.Generation, r.WorkerThreadID).Scan(&out.PendingOperationExits); err != nil {
		return nil, err
	}
	if out.PendingOperationExits > 0 {
		out.Drift = append(out.Drift, "operation-exit-unhandled")
	}
	if r.State == "failed" {
		var partial int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE repo_id=? AND agent_id=? AND thread=? AND kind='partial-retirement' AND json_valid(body) AND json_extract(body,'$.request.expected.reservation_key')=? AND json_extract(body,'$.request.expected.generation')=? AND json_extract(body,'$.request.expected.worker_thread_id')=?`, s.repoID, r.ReservedBy, r.CanonicalItemID, key, r.Generation, r.WorkerThreadID).Scan(&partial); err != nil {
			return nil, err
		}
		if partial > 0 {
			out.ExecutionDisposition = "partial"
			out.NextOwner = r.ReservedBy
		}
	}
	s.projectState(ctx, key, r, out)
	if out.IssueState == "open" && r.State == "completed" {
		out.Drift = append(out.Drift, "open-issue-completed-reservation")
	}
	if out.IssueState == "closed" && (r.State == "reserved" || r.State == "dispatched") {
		out.Drift = append(out.Drift, "closed-issue-active-reservation")
	}
	if (r.State == "completed" || r.State == "failed" || r.State == "cancelled") && len(out.Claims) > 0 {
		out.Drift = append(out.Drift, "terminal-reservation-retained-claims")
	}
	if out.ItemState == "blocked" && out.IssueState != "closed" {
		out.Drift = append(out.Drift, "issue-delivery-pending")
	}
	if r.State == "dispatched" && out.WorkerPhase == "ended" {
		out.Drift = append(out.Drift, "worker-phase-ended-reservation-active")
	}
	if r.State == "completed" && out.ItemState == "blocked" {
		out.Drift = append(out.Drift, "blocked-item-completed-reservation")
	}
	return out, nil
}

// projectState derives phase, continuation, custody and acceptance coverage from
// existing records. It writes nothing.
func (s *Store) projectState(ctx context.Context, key string, r *Reservation, out *DeliveryStatus) {
	out.Custody = Custody{OtherClaims: []string{}}
	for _, item := range out.Claims {
		if item == r.CanonicalItemID {
			out.Custody.ClaimsOnItem++
		} else {
			out.Custody.OtherClaims = append(out.Custody.OtherClaims, item)
		}
	}
	out.Custody.UnresolvedOperations = len(out.UnresolvedOperations)
	out.Custody.PendingEvents = 0
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND worker_session=? AND processed_at=0`, s.repoID, key, r.Generation, r.WorkerThreadID).Scan(&out.Custody.PendingEvents)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND state='active' AND holder IN (SELECT agent_id FROM claims WHERE repo_id=? AND item_id=? UNION SELECT agent_id FROM claim_history WHERE repo_id=? AND item_id=? AND claimed_at>=?)`, s.repoID, s.repoID, r.CanonicalItemID, s.repoID, r.CanonicalItemID, r.ReservedAt).Scan(&out.Custody.ExecutionPins)
	var fenced int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM worker_native_fences WHERE repo_id=? AND native_session=?`, s.repoID, r.WorkerThreadID).Scan(&fenced)
	out.Custody.NativeFenced = fenced > 0
	if r.WorkerThreadID != "" {
		if st, err := s.Retirement(ctx, RetirementRequest{Reservation: key, Generation: r.Generation, WorkerSession: r.WorkerThreadID}); err == nil {
			out.Custody.ClientRetirement = st
		}
	}

	switch r.State {
	case "completed", "failed", "cancelled":
		out.WorkerPhase = "ended"
	case "dispatched":
		out.WorkerPhase = "live"
		if out.Custody.ClaimsOnItem == 0 && (out.ItemState == "done" || out.ItemState == "blocked") && out.Custody.PendingEvents == 0 && out.Custody.UnresolvedOperations == 0 {
			out.WorkerPhase = "ended"
		}
	default:
		out.WorkerPhase = "unknown"
	}
	switch {
	case out.IssueState == "closed":
		out.Continuation = "none"
	case out.ExecutionDisposition == "partial":
		out.Continuation = "partial-retired"
	case out.WorkerPhase == "live":
		out.Continuation = "in-progress"
	case out.WorkerPhase == "ended":
		out.Continuation = "awaiting-owner"
	default:
		out.Continuation = "unknown"
	}
	if out.WorkerPhase == "ended" {
		// The finished Worker no longer owns the next step.
		out.NextOwner = r.ReservedBy
	}

	out.Acceptance = Acceptance{Coverage: "unrecorded"}
	var coverage, ref string
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(body,'$.coverage'),COALESCE(json_extract(body,'$.ref'),'') FROM messages WHERE repo_id=? AND thread=? AND kind='acceptance-coverage' AND json_valid(body) AND json_extract(body,'$.reservation')=? AND json_extract(body,'$.generation')=? ORDER BY id DESC LIMIT 1`, s.repoID, r.CanonicalItemID, key, r.Generation).Scan(&coverage, &ref)
	if err == nil && AcceptanceCoverages[coverage] {
		out.Acceptance.Coverage, out.Acceptance.Ref = coverage, ref
	}
	out.Acceptance.GoalReached = out.Acceptance.Coverage == "full" && out.IssueState == "closed"

	var handledAt int64
	var note string
	err = s.db.QueryRowContext(ctx, `SELECT e.processed_at,e.processed_note FROM terminal_event_receipts e WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.worker_session=? AND e.kind='operation-exit' ORDER BY e.source_message_id DESC LIMIT 1`, s.repoID, key, r.Generation, r.WorkerThreadID).Scan(&handledAt, &note)
	if err == nil && handledAt > 0 {
		out.OperationHandledAt, out.OperationHandledNote = handledAt, note
	}
	// A recorded exit with no routed receipt is drift, not a pending Worker action.
	var unrouted int
	if s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages m WHERE m.repo_id=? AND m.kind='owned-operation' AND json_valid(m.body) AND json_extract(m.body,'$.request.reservation')=? AND json_extract(m.body,'$.request.generation')=? AND json_extract(m.body,'$.request.worker_session')=? AND json_extract(m.body,'$.state') IN ('exited','exited-without-evidence') AND NOT EXISTS(SELECT 1 FROM terminal_event_receipts e WHERE e.repo_id=m.repo_id AND e.kind='operation-exit' AND e.source_message_id=m.id)`, s.repoID, key, r.Generation, r.WorkerThreadID).Scan(&unrouted) == nil && unrouted > 0 {
		out.Drift = append(out.Drift, "operation-exit-unrouted")
	}
}
