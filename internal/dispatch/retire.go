package dispatch

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// RetirementRequest names the exact assignment whose finished Claude client may
// be retired. Nothing is derived from an Issue, a heartbeat or a quiet terminal.
type RetirementRequest struct {
	Reservation   string `json:"reservation"`
	Generation    int64  `json:"generation"`
	WorkerSession string `json:"worker_session"`
}

// RetirementStep is one independently recorded result. States are
// pending, running, exited, already-exited, exit-timeout, helpers-remain,
// cleared and failed; only exited/already-exited/cleared are terminal successes.
type RetirementStep struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	At     int64  `json:"at,omitempty"`
}

type RetirementStatus struct {
	Reservation Reservation    `json:"reservation"`
	Actor       string         `json:"actor,omitempty"`
	Disposition string         `json:"disposition"`
	Eligible    bool           `json:"eligible"`
	Reasons     []string       `json:"reasons"`
	ClientExit  RetirementStep `json:"client_exit"`
	Color       RetirementStep `json:"color"`
}

const (
	retirementKind   = "client-retirement"
	retirementSchema = "squad.client-retirement.v1"
	retirementLease  = 120
)

var retirementToken = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

type retirementReceipt struct {
	Schema        string `json:"schema"`
	Reservation   string `json:"reservation"`
	Generation    int64  `json:"generation"`
	WorkerSession string `json:"worker_session"`
	Step          string `json:"step"`
	State         string `json:"state"`
	Token         string `json:"token"`
	Detail        string `json:"detail,omitempty"`
}

func retirementSuccess(state string) bool {
	return state == "exited" || state == "already-exited" || state == "cleared"
}

func (q RetirementRequest) valid() bool {
	return retirementToken.MatchString(q.Reservation) && retirementToken.MatchString(q.WorkerSession) && q.Generation >= 1
}

// Retirement evaluates whether the exact finished assignment may lose its
// client and returns every recorded step. It writes nothing.
func (s *Store) Retirement(ctx context.Context, q RetirementRequest) (*RetirementStatus, error) {
	return s.retirement(ctx, s.db, q)
}

type retirementQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) retirement(ctx context.Context, db retirementQuerier, q RetirementRequest) (*RetirementStatus, error) {
	if !q.valid() {
		return nil, errors.New("exact reservation, generation and worker session required")
	}
	r, err := scanReservation(db.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, q.Reservation))
	if err != nil {
		return nil, err
	}
	out := &RetirementStatus{Reservation: *r, Disposition: r.State, Reasons: []string{}, ClientExit: RetirementStep{State: "pending"}, Color: RetirementStep{State: "pending"}}
	refuse := func(reason string) { out.Reasons = append(out.Reasons, reason) }
	if r.Generation != q.Generation {
		refuse("stale-generation")
	}
	if r.WorkerThreadID != q.WorkerSession {
		refuse("worker-session-mismatch")
	}
	switch r.State {
	case "completed", "cancelled":
	case "failed":
		var partial int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE repo_id=? AND agent_id=? AND thread=? AND kind='partial-retirement' AND json_valid(body) AND json_extract(body,'$.request.expected.reservation_key')=? AND json_extract(body,'$.request.expected.generation')=? AND json_extract(body,'$.request.expected.worker_thread_id')=?`, s.repoID, r.ReservedBy, r.CanonicalItemID, q.Reservation, r.Generation, r.WorkerThreadID).Scan(&partial); err != nil {
			return nil, err
		}
		if partial == 0 {
			refuse("failed-without-handoff-evidence")
		} else {
			out.Disposition = "partial"
		}
	default:
		refuse("reservation-not-terminal")
	}
	actors, err := retirementActors(ctx, db, s.repoID, r)
	if err != nil {
		return nil, err
	}
	switch len(actors) {
	case 0:
	case 1:
		out.Actor = actors[0]
	default:
		refuse("ambiguous-worker-custody")
	}
	if len(actors) > 0 {
		var n int
		args := []any{s.repoID}
		marks := make([]string, len(actors))
		for i, a := range actors {
			marks[i] = "?"
			args = append(args, a)
		}
		in := strings.Join(marks, ",")
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND agent_id IN (`+in+`)`, args...).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			refuse("claims-retained")
		}
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND state='active' AND holder IN (`+in+`)`, args...).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			refuse("execution-custody-active")
		}
	}
	var anyClaim int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, r.CanonicalItemID).Scan(&anyClaim); err != nil {
		return nil, err
	}
	if anyClaim > 0 {
		refuse("canonical-item-claimed")
	}
	unresolved, err := store.UnresolvedOperationKeys(ctx, db, s.repoID, "", q.Reservation, q.WorkerSession, q.Generation)
	if err != nil {
		return nil, err
	}
	if len(unresolved) > 0 {
		refuse("owned-operation-unresolved")
	}
	var pending int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND worker_session=? AND processed_at=0`, s.repoID, q.Reservation, q.Generation, q.WorkerSession).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		refuse("terminal-event-unhandled")
	}
	var fenced int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM worker_native_fences WHERE repo_id=? AND native_session=?`, s.repoID, q.WorkerSession).Scan(&fenced); err != nil {
		return nil, err
	}
	if fenced > 0 {
		refuse("native-fenced")
	}
	if out.Disposition != "partial" {
		var action string
		err = db.QueryRowContext(ctx, `SELECT action FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.repoID, q.Reservation, r.Generation, r.CanonicalItemID).Scan(&action)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && action == "hold" {
			refuse("decision-hold")
		}
	}
	out.Eligible = len(out.Reasons) == 0
	for _, step := range []string{"client-exit", "color"} {
		latest, at, found, e := latestRetirementReceipt(ctx, db, s.repoID, r.CanonicalItemID, q, step)
		if e != nil {
			return nil, e
		}
		if !found {
			continue
		}
		state := RetirementStep{State: latest.State, Detail: latest.Detail, At: at}
		if step == "client-exit" {
			out.ClientExit = state
		} else {
			out.Color = state
		}
	}
	sort.Strings(out.Reasons)
	return out, nil
}

// retirementActors returns every Worker identity that held this assignment's item.
func retirementActors(ctx context.Context, db retirementQuerier, repo string, r *Reservation) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT agent_id FROM (SELECT agent_id FROM claims WHERE repo_id=? AND item_id=? UNION SELECT agent_id FROM claim_history WHERE repo_id=? AND item_id=? AND claimed_at>=?) ORDER BY agent_id LIMIT 11`, repo, r.CanonicalItemID, repo, r.CanonicalItemID, r.ReservedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) > 10 {
		return nil, errors.New("worker custody history exceeds bounded limit")
	}
	return out, rows.Err()
}

func latestRetirementReceipt(ctx context.Context, db retirementQuerier, repo, item string, q RetirementRequest, step string) (retirementReceipt, int64, bool, error) {
	var body string
	var at int64
	err := db.QueryRowContext(ctx, `SELECT body,ts FROM messages WHERE repo_id=? AND thread=? AND kind=? AND json_valid(body) AND json_extract(body,'$.schema')=? AND json_extract(body,'$.reservation')=? AND json_extract(body,'$.generation')=? AND json_extract(body,'$.worker_session')=? AND json_extract(body,'$.step')=? ORDER BY id DESC LIMIT 1`, repo, item, retirementKind, retirementSchema, q.Reservation, q.Generation, q.WorkerSession, step).Scan(&body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return retirementReceipt{}, 0, false, nil
	}
	if err != nil {
		return retirementReceipt{}, 0, false, err
	}
	var r retirementReceipt
	if json.Unmarshal([]byte(body), &r) != nil {
		return retirementReceipt{}, 0, false, errors.New("retirement receipt unreadable")
	}
	return r, at, true, nil
}

// authorizeRetirement permits the original Worker (its actor) or the owning
// Dispatcher controller (caller already verified as controller by the CLI).
func authorizeRetirement(st *RetirementStatus, caller string, controller bool) error {
	if controller && st.Reservation.ReservedBy == caller {
		return nil
	}
	if !controller && st.Actor != "" && st.Actor == caller {
		return nil
	}
	return errors.New("only the original Worker or the owning Dispatcher may retire this client")
}

// beginRetirementStep records an intent unless the step already succeeded or a
// live attempt holds the lease. Retrying after a crash re-enters here.
func (s *Store) beginRetirementStep(ctx context.Context, caller string, controller bool, q RetirementRequest, step string) (token string, done *RetirementStep, st *RetirementStatus, err error) {
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		token, done = "", nil
		st, err = s.retirement(ctx, tx, q)
		if err != nil {
			return err
		}
		if err = authorizeRetirement(st, caller, controller); err != nil {
			return err
		}
		if !st.Eligible {
			return fmt.Errorf("retirement not permitted: %s", strings.Join(st.Reasons, ","))
		}
		latest, at, found, e := latestRetirementReceipt(ctx, tx, s.repoID, st.Reservation.CanonicalItemID, q, step)
		if e != nil {
			return e
		}
		if found {
			if retirementSuccess(latest.State) {
				done = &RetirementStep{State: latest.State, Detail: latest.Detail, At: at}
				return nil
			}
			if latest.State == "running" && s.now().Unix()-at < retirementLease {
				return errors.New("retirement step in flight or response unknown; retry after its bounded lease")
			}
		}
		var raw [16]byte
		if _, e = rand.Read(raw[:]); e != nil {
			return e
		}
		token = hex.EncodeToString(raw[:])
		return s.appendRetirementReceipt(ctx, tx, caller, st.Reservation.CanonicalItemID, retirementReceipt{Schema: retirementSchema, Reservation: q.Reservation, Generation: q.Generation, WorkerSession: q.WorkerSession, Step: step, State: "running", Token: token})
	})
	return
}

func (s *Store) appendRetirementReceipt(ctx context.Context, tx *sql.Tx, caller, item string, r retirementReceipt) error {
	if len(r.Detail) > 512 {
		r.Detail = r.Detail[:512]
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,?,?,'[]','normal')`, s.repoID, s.now().Unix(), caller, item, retirementKind, string(b))
	return err
}

// finishRetirementStep records the real result only for the attempt that holds
// the intent; a superseded attempt cannot overwrite a newer one.
func (s *Store) finishRetirementStep(ctx context.Context, caller string, st *RetirementStatus, q RetirementRequest, step, token, state, detail string) error {
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		latest, _, found, err := latestRetirementReceipt(ctx, tx, s.repoID, st.Reservation.CanonicalItemID, q, step)
		if err != nil {
			return err
		}
		if !found || latest.State != "running" || latest.Token != token {
			return errors.New("retirement lease changed")
		}
		return s.appendRetirementReceipt(ctx, tx, caller, st.Reservation.CanonicalItemID, retirementReceipt{Schema: retirementSchema, Reservation: q.Reservation, Generation: q.Generation, WorkerSession: q.WorkerSession, Step: step, State: state, Token: token, Detail: detail})
	})
}

func pidList(pids []int) string {
	s := make([]string, len(pids))
	for i, p := range pids {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

var _ = time.Second

var workspaceUUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// ColorRunner performs the presentation-only clear-color action for one explicit
// workspace UUID. A failure is recorded and never changes any other state.
type ColorRunner func(ctx context.Context, workspace string) error

// RetireColor clears the Worker workspace color after the same terminal gate as
// client retirement, with its own intent/result receipt. It does not require,
// imply or affect the client-exit step.
func (s *Store) RetireColor(ctx context.Context, caller string, q RetirementRequest, workspace string, run ColorRunner) (*ClientRetirement, error) {
	if !workspaceUUID.MatchString(workspace) || run == nil {
		return nil, errors.New("explicit workspace UUID required")
	}
	token, done, st, err := s.beginRetirementStep(ctx, caller, true, q, "color")
	if err != nil {
		return nil, err
	}
	if done != nil {
		return &ClientRetirement{State: done.State, Detail: done.Detail}, nil
	}
	state, detail := "cleared", "workspace "+strings.ToUpper(workspace)
	if e := run(ctx, workspace); e != nil {
		state, detail = "failed", "clear-color failed: "+e.Error()
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.finishRetirementStep(saveCtx, caller, st, q, "color", token, state, detail); err != nil {
		return nil, err
	}
	return &ClientRetirement{State: state, Detail: detail}, nil
}
