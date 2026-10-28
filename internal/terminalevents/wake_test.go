package terminalevents

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/notify"
)

func TestCommittedPublicationsWakeWaitingReceivers(t *testing.T) {
	for _, operation := range []string{"publish", "submit", "decide"} {
		t.Run(operation, func(t *testing.T) {
			s := fixture(t)
			ctx := context.Background()
			l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if err := notify.NewRegistry(s.DB).Register(ctx, notify.Endpoint{
				Instance: "terminal:recipient:incarnation", RepoID: s.Repo,
				Kind: notify.KindRewake, Port: l.Addr().(*net.TCPAddr).Port,
			}); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "publish":
				_, err = s.Publish(ctx, "worker", PublishRequest{
					Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session",
					Kind: "issue-closed", OutcomeID: 1,
				})
			case "submit":
				_, err = s.Submit(ctx, "worker", SubmitRequest{
					Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session",
					Kind: "blocked", Body: "isolated result",
				})
			case "decide":
				post(t, s, "Continue the isolated fixture", "dispatcher")
				_, err = s.Decide(ctx, "dispatcher", DecisionRequest{
					Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session",
					OutcomeID: 2, Action: "proceed",
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := l.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			connection, err := l.Accept()
			if err != nil {
				t.Fatalf("committed %s did not wake its receiver: %v", operation, err)
			}
			_ = connection.Close()
			var pending int
			if err := s.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts WHERE delivered_at=0 AND processed_at=0`).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("wake must follow durable publication without acknowledging it: %d %v", pending, err)
			}
		})
	}
}

func TestRejectedPublicationDoesNotWakeOrLeaveAnEvent(t *testing.T) {
	s := fixture(t)
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := notify.NewRegistry(s.DB).Register(context.Background(), notify.Endpoint{
		Instance: "terminal:recipient:incarnation", RepoID: s.Repo,
		Kind: notify.KindRewake, Port: l.Addr().(*net.TCPAddr).Port,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(context.Background(), "foreign", PublishRequest{
		Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session",
		Kind: "issue-closed", OutcomeID: 1,
	})
	if err == nil {
		t.Fatal("foreign publication accepted")
	}
	if err := l.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	connection, err := l.Accept()
	if err == nil {
		_ = connection.Close()
		t.Fatal("rejected publication woke receiver")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected event remained: %d %v", count, err)
	}
}
