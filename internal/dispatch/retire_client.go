package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	if ctx.Err() != nil {
		return true // unknown is never "gone"
	}
	if err := syscall.Kill(p.pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	if stat, err := legacyPS(ctx, p.pid, "stat"); err == nil && strings.HasPrefix(stat, "Z") {
		return false
	}
	if p.start != "" {
		if now := processStart(ctx, p.pid); now != "" && now != p.start {
			return false // the PID now belongs to a different process
		}
	}
	return true
}

// ownProcessTable returns pid->ppid and the own-user pids whose argv is unreadable.
func ownProcessTable(ctx context.Context) (map[int]int, []int, error) {
	raw, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,uid=").Output()
	if err != nil {
		return nil, nil, err
	}
	parents := map[int]int{}
	var unreadable []int
	self := os.Getuid()
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		uid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		parents[pid] = ppid
		if uid == self {
			if args, e := legacyProcessArguments(pid); e != nil || len(args) == 0 {
				if syscall.Kill(pid, 0) == nil {
					if stat, e := legacyPS(ctx, pid, "stat"); e == nil && !strings.HasPrefix(stat, "Z") {
						unreadable = append(unreadable, pid)
					}
				}
			}
		}
	}
	return parents, unreadable, nil
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
	parents, unreadable, err := ownProcessTable(ctx)
	if err != nil {
		return "failed", "process table unavailable"
	}
	if len(unreadable) > 0 {
		return "failed", "cannot read arguments of own-user processes " + pidList(unreadable) + "; retry"
	}
	ancestors := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !ancestors[pid]; pid = parents[pid] {
		ancestors[pid] = true
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
	deadline := time.Now().Add(wait)
	for _, h := range helpers {
		// The caller and its ancestors (e.g. the launcher supervisor blocked on
		// this very call, or a Worker shell) can never leave before we return.
		if h.pid == client.pidOrZero() || ancestors[h.pid] {
			continue
		}
		if !waitGone(ctx, time.Until(deadline), h) {
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
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
