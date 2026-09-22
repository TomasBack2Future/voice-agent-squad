package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/listener"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/terminalevents"
)

func newTerminalEventsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "terminal-events", Short: "Durable recipient acknowledgements and safe hook delivery"}
	var session string
	var max time.Duration
	listen := &cobra.Command{Use: "listen", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if session == "" || max <= 0 || max > 24*time.Hour {
			return fmt.Errorf("delivery-session required and max must be in (0,24h]")
		}
		return listenTerminalEvents(cmd.Context(), terminalevents.Store{DB: bc.db, Repo: bc.repoID, Recipient: bc.agentID}, session, max, cmd.OutOrStdout())
	}}
	listen.Flags().StringVar(&session, "delivery-session", "", "Receiver incarnation, unique for each client start/resume")
	listen.Flags().DurationVar(&max, "max", 23*time.Hour, "Maximum receiver lifetime")
	var note string
	ack := &cobra.Command{Use: "ack <event-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		return (terminalevents.Store{DB: bc.db, Repo: bc.repoID, Recipient: bc.agentID}).Ack(cmd.Context(), args[0], note)
	}}
	ack.Flags().StringVar(&note, "note", "", "Durable reconciliation result/reference")
	cmd.AddCommand(listen, ack)
	return cmd
}

// The only output is a bounded identity/pointer list, never sender prose. The
// hook converts a nonempty receipt to asyncRewake exit 2. A timed-out receiver
// emits a renewal reminder rather than silently disabling future wakeups.
func listenTerminalEvents(ctx context.Context, s terminalevents.Store, session string, max time.Duration, out io.Writer) error {
	l, err := listener.New("127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	reg := notify.NewRegistry(s.DB)
	instance := "terminal:" + s.Recipient + ":" + session
	if err = reg.Register(ctx, notify.Endpoint{Instance: instance, RepoID: s.Repo, Kind: notify.KindRewake, Port: l.Port()}); err != nil {
		return err
	}
	defer func() { _ = reg.Unregister(context.Background(), instance, notify.KindRewake) }()
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	for {
		if err = s.Discover(ctx); err != nil {
			return err
		}
		events, err := s.Pending(ctx, session, 2*time.Minute)
		if err != nil {
			return err
		}
		if len(events) > 0 {
			if err = json.NewEncoder(out).Encode(map[string]any{"type": "worker-terminal-delivery-v1", "events": events}); err != nil {
				return err
			}
			for _, e := range events {
				if err = s.Delivered(ctx, e.ID, session); err != nil {
					return err
				}
			}
			return nil
		}
		if _, err = l.WaitWake(ctx, 15*time.Second); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}
