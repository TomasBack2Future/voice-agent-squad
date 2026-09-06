// Package notify is the cross-session wake mechanism: peer chat publishers
// look up active session listeners in notify_endpoints and dial each one's
// loopback port to wake them. The endpoints rows are written by the
// stop-listen hook and dropped by session-end-cleanup.
package notify

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"
)

// Wake fans out a connect-and-drop signal to every listen endpoint
// registered for repoID. The connect itself is the wake — the receiver's
// Accept() returning is enough; we never write or read bytes.
//
// Per-endpoint dial failures are swallowed: a dead listener is the
// expected steady state when a session has ended without firing the
// SessionEnd cleanup. The receiver-side housekeeping (TTL sweep) is
// what reclaims the row, not the sender.
func Wake(ctx context.Context, r *Registry, repoID string, perDial time.Duration) error {
	return WakeKind(ctx, r, repoID, KindListen, perDial)
}

func WakeKind(ctx context.Context, r *Registry, repoID, kind string, perDial time.Duration) error {
	eps, err := r.LookupRepo(ctx, repoID)
	if err != nil {
		return err
	}
	if perDial <= 0 {
		perDial = 100 * time.Millisecond
	}
	var wg sync.WaitGroup
	for _, e := range eps {
		if e.Kind != kind {
			continue
		}
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			d := net.Dialer{Timeout: perDial}
			c, err := d.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				return
			}
			_ = c.Close()
		}(e.Port)
	}
	wg.Wait()
	return nil
}
