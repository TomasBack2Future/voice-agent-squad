package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zsiec/squad/internal/chat"
)

// seenIDCap bounds the dedupe map per-subscriber so a long-lived SSE
// connection doesn't grow unbounded over hours of activity. When we hit
// the cap we drop the oldest half by walking the map and dropping ids
// below the median; that keeps the cap O(1) without sorting.
const seenIDCap = 4096

// publishInboxChanged emits an inbox_changed event on the bus so SSE
// subscribers (the TUI inbox view, dashboards) refresh after a successful
// intake mutation. action is one of "captured", "accepted", "rejected",
// "auto-refine".
func (s *Server) publishInboxChanged(itemID, action string) {
	s.Bus().Publish(chat.Event{
		Kind: "inbox_changed",
		Payload: map[string]any{
			"item_id": itemID,
			"action":  action,
		},
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Clients may publish as soon as the initial response reaches them.
	ch := s.Bus().Subscribe()
	defer s.Bus().Unsubscribe(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ping := time.NewTicker(s.cfg.pingInterval)
	defer ping.Stop()
	lagFlush := time.NewTicker(s.cfg.lagFlushInterval)
	defer lagFlush.Stop()

	// Dedupe by message id: server-mediated POSTs publish via chat.Post AND
	// the pump re-publishes from the DB on its next tick. Both paths carry
	// the row's id in the payload; track what we've already emitted.
	seen := make(map[float64]bool)

	emit := func(e chat.Event) bool {
		payload, _ := json.Marshal(e)
		if _, err := w.Write([]byte("event: " + e.Kind + "\ndata: ")); err != nil {
			return false
		}
		if _, err := w.Write(payload); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-lagFlush.C:
			// Surface any drops that the publish-side sentinel couldn't
			// place (channel full at the time of overflow) AND any
			// drops the publisher hasn't emitted yet because no later
			// publish came. Emits the same Event shape as the in-band
			// lag sentinel so a single client parser handles both.
			if n := s.Bus().PullDropped(ch); n > 0 {
				if !emit(chat.Event{Kind: "lag", Payload: map[string]any{"dropped": n}}) {
					return
				}
			}
		case e, ok := <-ch:
			if !ok {
				return
			}
			if v, ok := e.Payload["id"]; ok {
				var f float64
				switch id := v.(type) {
				case float64:
					f = id
				case int64:
					f = float64(id)
				}
				if f != 0 {
					if seen[f] {
						continue
					}
					if len(seen) >= seenIDCap {
						// Cap reached: keep only the upper half by id so
						// recent publishes remain deduped while old ids
						// get garbage-collected.
						var pivot float64
						for k := range seen {
							if k > pivot {
								pivot = k
							}
						}
						pivot /= 2
						for k := range seen {
							if k < pivot {
								delete(seen, k)
							}
						}
					}
					seen[f] = true
				}
			}
			if !emit(e) {
				return
			}
		}
	}
}
