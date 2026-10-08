package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := b.limit - b.Len()
	if left < len(p) {
		b.overflow = true
		p = p[:left]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// ProcessRunner reuses the existing CLI/MCP implementation in an isolated
// process. It never runs a shell or accepts a client-supplied cwd/environment.
// Only this host accesses SQLite and the selected .squad files.
func ProcessRunner(binary string, c Config) Runner {
	return func(ctx context.Context, client Client, args []string, stdin []byte) (Result, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = c.Workspace
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = 5 * time.Second
		cmd.Env = []string{
			"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + c.Home, "SQUAD_HOME=" + c.Home,
			"SQUAD_AGENT=" + client.Agent, "SQUAD_SESSION_ID=remote:" + client.Session, "SQUAD_NATIVE_SESSION_ID=" + client.Session,
			"SQUAD_NO_HYGIENE=1", "SQUAD_NO_AUTO_DAEMON=1", "SQUAD_NO_BROWSER=1", "SQUAD_SERVICE_CHILD=1",
			"SQUAD_MCP_TOOLS=" + strings.Join(Tools(client.Role), ","),
		}
		cmd.Stdin = bytes.NewReader(stdin)
		out, errout := &limitedBuffer{limit: MaxOutput}, &limitedBuffer{limit: MaxOutput}
		cmd.Stdout, cmd.Stderr = out, errout
		err := cmd.Run()
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if out.overflow || errout.overflow {
			return Result{}, fmt.Errorf("output limit exceeded")
		}
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return Result{}, err
			}
			code = exit.ExitCode()
			if code < 0 {
				return Result{}, err
			}
		}
		return Result{Stdout: out.String(), Stderr: errout.String(), ExitCode: code}, nil
	}
}
