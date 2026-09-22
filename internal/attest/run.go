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
)

type RunOpts struct {
	ItemID   string
	Kind     Kind
	Command  string
	Argv     []string
	AgentID  string
	AttDir   string
	RepoRoot string
	WorkDir  string // Optional absolute execution directory; RepoRoot still owns evidence.
}

func (l *Ledger) Run(ctx context.Context, opts RunOpts) (Record, error) {
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
	cmd.Dir = workDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	exitCode := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return Record{}, fmt.Errorf("attest.Run exec: %w (output: %s)", err, buf.String())
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
	id, err := l.Insert(ctx, rec)
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
