package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/listener"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/terminalevents"
)

func newTerminalEventsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "terminal-events", Short: "Durable recipient acknowledgements and safe hook delivery"}
	var session, nativeSession string
	var max time.Duration
	var deferDelivery bool
	listen := &cobra.Command{Use: "listen", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if session == "" || max <= 0 || max > 24*time.Hour {
			return fmt.Errorf("delivery-session required and max must be in (0,24h]")
		}
		return receiveTerminalEvents(cmd.Context(), terminalevents.Store{DB: bc.db, Repo: bc.repoID, Recipient: bc.agentID, NativeSession: nativeSession}, session, max, cmd.OutOrStdout(), deferDelivery)
	}}
	listen.Flags().StringVar(&nativeSession, "native-session", "", "Exact bound controller native, distinct from receiver incarnation")
	listen.Flags().StringVar(&session, "delivery-session", "", "Receiver incarnation, unique for each client start/resume")
	listen.Flags().DurationVar(&max, "max", 23*time.Hour, "Maximum receiver lifetime")
	listen.Flags().BoolVar(&deferDelivery, "defer-delivery", false, "Leave events pending until structured transport confirms acceptance")
	poll := &cobra.Command{Use: "poll", Short: "Read a bounded pending event batch without marking it delivered", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		events, err := pollTerminalEvents(cmd.Context(), terminalevents.Store{DB: bc.db, Repo: bc.repoID, Recipient: bc.agentID, NativeSession: nativeSession}, session)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(events)
	}}
	poll.Flags().StringVar(&nativeSession, "native-session", "", "Exact bound native session")
	poll.Flags().StringVar(&session, "delivery-session", "", "Current receiver incarnation")
	var deliveredSession string
	delivered := &cobra.Command{Use: "delivered <event-id>", Short: "Record native transport acceptance without acknowledging handling", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		return (terminalevents.Store{DB: bc.db, Repo: bc.repoID, Recipient: bc.agentID, NativeSession: nativeSession}).Delivered(cmd.Context(), args[0], deliveredSession)
	}}
	delivered.Flags().StringVar(&nativeSession, "native-session", "", "Exact bound controller native")
	delivered.Flags().StringVar(&deliveredSession, "delivery-session", "", "Receiver incarnation that confirmed transport acceptance")
	var note string
	ack := &cobra.Command{Use: "ack <event-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		return (terminalevents.Store{DB: bc.db, Repo: bc.repoID, Recipient: bc.agentID, NativeSession: nativeSession}).Ack(cmd.Context(), args[0], note)
	}}
	ack.Flags().StringVar(&nativeSession, "native-session", "", "Exact bound controller native")
	ack.Flags().StringVar(&note, "note", "", "Durable reconciliation result/reference")
	var request terminalevents.PublishRequest
	publish := &cobra.Command{Use: "publish", Short: "Persist one validated event for its ledger-derived recipient", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		id, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).Publish(cmd.Context(), bc.agentID, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"event_id": id, "state": "pending"})
	}}
	publish.Flags().StringVar(&request.Reservation, "reservation", "", "Exact dispatch reservation key")
	publish.Flags().Int64Var(&request.Generation, "generation", 0, "Reservation generation")
	publish.Flags().StringVar(&request.WorkerSession, "worker-session", "", "Bound native Worker session")
	publish.Flags().StringVar(&request.Kind, "kind", "", "issue-closed, handoff-complete, blocked, decision-request or decision-resolved")
	publish.Flags().Int64Var(&request.OutcomeID, "outcome", 0, "Durable Squad outcome/decision message id")
	publish.Flags().Int64Var(&request.ExpectedDecision, "expected-decision", 0, "Current adopted decision revision for Worker outcomes")
	var submit terminalevents.SubmitRequest
	var bodyFile string
	submitCmd := &cobra.Command{Use: "submit", Short: "Atomically store one Worker outcome message and its durable terminal event", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if bodyFile != "" {
			raw, err := os.ReadFile(bodyFile)
			if err != nil {
				return err
			}
			submit.Body = string(raw)
		}
		out, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).Submit(cmd.Context(), bc.agentID, submit)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"message_id": out.MessageID, "event_id": out.EventID, "state": "pending"})
	}}
	submitCmd.Flags().StringVar(&submit.Reservation, "reservation", "", "Exact dispatch reservation key")
	submitCmd.Flags().Int64Var(&submit.Generation, "generation", 0, "Reservation generation")
	submitCmd.Flags().StringVar(&submit.WorkerSession, "worker-session", "", "Bound native Worker session")
	submitCmd.Flags().StringVar(&submit.Kind, "kind", "", "issue-closed, handoff-complete, blocked or decision-request")
	submitCmd.Flags().StringVar(&submit.Body, "body", "", "Outcome body text (or --body-file)")
	submitCmd.Flags().StringVar(&bodyFile, "body-file", "", "Read outcome body from file")
	submitCmd.Flags().StringVar(&submit.RequestKey, "request-key", "", "Stable request identity: retries reuse it, distinct requests use distinct keys (#84 episodes map one episode to one key)")
	submitCmd.Flags().Int64Var(&submit.ExpectedDecision, "expected-decision", 0, "Current adopted decision revision for Worker outcomes")
	_ = submitCmd.MarkFlagRequired("reservation")
	_ = submitCmd.MarkFlagRequired("generation")
	_ = submitCmd.MarkFlagRequired("worker-session")
	_ = submitCmd.MarkFlagRequired("kind")
	cmd.AddCommand(terminalDecisionCommands()...)
	cmd.AddCommand(listen, poll, delivered, ack, publish, submitCmd)
	return cmd
}

// The only output is a bounded identity/pointer list, never sender prose. The
// hook converts a nonempty receipt to asyncRewake exit 2. A timed-out receiver
// emits a renewal reminder rather than silently disabling future wakeups.
func listenTerminalEvents(ctx context.Context, s terminalevents.Store, session string, max time.Duration, out io.Writer) error {
	return receiveTerminalEvents(ctx, s, session, max, out, false)
}

func receiveTerminalEvents(ctx context.Context, s terminalevents.Store, session string, max time.Duration, out io.Writer, deferDelivery bool) error {
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
			if err = json.NewEncoder(out).Encode(map[string]any{"type": "worker-terminal-delivery-v1", "recipient": s.Recipient, "delivery_session": session, "events": events}); err != nil {
				return err
			}
			if !deferDelivery {
				for _, e := range events {
					if err = s.Delivered(ctx, e.ID, session); err != nil {
						return err
					}
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

// Poll exposes the existing bounded/fenced receiver read without holding a
// service execution slot for a long-lived listener. Transport and handling
// acknowledgements remain separate explicit operations.
func pollTerminalEvents(ctx context.Context, s terminalevents.Store, session string) ([]terminalevents.Event, error) {
	if session == "" {
		return nil, fmt.Errorf("delivery-session required")
	}
	if err := s.Discover(ctx); err != nil {
		return nil, err
	}
	return s.Pending(ctx, session, 2*time.Minute)
}
