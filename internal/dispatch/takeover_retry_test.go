package dispatch

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	sqlite "modernc.org/sqlite"
)

// Use real SQLite transactions, but deterministically fail the first commit
// after rolling back its writes. No timing or production retry hook is needed.
type takeoverRetryConnector struct {
	dsn     string
	busy    error
	change  string
	commits int
}

func (c *takeoverRetryConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *takeoverRetryConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &takeoverRetryConn{Conn: conn, control: c}, nil
}

type takeoverRetryConn struct {
	driver.Conn
	control *takeoverRetryConnector
}

func (c *takeoverRetryConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *takeoverRetryConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &takeoverRetryTx{Tx: tx, conn: c}, nil
}

type takeoverRetryTx struct {
	driver.Tx
	conn *takeoverRetryConn
}

func (tx *takeoverRetryTx) Commit() error {
	c := tx.conn.control
	c.commits++
	if c.commits != 1 {
		return tx.Tx.Commit()
	}
	if err := tx.Rollback(); err != nil {
		return err
	}
	// Emulate a receipt transition committed by another owner between attempts.
	if _, err := tx.conn.Conn.(driver.ExecerContext).ExecContext(context.Background(), c.change, nil); err != nil {
		return err
	}
	return c.busy
}

func TestTakeoverRetryReturnsOnlyCommittedEvents(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		want         []string
	}{
		{"unchanged", "SELECT 1", []string{"old-event"}},
		{"replaced", "UPDATE terminal_event_receipts SET event_id='new-event'", []string{"new-event"}},
		{"processed", "UPDATE terminal_event_receipts SET processed_at=10001", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := takeoverFixture(t)
			var seq int
			var name, path string
			if err := s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			dsn := "file:" + path + "?_pragma=busy_timeout(0)&_pragma=foreign_keys(ON)&_txlock=immediate"
			other, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			locked, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			// Obtain the real SQLITE_BUSY error type used by WithTxRetry.
			_, busy := other.Exec("UPDATE terminal_event_receipts SET processed_at=1")
			if err := locked.Rollback(); err != nil {
				t.Fatal(err)
			}
			var sqlErr *sqlite.Error
			if !errors.As(busy, &sqlErr) || sqlErr.Code() != 5 {
				t.Fatalf("expected SQLITE_BUSY, got %v", busy)
			}
			control := &takeoverRetryConnector{dsn: dsn, busy: busy, change: tc.change}
			db := sql.OpenDB(control)
			db.SetMaxOpenConns(1)
			defer db.Close()
			s.db = db
			r, err := s.Takeover(context.Background(), "operator", q)
			if err != nil {
				t.Fatal(err)
			}
			if control.commits != 2 {
				t.Fatalf("expected rollback then committed retry, got %d attempts", control.commits)
			}
			if !reflect.DeepEqual(r.PendingEvents, tc.want) {
				t.Errorf("returned events = %v, want only committed events %v", r.PendingEvents, tc.want)
			}
			var body string
			if err := db.QueryRow("SELECT body FROM messages WHERE id=?", r.AuditMessageID).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var audit struct {
				Pending []string `json:"pending_events"`
			}
			if err := json.Unmarshal([]byte(body), &audit); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(audit.Pending, tc.want) {
				t.Errorf("audit events = %v, want only committed events %v", audit.Pending, tc.want)
			}
			var history, messages, generation int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM claim_history),
				(SELECT count(*) FROM messages WHERE kind='session-takeover'),
				(SELECT generation FROM claims WHERE item_id='T')`).Scan(&history, &messages, &generation); err != nil {
				t.Fatal(err)
			}
			if history != 1 || messages != 1 || generation != 2 || r.ClaimGeneration != 2 || r.Reservation.Generation != 2 {
				t.Fatalf("rolled-back custody leaked: history=%d audit=%d generation=%d result=%+v", history, messages, generation, r)
			}
		})
	}
}
