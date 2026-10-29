// Package closeout resumes explicitly requested Worker closeout across external
// GitHub and local Squad operations. It does not schedule or approve work.
package closeout

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
	"github.com/zsiec/squad/internal/terminalevents"
)

type Request struct {
	Reservation      string `json:"reservation"`
	Generation       int64  `json:"generation"`
	WorkerSession    string `json:"worker_session"`
	ExpectedDecision int64  `json:"expected_decision"`
	RequestKey       string `json:"request_key"`
	Body             string `json:"body"`
}

type Backend interface {
	// These operations must be bounded and idempotent by the supplied identity.
	Done(context.Context, string) error
	Comment(context.Context, string, string, string) error
	Close(context.Context, string) error
}

type Engine struct {
	DB          *sql.DB
	Repo, Actor string
	Backend     Backend
}
type Result struct {
	Item    string                       `json:"item"`
	Steps   []string                     `json:"steps"`
	Outcome *terminalevents.SubmitResult `json:"outcome,omitempty"`
}
type receipt struct {
	Schema, Key, Digest, Step, Token string
	Generation                       int64
	State                            string
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)
var issuePattern = regexp.MustCompile(`^github:([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*)#([1-9][0-9]*)$`)

func (e Engine) binding(ctx context.Context, q Request) (item, source string, err error) {
	if e.Repo == "" || e.Actor == "" || !tokenPattern.MatchString(q.Reservation) || !tokenPattern.MatchString(q.RequestKey) || q.Generation < 1 || !tokenPattern.MatchString(q.WorkerSession) || strings.TrimSpace(q.Body) == "" || len(q.Body) > 16384 {
		return "", "", fmt.Errorf("explicit assignment identity, request key and bounded acceptance summary required")
	}
	var started int64
	var state string
	err = e.DB.QueryRowContext(ctx, `SELECT canonical_item_id,source_ref,reserved_at,state FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=? AND worker_thread_id=?`, e.Repo, q.Reservation, q.Generation, q.WorkerSession).Scan(&item, &source, &started, &state)
	if err != nil {
		return "", "", err
	}
	if state != "dispatched" && state != "completed" {
		return "", "", fmt.Errorf("assignment is not eligible for closeout")
	}
	if !issuePattern.MatchString(source) {
		return "", "", fmt.Errorf("closeout requires a canonical GitHub Issue")
	}
	var n int
	err = e.DB.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT agent_id,claimed_at FROM claims WHERE repo_id=? AND item_id=? UNION ALL SELECT agent_id,claimed_at FROM claim_history WHERE repo_id=? AND item_id=?) WHERE agent_id=? AND claimed_at>=?`, e.Repo, item, e.Repo, item, e.Actor, started).Scan(&n)
	if err != nil {
		return "", "", err
	}
	if n == 0 {
		return "", "", fmt.Errorf("caller has no custody of this assignment")
	}
	var other int
	if err = e.DB.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT agent_id,claimed_at FROM claims WHERE repo_id=? AND item_id=? UNION ALL SELECT agent_id,claimed_at FROM claim_history WHERE repo_id=? AND item_id=?) WHERE agent_id!=? AND claimed_at>=?`, e.Repo, item, e.Repo, item, e.Actor, started).Scan(&other); err != nil {
		return "", "", err
	}
	if other != 0 {
		return "", "", fmt.Errorf("assignment has ambiguous Worker custody")
	}
	var rev int64
	var action, worker string
	err = e.DB.QueryRowContext(ctx, `SELECT revision,action,worker_agent FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, e.Repo, q.Reservation, q.Generation, item).Scan(&rev, &action, &worker)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if err == nil && (rev != q.ExpectedDecision || action != "proceed" || worker != e.Actor) {
		return "", "", fmt.Errorf("current assignment decision does not permit closeout")
	}
	err = e.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM claims WHERE repo_id=? AND agent_id=? AND item_id!=?)+(SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND holder=? AND state='active')`, e.Repo, e.Actor, item, e.Repo, e.Actor).Scan(&n)
	if err != nil {
		return "", "", err
	}
	if n > 0 {
		return "", "", fmt.Errorf("release or reconcile owned external/resource custody before closeout")
	}
	unresolved, err := store.UnresolvedOperation(ctx, e.DB, e.Repo, e.Actor, q.Reservation, q.WorkerSession, q.Generation)
	if err != nil {
		return "", "", err
	}
	if unresolved {
		return "", "", fmt.Errorf("owned operation or exit handling remains unresolved")
	}
	return item, source, nil
}

func digest(q Request) string {
	q.ExpectedDecision = 0
	b, _ := json.Marshal(q)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (e Engine) acquire(ctx context.Context, item string, q Request, step string) (receipt, bool, error) {
	var out receipt
	done := false
	err := store.WithTxRetry(ctx, e.DB, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT body,ts FROM messages WHERE repo_id=? AND agent_id=? AND thread=? AND kind='closeout' AND json_valid(body) AND json_extract(body,'$.Key')=? AND json_extract(body,'$.Generation')=? ORDER BY id DESC LIMIT 1001`, e.Repo, e.Actor, item, q.Reservation+"/"+q.RequestKey, q.Generation)
		if err != nil {
			return err
		}
		var found *receipt
		var at int64
		count := 0
		for rows.Next() {
			count++
			if count > 1000 {
				rows.Close()
				return fmt.Errorf("closeout history exceeds bounded limit")
			}
			var body string
			var ts int64
			if err = rows.Scan(&body, &ts); err != nil {
				rows.Close()
				return err
			}
			var r receipt
			if json.Unmarshal([]byte(body), &r) == nil && r.Schema == "squad.closeout-step.v1" && r.Key == q.Reservation+"/"+q.RequestKey && r.Generation == q.Generation {
				if r.Digest != digest(q) {
					rows.Close()
					return fmt.Errorf("request key payload conflict")
				}
				if r.Step == step && found == nil {
					copy := r
					found = &copy
					at = ts
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if found != nil {
			if found.State == "done" {
				out = *found
				done = true
				return nil
			}
			if found.State == "running" && time.Now().Unix()-at < 120 {
				return fmt.Errorf("closeout step in flight or response unknown; retry after its bounded lease")
			}
		}
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			return err
		}
		out = receipt{Schema: "squad.closeout-step.v1", Key: q.Reservation + "/" + q.RequestKey, Digest: digest(q), Step: step, Token: hex.EncodeToString(token[:]), Generation: q.Generation, State: "running"}
		return appendReceipt(ctx, tx, e.Repo, e.Actor, item, out)
	})
	return out, done, err
}
func appendReceipt(ctx context.Context, tx *sql.Tx, repo, actor, item string, r receipt) error {
	b, _ := json.Marshal(r)
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,'closeout',?,'[]','normal')`, repo, time.Now().Unix(), actor, item, string(b))
	return err
}
func (e Engine) finish(ctx context.Context, item string, r receipt, state string) error {
	r.State = state
	return store.WithTxRetry(ctx, e.DB, func(tx *sql.Tx) error {
		var body string
		if err := tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE repo_id=? AND agent_id=? AND thread=? AND kind='closeout' AND json_valid(body) AND json_extract(body,'$.Key')=? AND json_extract(body,'$.Generation')=? AND json_extract(body,'$.Step')=? ORDER BY id DESC LIMIT 1`, e.Repo, e.Actor, item, r.Key, r.Generation, r.Step).Scan(&body); err != nil {
			return err
		}
		var latest receipt
		if json.Unmarshal([]byte(body), &latest) != nil || latest.Token != r.Token || latest.State != "running" {
			return fmt.Errorf("closeout lease changed")
		}
		return appendReceipt(ctx, tx, e.Repo, e.Actor, item, r)
	})
}

func (e Engine) Run(ctx context.Context, q Request) (Result, error) {
	var result Result
	item, source, err := e.binding(ctx, q)
	if err != nil {
		return result, err
	}
	result.Item = item
	result.Steps = []string{}
	if e.Backend == nil {
		return result, fmt.Errorf("closeout backend required")
	}
	// Done runs the existing verification/evidence gates. No GitHub closure is
	// attempted until they pass. A local done item alone is never Issue delivery.
	for _, step := range []string{"item-done", "github-comment", "github-close", "outcome"} {
		if _, _, err = e.binding(ctx, q); err != nil {
			return result, err
		}
		r, done, err := e.acquire(ctx, item, q, step)
		if err != nil {
			return result, err
		}
		if !done {
			opCtx, cancel := context.WithTimeout(ctx, 60*time.Second)

			switch step {
			case "item-done":
				err = e.Backend.Done(opCtx, item)
			case "github-comment":
				err = e.Backend.Comment(opCtx, source, "<!-- squad-closeout:"+q.Reservation+"/"+fmt.Sprint(q.Generation)+"/"+q.RequestKey+" -->", q.Body)
			case "github-close":
				err = e.Backend.Close(opCtx, source)
			case "outcome":
				var out terminalevents.SubmitResult
				out, err = (terminalevents.Store{DB: e.DB, Repo: e.Repo}).Submit(opCtx, e.Actor, terminalevents.SubmitRequest{Reservation: q.Reservation, Generation: q.Generation, WorkerSession: q.WorkerSession, Kind: "issue-closed", Body: q.Body, ExpectedDecision: q.ExpectedDecision, RequestKey: "closeout-" + q.RequestKey})
				if err == nil {
					result.Outcome = &out
				}
			}
			cancel()
			state := "done"
			if err != nil {
				state = "retry"
			}
			saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			saveErr := e.finish(saveCtx, item, r, state)
			saveCancel()
			if err != nil {
				return result, fmt.Errorf("%s incomplete: %w", step, err)
			}
			if saveErr != nil {
				return result, saveErr
			}
		}
		result.Steps = append(result.Steps, step)
	}
	return result, nil
}

// ParseSource accepts canonical GitHub sources only, preventing URL/flag injection.
func ParseSource(source string) (repo, number string, err error) {
	m := issuePattern.FindStringSubmatch(source)
	if m == nil {
		return "", "", fmt.Errorf("invalid GitHub source")
	}
	return m[1], m[2], nil
}
