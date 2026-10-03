package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/zsiec/squad/internal/dispatch"
)

type workerJoinOperation struct {
	State     string `json:"state"`
	Kind      string `json:"kind"`
	Container string `json:"container"`
	PID       int    `json:"pid"`
}

type workerJoinReceipt struct {
	Binding    dispatch.WorkerExecutionBinding `json:"binding"`
	NativePID  int                             `json:"native_pid"`
	NativeExit *int                            `json:"native_exit"`
	BridgeLive bool                            `json:"bridge_live"`
	BridgePID  *int                            `json:"bridge_pid"`
	Operations []workerJoinOperation           `json:"operations"`
	Joined     bool                            `json:"joined"`
}

func workerProcessAbsent(pid int) error {
	if pid < 1 {
		return errors.New("owned process identity unavailable")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return errors.New("owned process termination unavailable")
	}
	err = p.Signal(syscall.Signal(0))
	if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		return errors.New("owned process remains live or termination unavailable")
	}
	return nil
}

func verifyWorkerJoin(ctx context.Context, b dispatch.WorkerExecutionBinding, bindingPath, receiptPath string) error {
	if !filepath.IsAbs(receiptPath) || filepath.Dir(receiptPath) != filepath.Dir(bindingPath) {
		return errors.New("join receipt must accompany immutable binding")
	}
	info, err := os.Lstat(receiptPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1048576 {
		return errors.New("private bounded join receipt required")
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		return err
	}
	var r workerJoinReceipt
	if err = json.Unmarshal(raw, &r); err != nil {
		return errors.New("join receipt malformed")
	}
	if !reflect.DeepEqual(r.Binding, b) || !r.Joined || r.NativePID != b.ClientPID || r.NativeExit == nil || r.BridgeLive {
		return errors.New("join evidence does not match exact Worker custody")
	}
	if err = workerProcessAbsent(b.ClientPID); err != nil {
		return err
	}
	if r.BridgePID != nil {
		if err = workerProcessAbsent(*r.BridgePID); err != nil {
			return err
		}
	}
	journalPath := filepath.Join(filepath.Dir(bindingPath), "operations.json")
	if info, err := os.Lstat(journalPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 1048576 || info.Mode().Perm()&0077 != 0 {
			return errors.New("owned tool journal provenance unavailable")
		}
		journal, err := os.ReadFile(journalPath)
		if err != nil {
			return err
		}
		var operations []workerJoinOperation
		if json.Unmarshal(journal, &operations) != nil || !reflect.DeepEqual(operations, r.Operations) {
			return errors.New("join receipt omits or changes original tool journal")
		}
	} else if !errors.Is(err, os.ErrNotExist) || len(r.Operations) != 0 {
		return errors.New("original tool journal unavailable")
	}
	bridgePath := filepath.Join(filepath.Dir(bindingPath), "bridge.json")
	if info, err := os.Lstat(bridgePath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 8192 || info.Mode().Perm()&0077 != 0 {
			return errors.New("original bridge provenance unavailable")
		}
		raw, err := os.ReadFile(bridgePath)
		if err != nil {
			return err
		}
		var bridge struct {
			PID    int    `json:"pid"`
			Native string `json:"native"`
		}
		if json.Unmarshal(raw, &bridge) != nil || bridge.Native != b.Native || r.BridgePID == nil || bridge.PID != *r.BridgePID {
			return errors.New("join receipt changed original bridge identity")
		}
	} else if !errors.Is(err, os.ErrNotExist) || r.BridgePID != nil {
		return errors.New("original bridge journal unavailable")
	}
	runtime := b.ToolRuntime
	docker := func(args ...string) ([]byte, error) {
		bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		argv := append([]string{"--context", runtime.Context}, args...)
		out, e := exec.CommandContext(bounded, runtime.Executable, argv...).Output()
		if e != nil {
			return nil, errors.New("container custody verification unavailable")
		}
		return out, nil
	}
	out, err := docker("info", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if string(out) != runtime.DaemonID+"\n" {
		return errors.New("container daemon identity changed")
	}
	for _, op := range r.Operations {
		if op.State != "completed" && op.State != "joined" {
			return errors.New("unresolved owned tool operation")
		}
		if op.PID > 0 {
			if err = workerProcessAbsent(op.PID); err != nil {
				return err
			}
		}
		if op.Kind == "write" {
			continue
		}
		if op.Kind != "container" || len(op.Container) != 64 {
			return errors.New("owned container provenance unavailable")
		}
		out, err = docker("container", "inspect", op.Container)
		if err != nil {
			return err
		}
		var containers []struct {
			ID    string `json:"Id"`
			State struct {
				Running bool
				Pid     int
				Status  string
			}
			Config struct{ Labels map[string]string }
			Image  string
		}
		if json.Unmarshal(out, &containers) != nil || len(containers) != 1 {
			return errors.New("container join receipt unavailable")
		}
		c := containers[0]
		if c.ID != op.Container || c.Image != runtime.Image || c.Config.Labels["squad.execution"] != b.ID || c.State.Running || c.State.Pid != 0 || (c.State.Status != "exited" && c.State.Status != "created") {
			return fmt.Errorf("owned container has not joined")
		}
	}
	return nil
}
