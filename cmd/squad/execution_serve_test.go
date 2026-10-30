package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type serveOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *serveOutput) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *serveOutput) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// serveFixture runs the real binary the way launchd would: nothing inherited,
// no HOME, a PATH holding only a controllable gh stand-in.
type serveFixture struct {
	bin, repo, squadHome, token, ghDir string
}

func newServeFixture(t *testing.T, gh string) serveFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the service binary")
	}
	f := serveFixture{bin: buildServeBinary(t), repo: setupServeRepo(t), squadHome: t.TempDir(), ghDir: t.TempDir()}
	f.token = filepath.Join(t.TempDir(), "admission.token")
	if err := os.WriteFile(f.token, []byte(strings.Repeat("t", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ghDir, "gh"), []byte("#!/bin/sh\n"+gh+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f serveFixture) start(t *testing.T, listen string) (*exec.Cmd, *serveOutput) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, f.bin, "execution", "serve", "--listen", listen, "--token-file", f.token)
	cmd.Dir = f.repo
	cmd.Env = []string{"PATH=" + f.ghDir + ":/usr/bin:/bin", "SQUAD_HOME=" + f.squadHome, "SQUAD_AGENT=svc", "SQUAD_SESSION_ID=svc", "SQUAD_NO_AUTO_DAEMON=1", "SQUAD_NO_HYGIENE=1"}
	out := &serveOutput{}
	cmd.Stderr, cmd.Stdout = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, out
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitExit(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("service did not exit")
		return nil
	}
}

func TestServeRefusesToStartWithoutCredentials(t *testing.T) {
	f := newServeFixture(t, "echo 'gh auth login' >&2; exit 4")
	cmd, out := f.start(t, freeAddr(t))
	if err := waitExit(t, cmd); err == nil {
		t.Fatal("started without credentials")
	}
	if !strings.Contains(out.String(), "execution_startup_failed: credential_missing") || strings.Contains(out.String(), "listening") {
		t.Fatalf("output %q", out.String())
	}
}

func TestServeRefusesToStartWithoutGH(t *testing.T) {
	f := newServeFixture(t, "exit 0")
	_ = os.Remove(filepath.Join(f.ghDir, "gh"))
	cmd, out := f.start(t, freeAddr(t))
	if err := waitExit(t, cmd); err == nil || !strings.Contains(out.String(), "execution_startup_failed: tool_missing") {
		t.Fatalf("err %v output %q", err, out.String())
	}
}

func TestServeNeverClaimsAListenerItDoesNotOwn(t *testing.T) {
	f := newServeFixture(t, "exit 0")
	addr := freeAddr(t)
	first, firstOut := f.start(t, addr)
	defer func() { _ = first.Process.Kill(); _ = first.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(firstOut.String(), "listening on") {
		if time.Now().After(deadline) {
			t.Fatalf("first instance never listened: %q", firstOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	second, secondOut := f.start(t, addr)
	if err := waitExit(t, second); err == nil {
		t.Fatal("second instance started on a taken port")
	}
	if !strings.Contains(secondOut.String(), "listener_conflict") || !strings.Contains(secondOut.String(), "already served by this ledger") || strings.Contains(secondOut.String(), "listening on") {
		t.Fatalf("output %q", secondOut.String())
	}
	if first.ProcessState != nil {
		t.Fatal("the running instance was disturbed")
	}

	// Another process on the port is reported as foreign and left alone.
	foreignAddr := freeAddr(t)
	foreign := &http.Server{Addr: foreignAddr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hi")) })}
	ln, err := net.Listen("tcp", foreignAddr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = foreign.Serve(ln) }()
	defer func() { _ = foreign.Close() }()
	third, thirdOut := f.start(t, foreignAddr)
	if err := waitExit(t, third); err == nil || !strings.Contains(thirdOut.String(), "different listener") {
		t.Fatalf("err %v output %q", err, thirdOut.String())
	}
	resp, err := http.Get("http://" + foreignAddr + "/")
	if err != nil {
		t.Fatal("foreign listener was disturbed:", err)
	}
	_ = resp.Body.Close()
}

func buildServeBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "squad")
	if out, err := exec.Command("go", "build", "-o", bin, "./").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func setupServeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".squad", "items"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".squad", "config.yaml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
