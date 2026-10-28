package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestWorkerExecutionInspectRetainsExpiredAndReconciledCustody(t *testing.T) {
	s, b := workerExecutionFixture(t)
	ctx := context.Background()
	out, err := s.InspectWorkerExecution(ctx, b.Actor, b)
	if err != nil || out.State != "absent" {
		t.Fatal(out, err)
	}
	if _, err := s.InspectWorkerExecution(ctx, "foreign", b); err == nil {
		t.Fatal("foreign actor proved absence")
	}
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(b.ExpiresAt+1, 0) }
	out, err = s.InspectWorkerExecution(ctx, b.Actor, b)
	if err != nil || out.State != "active" || out.Gate != "gate-open" {
		t.Fatal("expired acquire remains observable", out, err)
	}
	if err := s.SuspendWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWorkerExecution(ctx, b.Actor, b, "original native/tools joined"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM dispatch_controller_bindings`); err != nil {
		t.Fatal(err)
	}
	out, err = s.InspectWorkerExecution(ctx, b.Actor, b)
	if err != nil || out.State != "reconciled" || out.Gate != "gate-closed" {
		t.Fatal("old custody cleanup remains observable", out, err)
	}
}

func TestWorkerExecutionInspectDoesNotClassifyForeignOrDatabaseErrorsAsAbsent(t *testing.T) {
	for _, statement := range []string{
		`UPDATE execution_authorizations SET holder='foreign'`,
		`UPDATE execution_authorizations SET item_id='other'`,
		`UPDATE execution_authorizations SET generation=2`,
		`UPDATE execution_authorizations SET binding='{}'`,
		`UPDATE execution_authorizations SET state='revoked'`,
		`UPDATE execution_authorizations SET last_step='unknown'`,
	} {
		t.Run(statement, func(t *testing.T) {
			s, b := workerExecutionFixture(t)
			if err := s.AcquireWorkerExecution(context.Background(), b.Actor, b); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			out, err := s.InspectWorkerExecution(context.Background(), b.Actor, b)
			if err == nil || out.State == "absent" {
				t.Fatal("ambiguous inspection became absence", out, err)
			}
		})
	}
	s, b := workerExecutionFixture(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := s.InspectWorkerExecution(context.Background(), b.Actor, b)
	if err == nil || out.State == "absent" {
		t.Fatal("database error became absence", out, err)
	}
}
