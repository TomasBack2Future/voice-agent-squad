package notify

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

func TestWake_ConnectsToEachEndpoint(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var hits atomic.Int64
	listeners := []net.Listener{}
	for i := 0; i < 3; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		listeners = append(listeners, l)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				hits.Add(1)
				_ = c.Close()
			}
		}()
	}
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()

	r := NewRegistry(db)
	for i, l := range listeners {
		port := l.Addr().(*net.TCPAddr).Port
		if err := r.Register(context.Background(), Endpoint{
			Instance: "inst-" + itoa(i), RepoID: "repo-x", Kind: KindListen, Port: port,
		}); err != nil {
			t.Fatalf("register: %v", err)
		}
	}

	if err := Wake(context.Background(), r, "repo-x", 100*time.Millisecond); err != nil {
		t.Fatalf("wake: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for hits.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("expected 3 wake connects, got %d", got)
	}
}

func TestWake_TolerantOfDeadEndpoints(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = live.Close() }()
	var hits atomic.Int64
	go func() {
		for {
			c, err := live.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			_ = c.Close()
		}
	}()

	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	deadPort := dead.Addr().(*net.TCPAddr).Port
	_ = dead.Close()

	r := NewRegistry(db)
	ctx := context.Background()
	_ = r.Register(ctx, Endpoint{Instance: "live", RepoID: "r", Kind: KindListen, Port: live.Addr().(*net.TCPAddr).Port})
	_ = r.Register(ctx, Endpoint{Instance: "dead", RepoID: "r", Kind: KindListen, Port: deadPort})

	if err := Wake(ctx, r, "r", 50*time.Millisecond); err != nil {
		t.Fatalf("wake should swallow dead-endpoint errors, got %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("live endpoint should have been hit exactly once, got %d", got)
	}
}

func TestWakeKind_OnlyConnectsMatchingEndpoints(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	matching, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = matching.Close() }()
	unrelated, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Close() }()

	var matchingHits, unrelatedHits atomic.Int64
	acceptCount := func(l net.Listener, hits *atomic.Int64) {
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				hits.Add(1)
				_ = c.Close()
			}
		}()
	}
	acceptCount(matching, &matchingHits)
	acceptCount(unrelated, &unrelatedHits)

	registry := NewRegistry(db)
	kind := ClaimWaitKind("ENV-001")
	ctx := context.Background()
	_ = registry.Register(ctx, Endpoint{
		Instance: "matching", RepoID: "repo", Kind: kind,
		Port: matching.Addr().(*net.TCPAddr).Port,
	})
	_ = registry.Register(ctx, Endpoint{
		Instance: "unrelated", RepoID: "repo", Kind: ClaimWaitKind("ENV-002"),
		Port: unrelated.Addr().(*net.TCPAddr).Port,
	})

	if err := WakeKind(ctx, registry, "repo", kind, 100*time.Millisecond); err != nil {
		t.Fatalf("WakeKind: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for matchingHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if matchingHits.Load() != 1 {
		t.Fatalf("matching hits=%d want 1", matchingHits.Load())
	}
	if unrelatedHits.Load() != 0 {
		t.Fatalf("unrelated hits=%d want 0", unrelatedHits.Load())
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
