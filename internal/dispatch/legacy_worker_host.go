package dispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/grokreview"
)

func legacyPS(ctx context.Context, pid int, field string) (string, error) {
	command := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", field+"=")
	raw, err := command.Output()
	if err != nil {
		return "", errors.New("native host process identity unavailable")
	}
	return strings.TrimSpace(string(raw)), nil
}

func observeLegacyOwner(ctx context.Context, native string, leasePID int) ([]LegacyProcess, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var client int
	ancestors := map[int]bool{}
	for pid, n := os.Getpid(), 0; pid > 1 && n < 32; n++ {
		ancestors[pid] = true
		parent, err := legacyPS(bounded, pid, "ppid")
		if err != nil {
			return nil, err
		}
		parentPID, err := strconv.Atoi(parent)
		if err != nil {
			return nil, err
		}
		fields, err := legacyProcessArguments(pid)
		if err != nil {
			return nil, err
		}
		executable, identityErr := legacyPS(bounded, pid, "comm")
		if identityErr != nil {
			return nil, identityErr
		}
		if len(fields) > 0 && filepath.Base(fields[0]) == "claude" && filepath.Base(executable) == "claude" {
			for i := 1; i+1 < len(fields); i++ {
				if fields[i] == "--" {
					break
				}
				if fields[i] == "--session-id" || fields[i] == "--resume" {
					if fields[i+1] == native {
						client = pid
					}
					break
				}
			}
			if client != 0 {
				if leasePID < 2 || leasePID != parentPID {
					return nil, errors.New("exact original native parent/renewal supervisor required")
				}
				break
			}
		}
		pid = parentPID
	}
	if client == 0 {
		return nil, errors.New("qualified original Claude native Bash ancestor unavailable; App/terminal receipt import is unsupported")
	}
	// Parent IDs carry no prompts or credentials. Read argv only for the narrow
	// client/descendant qualification, and persist selected process metadata.
	raw, err := exec.CommandContext(bounded, "/bin/ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	parents := map[int]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			pid, e := strconv.Atoi(f[0])
			ppid, e2 := strconv.Atoi(f[1])
			if e == nil && e2 == nil {
				parents[pid] = ppid
			}
		}
	}
	owned := map[int]bool{client: true, leasePID: true}
	for changed := true; changed; {
		changed = false
		for pid, parent := range parents {
			if owned[parent] && !owned[pid] {
				owned[pid] = true
				changed = true
			}
		}
	}
	if len(owned) > 128 {
		return nil, errors.New("legacy native process tree exceeds the qualified bound")
	}
	var out []LegacyProcess
	for pid := range owned {
		start, err := legacyPS(bounded, pid, "lstart")
		if err != nil {
			return nil, err
		}
		executable, err := legacyPS(bounded, pid, "comm")
		if err != nil {
			return nil, err
		}
		if pid != leasePID && !ancestors[pid] {
			argv, err := legacyProcessArguments(pid)
			if err != nil {
				return nil, err
			}
			ownedReceiver := false
			for index, arg := range argv {
				if filepath.Base(arg) == "terminal_receiver.py" || (arg == "terminal-events" && index+1 < len(argv) && argv[index+1] == "listen") {
					ownedReceiver = true
				}
			}
			if !ownedReceiver {
				return nil, errors.New("another original native child/tool is still running; join it before stop preparation")
			}
		}
		out = append(out, LegacyProcess{PID: pid, Start: start, Executable: executable})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func legacyWorkspaceDigest(ctx context.Context, workspace string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(bounded, "git", args...)
		cmd.Dir = workspace
		return cmd.Output()
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(string(root)))
	if err != nil {
		return "", err
	}
	selected, err := filepath.EvalSymlinks(workspace)
	if err != nil || selected != actual {
		return "", errors.New("exact retained worktree root required")
	}
	hash := sha256.New()
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"branch", "--show-current"}, {"remote", "get-url", "origin"}, {"status", "--porcelain=v1", "-z"}} {
		value, err := git(args...)
		if err != nil {
			return "", err
		}
		hash.Write(value)
		hash.Write([]byte{0})
	}
	files, err := git("ls-files", "-cz", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	names := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
	sort.Strings(names)
	var total int64
	for _, name := range names {
		if name == "" {
			continue
		}
		target := filepath.Join(selected, name)
		relative, err := filepath.Rel(selected, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return "", errors.New("worktree path escaped retained source")
		}
		info, err := os.Lstat(target)
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		if errors.Is(err, os.ErrNotExist) {
			hash.Write([]byte("deleted\x00"))
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("legacy snapshot requires regular source files; qualify symlink/submodule custody separately")
		}
		total += info.Size()
		if total > 256<<20 {
			return "", errors.New("legacy source snapshot exceeds 256MiB bound")
		}
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil || resolved != target {
			return "", errors.New("retained source traverses an unqualified symlink")
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return "", err
		}
		hash.Write(data)
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func verifyLegacyExternal(ctx context.Context, inventory []LegacyExternalOperation, actor, native string) error {
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	for _, operation := range inventory {
		if operation.Identity == "" {
			return errors.New("exact external-operation identity required")
		}
		switch operation.Kind {
		case "managed-grok-review":
			if !filepath.IsAbs(operation.ReceiptPath) {
				return errors.New("private managed review admission database required")
			}
			info, err := os.Lstat(operation.ReceiptPath)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return errors.New("managed review admission provenance unavailable")
			}
			db, err := sql.Open("sqlite", "file:"+url.PathEscape(operation.ReceiptPath)+"?mode=ro&_pragma=query_only(1)")
			if err != nil {
				return err
			}
			var raw string
			err = db.QueryRowContext(ctx, `SELECT receipt FROM review_attempts WHERE id=?`, operation.Identity).Scan(&raw)
			db.Close()
			if err != nil {
				return errors.New("original managed review attempt unavailable")
			}
			var receipt grokreview.AttemptReceipt
			if len(raw) > 65536 || json.Unmarshal([]byte(raw), &receipt) != nil || receipt.ID != operation.Identity || receipt.OwnerActor != actor || receipt.OwnerNative != native || !receipt.Joined || receipt.CompletedAt < 1 || receipt.WrapperPID < 1 || receipt.ReviewerPID < 1 || receipt.Identity.Repository != operation.Repository || receipt.Identity.HeadSHA != operation.HeadSHA {
				return errors.New("review original native, exact revision or durable join is unqualified")
			}
			for _, pid := range []int{receipt.WrapperPID, receipt.ReviewerPID} {
				if err = absentWorkerProcess(pid); err != nil {
					return err
				}
			}
		case "readonly-github-run":
			if !validLegacyHash(operation.HeadSHA) && len(operation.HeadSHA) != 40 {
				return errors.New("exact CI source revision required")
			}
			id, err := strconv.ParseInt(operation.Identity, 10, 64)
			if err != nil || id <= 0 || operation.Attempt < 1 || len(strings.Split(operation.Repository, "/")) != 2 || strings.ContainsAny(operation.Repository, " .\\\n\r") {
				return errors.New("exact repository/run/attempt required")
			}
			bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
			raw, err := exec.CommandContext(bounded, "gh", "api", "repos/"+operation.Repository+"/actions/runs/"+operation.Identity).Output()
			cancel()
			if err != nil || len(raw) > 131072 {
				return errors.New("actual GitHub CI observation unavailable")
			}
			var run struct {
				ID      int64  `json:"id"`
				Attempt int    `json:"run_attempt"`
				Head    string `json:"head_sha"`
				Status  string `json:"status"`
			}
			if json.Unmarshal(raw, &run) != nil || run.ID != id || run.Attempt != operation.Attempt || run.Head != operation.HeadSHA || run.Status != "completed" {
				return errors.New("ci run/attempt/revision remains live or changed; retain its original custody")
			}
		default:
			return errors.New("external mutation or executor remains unqualified: " + operation.Kind)
		}
	}
	return nil
}
