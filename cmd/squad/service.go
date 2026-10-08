package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/remote"
)

func newServiceCmd() *cobra.Command {
	var configPath, listen, cert, key string
	cmd := &cobra.Command{Use: "service", Short: "Serve authenticated remote coordination and MCP", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		raw, e := os.ReadFile(configPath)
		if e != nil {
			return e
		}
		var cfg remote.Config
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&cfg); e != nil {
			return e
		}
		if e = cfg.Validate(); e != nil {
			return e
		}
		if _, e = os.Stat(cfg.Workspace + "/.squad/config.yaml"); e != nil {
			return fmt.Errorf("initialize the service ledger locally before starting: %w", e)
		}
		host, _, e := net.SplitHostPort(listen)
		if e != nil {
			return e
		}
		loopback := host == "localhost"
		if ip := net.ParseIP(host); ip != nil {
			loopback = ip.IsLoopback()
		}
		if !loopback && (cert == "" || key == "") {
			return fmt.Errorf("non-loopback service listeners require --tls-cert and --tls-key")
		}
		binary, e := os.Executable()
		if e != nil {
			return e
		}
		handler, e := remote.NewServer(cfg, versionString, remote.ProcessRunner(binary, cfg))
		if e != nil {
			return e
		}
		srv := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 135 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		served := make(chan error, 1)
		go func() {
			if cert != "" || key != "" {
				served <- srv.ListenAndServeTLS(cert, key)
			} else {
				served <- srv.ListenAndServe()
			}
		}()
		select {
		case e = <-served:
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 125*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				_ = srv.Close()
				return err
			}
			e = <-served
		}
		if e == http.ErrServerClosed {
			return nil
		}
		return e
	}}
	cmd.Flags().StringVar(&configPath, "config", "", "Service configuration file (workspace, state paths, hashed client tokens)")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:7788", "Listen address")
	cmd.Flags().StringVar(&cert, "tls-cert", "", "TLS certificate")
	cmd.Flags().StringVar(&key, "tls-key", "", "TLS private key")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func executeRemote(args []string) (int, error) {
	c, e := remote.NewClient(os.Getenv("SQUAD_REMOTE_URL"), os.Getenv("SQUAD_REMOTE_TOKEN"))
	if e != nil {
		return 2, e
	}
	if len(args) == 1 && args[0] == "mcp" {
		return 0, c.MCP(context.Background(), os.Stdin, os.Stdout)
	}
	r, e := c.Command(context.Background(), args, os.Getenv("SQUAD_REQUEST_ID"))
	if e != nil {
		return 2, e
	}
	fmt.Fprint(os.Stdout, r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	return r.ExitCode, nil
}
