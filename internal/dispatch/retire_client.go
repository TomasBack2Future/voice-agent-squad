package dispatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ClientRetirement is the real, persisted result of one normal-exit attempt.
type ClientRetirement struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type ownedProcess struct {
	pid   int
	start string
	role  string
}

func processStart(ctx context.Context, pid int) string {
	v, err := legacyPS(ctx, pid, "lstart")
	if err != nil {
		return ""
	}
	return v
}

func ownedAlive(ctx context.Context, p ownedProcess) bool {
	if p.start == "" {
		return false
	}
	stat, err := legacyPS(ctx, p.pid, "stat")
	if err != nil || stat == "" || strings.HasPrefix(stat, "Z") {
		return false
	}
	return processStart(ctx, p.pid) == p.start
}

func descendants(parents map[int]int, root int) []int {
	var out []int
	frontier := []int{root}
	seen := map[int]bool{root: true}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		for pid, ppid := range parents {
			if ppid == cur && !seen[pid] {
				seen[pid] = true
				out = append(out, pid)
				frontier = append(frontier, pid)
			}
		}
	}
	return out
}

// RetireClient asks the exact finished assignment's own Claude client to end
// normally (SIGTERM, never SIGKILL) and verifies that the client and its own
// helpers are gone. It identifies processes by argv/kernel identity of this
// native only, never by name, port or PID range, and re-checks eligibility
// immediately before signalling.
func (s *Store) RetireClient(ctx context.Context, caller string, controller bool, q RetirementRequest) (*ClientRetirement, error) {
	token, done, st, err := s.beginRetirementStep(ctx, caller, controller, q, "client-exit")
	if err != nil {
		return nil, err
	}
	if done != nil {
		return &ClientRetirement{State: done.State, Detail: done.Detail}, nil
	}
	state, detail := s.terminateClient(ctx, q)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.finishRetirementStep(saveCtx, caller, st, q, "client-exit", token, state, detail); err != nil {
		return nil, err
	}
	return &ClientRetirement{State: state, Detail: detail}, nil
}

func (s *Store) terminateClient(ctx context.Context, q RetirementRequest) (string, string) {
	wait := s.retireWait
	if wait <= 0 {
		wait = 20 * time.Second
	}
	parents, err := legacyProcessParents(ctx)
	if err != nil {
		return "failed", "process table unavailable"
	}
	var clients []int
	var helpers []ownedProcess
	for pid, ppid := range parents {
		args, e := legacyProcessArguments(pid)
		if e != nil || len(args) == 0 {
			continue
		}
		if legacyClaudeNative(pid, q.WorkerSession) {
			clients = append(clients, pid)
			if sup, e := legacyProcessArguments(ppid); e == nil && ownsLauncher(sup) {
				helpers = append(helpers, ownedProcess{ppid, processStart(ctx, ppid), "supervisor"})
			}
			continue
		}
		if len(args) >= 3 && args[len(args)-2] == "--config" && strings.HasSuffix(args[len(args)-1], "/receiver-"+q.WorkerSession+"/config.json") {
			helpers = append(helpers, ownedProcess{pid, processStart(ctx, pid), "receiver"})
		}
	}
	if len(clients) > 1 {
		return "failed", "ambiguous native client processes: " + pidList(clients)
	}
	var client *ownedProcess
	if len(clients) == 1 {
		pid := clients[0]
		client = &ownedProcess{pid, processStart(ctx, pid), "client"}
		for _, d := range descendants(parents, pid) {
			helpers = append(helpers, ownedProcess{d, processStart(ctx, d), "descendant"})
		}
		// Re-qualify at the last moment: eligibility, identity and start time.
		st, e := s.Retirement(ctx, q)
		if e != nil || !st.Eligible {
			return "failed", "eligibility changed before signalling"
		}
		if client.start == "" || !legacyClaudeNative(pid, q.WorkerSession) || processStart(ctx, pid) != client.start {
			return "failed", "client identity changed before signalling"
		}
		if e = syscall.Kill(pid, syscall.SIGTERM); e != nil && !errors.Is(e, syscall.ESRCH) {
			return "failed", "signal rejected: " + e.Error()
		}
		if !waitGone(ctx, wait, *client) {
			return "exit-timeout", fmt.Sprintf("client %d still running after %s; retained", pid, wait)
		}
	}
	var remaining []string
	for _, h := range helpers {
		if h.pid == client.pidOrZero() {
			continue
		}
		if !waitGone(ctx, wait, h) {
			remaining = append(remaining, fmt.Sprintf("%s:%d", h.role, h.pid))
		}
	}
	if len(remaining) > 0 {
		return "helpers-remain", "client ended; own helpers still running: " + strings.Join(remaining, ",")
	}
	if client == nil {
		return "already-exited", "no live client for this native"
	}
	return "exited", fmt.Sprintf("client %d and %d own helper(s) ended", client.pid, len(helpers))
}

func (p *ownedProcess) pidOrZero() int {
	if p == nil {
		return 0
	}
	return p.pid
}

func ownsLauncher(args []string) bool {
	for _, a := range args {
		if filepath.Base(a) == "claude_worker_launcher.py" {
			return true
		}
	}
	return false
}

func waitGone(ctx context.Context, bound time.Duration, p ownedProcess) bool {
	deadline := time.Now().Add(bound)
	for {
		if !ownedAlive(ctx, p) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
