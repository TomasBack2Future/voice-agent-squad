package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/execution"
)

func readExecutionJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > 16384 {
		return errors.New("execution input too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func newExecutionCmd() *cobra.Command {
	root := &cobra.Command{Use: "execution", Short: "Authorize, serve and reconcile fenced production execution"}
	root.AddCommand(&cobra.Command{Use: "authorize binding.json", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var b execution.Binding
		if err := readExecutionJSON(args[0], &b); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if err := execution.New(bc.db, bc.repoID, nil).Authorize(cmd.Context(), b, bc.agentID); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"id": b.ID, "state": "authorized"})
	}})
	root.AddCommand(&cobra.Command{Use: "show id", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		b, err := execution.New(bc.db, bc.repoID, nil).Snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
	}})
	var runID, attempt int64
	var evidence string
	var stopped bool
	reconcile := &cobra.Command{Use: "reconcile id", Args: cobra.ExactArgs(1), Short: "Verify terminal GitHub identity and record external-stop/recovery evidence before unpinning", RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		s := execution.New(bc.db, bc.repoID, nil)
		b, err := s.Binding(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := execution.VerifyGitHubRun(cmd.Context(), b, runID, attempt, true); err != nil {
			return err
		}
		return s.Reconcile(cmd.Context(), args[0], bc.agentID, evidence, runID, attempt, stopped)
	}}
	reconcile.Flags().Int64Var(&runID, "run-id", 0, "Exact GitHub run id")
	reconcile.Flags().Int64Var(&attempt, "run-attempt", 1, "Exact attempt (reruns are forbidden)")
	reconcile.Flags().StringVar(&evidence, "evidence", "", "Verified external termination and environment outcome evidence")
	reconcile.Flags().BoolVar(&stopped, "confirm-external-stopped", false, "Cluster writes and child processes have stopped; safe outcome verified")
	root.AddCommand(reconcile)
	var listen, tokenFile, cert, key string
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, Short: "Serve narrow authenticated admission from this repository's authoritative ledger", RunE: func(cmd *cobra.Command, args []string) error {
		host, _, err := net.SplitHostPort(listen)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		loopback := ip != nil && ip.IsLoopback()
		if (cert == "") != (key == "") || (!loopback && cert == "") {
			return errors.New("non-loopback admission requires TLS cert and key")
		}
		info, err := os.Stat(tokenFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("token file must be owner-only")
		}
		raw, err := os.ReadFile(tokenFile)
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(raw))
		if len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
			return errors.New("token must contain 32-512 non-whitespace bytes")
		}
		// A missing gh or credentials is a start-up defect, not something to
		// discover at /begin; a GitHub outage is reported but does not stop us.
		if err := execution.CheckStartup(cmd.Context()); err != nil {
			if execution.StartupFatal(err) {
				return fmt.Errorf("execution_startup_failed: %s (gh must be on PATH with credentials reachable via HOME or GH_CONFIG_DIR; see `squad execution launchd-plist`)", err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "execution admission start-up warning: %s\n", err)
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		// Bind before announcing anything: a taken port is a failed start, never a
		// "listening" line, and the occupant is identified, not stopped.
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			if !errors.Is(err, syscall.EADDRINUSE) {
				return err
			}
			if cert != "" {
				return fmt.Errorf("listener_conflict: %s is already in use by a listener that cannot be identified over TLS; it was not touched", listen)
			}
			switch execution.ProbeListener(cmd.Context(), listen, token, bc.repoID) {
			case execution.ListenerSameService:
				return fmt.Errorf("listener_conflict: %s is already served by this ledger's admission service; no second instance was started", listen)
			default:
				return fmt.Errorf("listener_conflict: %s is in use by a different listener that is not this ledger's admission service; it was not touched", listen)
			}
		}
		store := execution.New(bc.db, bc.repoID, nil)
		handler := execution.HandlerWithDiagnostics(store, token, func(r *http.Request, q execution.Request) error {
			return execution.VerifyGitHubRun(r.Context(), q.Binding, q.RunID, q.RunAttempt, false)
		}, execution.VerifyGitHubLookup)
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-cmd.Context().Done():
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
			case <-done:
			}
		}()
		fmt.Fprintf(cmd.ErrOrStderr(), "execution admission listening on %s; repo %s\n", listener.Addr(), bc.repoID)
		if cert != "" {
			err = server.ServeTLS(listener, cert, key)
		} else {
			err = server.Serve(listener)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}}
	serve.Flags().StringVar(&listen, "listen", "127.0.0.1:7788", "Listen address (TLS required outside loopback)")
	serve.Flags().StringVar(&tokenFile, "token-file", "", "Owner-only shared runner token file")
	serve.Flags().StringVar(&cert, "tls-cert", "", "TLS certificate file")
	serve.Flags().StringVar(&key, "tls-key", "", "TLS private key file")
	root.AddCommand(serve)
	root.AddCommand(newExecutionLaunchdCmd())
	return root
}
