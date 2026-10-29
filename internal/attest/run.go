package attest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/ownedexec"
)

type RunOpts struct {
	ItemID       string
	Kind         Kind
	Command      string
	Argv         []string
	AgentID      string
	AttDir       string
	RepoRoot     string
	WorkDir      string // Optional absolute execution directory; RepoRoot still owns evidence.
	OperationKey string // Assigned by the owned-operation wrapper, not copied into evidence.
}

// Execution separates process custody from evidence persistence. A failed
// evidence write cannot erase an observed exit or invent a live process.
type Execution struct {
	Started  bool
	Joined   bool
	ExitCode int
}

func (l *Ledger) Run(ctx context.Context, opts RunOpts) (Record, error) {
	rec, _, err := l.RunObserved(ctx, opts)
	return rec, err
}

func (l *Ledger) RunObserved(ctx context.Context, opts RunOpts) (Record, Execution, error) {
	observed := Execution{ExitCode: -1}
	rec, err := l.run(ctx, opts, &observed)
	return rec, observed, err
}

func (l *Ledger) run(ctx context.Context, opts RunOpts, observed *Execution) (Record, error) {
	if opts.ItemID == "" || opts.AgentID == "" || opts.AttDir == "" {
		return Record{}, fmt.Errorf("attest.Run: ItemID, AgentID, AttDir required")
	}
	if !opts.Kind.Valid() {
		return Record{}, fmt.Errorf("attest.Run: invalid kind %q", opts.Kind)
	}

	if (opts.Command == "") == (len(opts.Argv) == 0) {
		return Record{}, fmt.Errorf("provide exactly one of command or argv")
	}
	if len(opts.Argv) > 0 && strings.TrimSpace(opts.Argv[0]) == "" {
		return Record{}, fmt.Errorf("argv executable must not be empty")
	}

	workDir := opts.RepoRoot
	recordedCommand := opts.Command
	if len(opts.Argv) > 0 {
		raw, _ := json.Marshal(opts.Argv)
		recordedCommand = "argv: " + string(raw)
	}
	if opts.WorkDir != "" {
		if !filepath.IsAbs(opts.WorkDir) {
			return Record{}, fmt.Errorf("attest.Run: work_dir must be absolute")
		}
		resolved, err := filepath.EvalSymlinks(opts.WorkDir)
		if err != nil {
			return Record{}, fmt.Errorf("attest.Run: resolve work_dir: %w", err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return Record{}, fmt.Errorf("attest.Run: work_dir must be an existing directory")
		}
		workDir = resolved
		if len(opts.Argv) > 0 {
			recordedCommand = "cwd: " + shellQuote(workDir) + "; " + recordedCommand
		} else {
			recordedCommand = "cd " + shellQuote(workDir) + " && sh -c " + shellQuote(opts.Command)
		}
	}
	var buf bytes.Buffer
	if opts.WorkDir != "" {
		fmt.Fprintf(&buf, "squad execution directory: %s\n", workDir)
	}

	var cmd *exec.Cmd
	if len(opts.Argv) > 0 {
		cmd = exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", opts.Command)
	}
	ownedexec.Configure(cmd)
	cmd.Dir = workDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return Record{}, fmt.Errorf("attest.Run exec: %w (output: %s)", err, buf.String())
	}
	observed.Started = true
	runErr := cmd.Wait()
	if cmd.ProcessState != nil {
		// WaitDelay can leave an owned descendant holding the pipes after
		// the direct child exits. Retain unresolved custody in that case.
		observed.Joined = !errors.Is(runErr, exec.ErrWaitDelay)
		observed.ExitCode = cmd.ProcessState.ExitCode()
	}
	exitCode := observed.ExitCode
	if err := runErr; err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return Record{}, fmt.Errorf("attest.Run exec: %w (output: %s)", err, buf.String())
		}
	}

	if exitCode != 0 || opts.OperationKey != "" {
		// A quiet failed command must not deduplicate against a prior quiet
		// success (attestations are keyed by output hash per item/kind).
		fmt.Fprintf(&buf, "\nsquad execution exit code: %d\n", exitCode)
		if opts.OperationKey != "" {
			fmt.Fprintf(&buf, "squad owned operation: %s\n", l.Hash([]byte(opts.OperationKey)))
		}
	}
	hash := l.Hash(buf.Bytes())
	if err := os.MkdirAll(opts.AttDir, 0o755); err != nil {
		return Record{}, fmt.Errorf("mkdir attestations dir: %w", err)
	}
	out := filepath.Join(opts.AttDir, hash+".txt")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return Record{}, fmt.Errorf("write attestation file: %w", err)
	}

	rec := Record{
		ItemID:     opts.ItemID,
		Kind:       opts.Kind,
		Command:    recordedCommand,
		ExitCode:   exitCode,
		OutputHash: hash,
		OutputPath: out,
		AgentID:    opts.AgentID,
	}
	// The process has joined. Its cancellation must not cancel persistence of
	// the observed failure, otherwise a resumed owner sees no terminal result.
	// Bound this final local write independently of the execution lifetime.
	resultCtx, cancelResult := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelResult()
	id, err := l.Insert(resultCtx, rec)
	if err != nil {
		return Record{}, err
	}
	rec.ID = id
	rec.RepoID = l.repoID
	return rec, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
