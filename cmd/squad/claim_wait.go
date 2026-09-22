package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/listener"
	"github.com/zsiec/squad/internal/notify"
)

type ClaimWaitArgs struct {
	Claim      ClaimArgs
	Registry   *notify.Registry
	Instance   string
	Fallback   time.Duration
	Timeout    time.Duration
	BindAddr   string
	WaitWriter io.Writer
}

type ClaimWaitTimeoutError struct {
	ItemID string
	After  time.Duration
}

func (e *ClaimWaitTimeoutError) Error() string {
	return fmt.Sprintf("timed out waiting %s for claim on %s", e.After, e.ItemID)
}

func ClaimWithWait(ctx context.Context, args ClaimWaitArgs) (*ClaimResult, error) {
	if args.Timeout <= 0 {
		return nil, fmt.Errorf("claim --wait: timeout must be greater than zero")
	}
	if args.Fallback < 0 {
		return nil, fmt.Errorf("claim --wait: fallback must not be negative")
	}
	res, err := Claim(ctx, args.Claim)
	if err == nil || !isClaimHeld(err) {
		return res, err
	}
	if args.Registry == nil {
		args.Registry = notify.NewRegistry(args.Claim.DB)
	}
	bind := args.BindAddr
	if bind == "" {
		bind = "127.0.0.1:0"
	}
	l, err := listener.New(bind)
	if err != nil {
		return nil, fmt.Errorf("claim --wait: bind: %w", err)
	}
	defer func() { _ = l.Close() }()

	instance := resolveInstance(args.Instance)
	kind := notify.ClaimWaitKind(args.Claim.ItemID)
	if err := args.Registry.Register(ctx, notify.Endpoint{
		Instance: instance,
		RepoID:   args.Claim.RepoID,
		Kind:     kind,
		Port:     l.Port(),
	}); err != nil {
		return nil, fmt.Errorf("claim --wait: register: %w", err)
	}
	defer func() {
		_ = args.Registry.Unregister(context.Background(), instance, kind)
	}()

	var held *ClaimHeldError
	_ = errors.As(err, &held)
	if args.WaitWriter != nil {
		holder := "another agent"
		if held != nil && held.Holder != "" {
			holder = held.Holder
		}
		fmt.Fprintf(args.WaitWriter,
			"waiting for %s (held by %s; event-driven, fallback %s, timeout %s)\n",
			args.Claim.ItemID, holder, args.Fallback, args.Timeout)
	}

	waitCtx, cancel := context.WithTimeout(ctx, args.Timeout)
	defer cancel()
	ledger := claims.New(args.Claim.DB, args.Claim.RepoID, nil)
	waitID := fmt.Sprintf("%x", makeWaitID())
	defer func() { _ = ledger.EndWait(context.Background(), waitID) }()
	// Renew frequently even when notification fallback is disabled. A crashed
	// waiter expires without deleting or transferring its held claims.
	interval := 10 * time.Second
	if args.Fallback > 0 && args.Fallback < interval {
		interval = args.Fallback
	}
	for {
		if err := claimWaitContextError(ctx, waitCtx, args); err != nil {
			return nil, err
		}
		blockers, waitErr := ledger.Wait(waitCtx, waitID, args.Claim.AgentID, args.Claim.ItemID, args.Claim.Scope, 30*time.Second)
		if waitErr != nil {
			if contextErr := claimWaitContextError(ctx, waitCtx, args); contextErr != nil {
				return nil, contextErr
			}
			return nil, waitErr
		}
		if len(blockers) == 0 {
			res, err = Claim(waitCtx, args.Claim)
			if err == nil {
				return res, nil
			}
			if !isClaimHeld(err) {
				if contextErr := claimWaitContextError(ctx, waitCtx, args); contextErr != nil {
					return nil, contextErr
				}
				return nil, err
			}
		}

		if _, err := l.WaitWake(waitCtx, interval); err != nil {
			if waitErr := claimWaitContextError(ctx, waitCtx, args); waitErr != nil {
				return nil, waitErr
			}
			return nil, fmt.Errorf("claim --wait: listen: %w", err)
		}
	}
}

func isClaimHeld(err error) bool {
	var held *ClaimHeldError
	var resource *claims.ResourceConflictError
	return errors.As(err, &held) || errors.As(err, &resource)
}

func claimWaitContextError(parent, waitCtx context.Context, args ClaimWaitArgs) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return &ClaimWaitTimeoutError{ItemID: args.Claim.ItemID, After: args.Timeout}
	}
	return waitCtx.Err()
}

func notifyClaimWaiters(ctx context.Context, registry *notify.Registry, repoID, itemID string) {
	// A release can unblock a different item through legacy/group scope.
	endpoints, err := registry.LookupRepo(ctx, repoID)
	if err != nil {
		return
	}
	kinds := map[string]bool{notify.ClaimWaitKind(itemID): true}
	for _, e := range endpoints {
		if strings.HasPrefix(e.Kind, notify.ClaimWaitKind("")) {
			kinds[e.Kind] = true
		}
	}
	for kind := range kinds {
		_ = notify.WakeKind(ctx, registry, repoID, kind, 100*time.Millisecond)
	}
}

func makeWaitID() []byte {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	return id
}
