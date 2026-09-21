package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/chat"
)

func TestSSE_RoundTripsPostedMessage(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-x", "X")
	s := New(db, testRepoID, Config{RepoID: testRepoID})
	defer s.Close()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		time.Sleep(150 * time.Millisecond)
		body, _ := json.Marshal(map[string]any{"thread": "global", "body": "sse-hello"})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Squad-Agent", "agent-x")
		resp, err := srv.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type=%q", got)
	}

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			continue
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, "sse-hello") {
			return
		}
	}
	t.Fatal("did not observe message event within 3s")
}

type firstFlushResponseWriter struct {
	http.ResponseWriter
	onFirstFlush func()
}

func (w *firstFlushResponseWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	if publish := w.onFirstFlush; publish != nil {
		w.onFirstFlush = nil
		publish()
	}
}

func TestSSE_SubscribedBeforeResponseFlush(t *testing.T) {
	s := New(newTestDB(t), testRepoID, Config{pingInterval: time.Hour})
	defer s.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleEvents(&firstFlushResponseWriter{ResponseWriter: w, onFirstFlush: func() {
			s.Bus().Publish(chat.Event{Kind: "message", Payload: map[string]any{"body": "ready"}})
		}}, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("message published at initial response flush was lost: %v", err)
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"body":"ready"`) {
			return
		}
	}
}

func TestSSE_LagEventReachesStream(t *testing.T) {
	s := New(newTestDB(t), testRepoID, Config{
		RepoID:           testRepoID,
		pingInterval:     time.Hour,
		lagFlushInterval: 50 * time.Millisecond,
	})
	defer s.Close()
	probe := s.Bus().Subscribe()
	bufferSize := cap(probe)
	s.Bus().Unsubscribe(probe)
	const overflow = 8
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleEvents(&firstFlushResponseWriter{ResponseWriter: w, onFirstFlush: func() {
			// The handler cannot drain its subscription until this flush returns.
			// Stop publishing after overflow so only the independent lag tick can report it.
			for i := 0; i < bufferSize+overflow; i++ {
				s.Bus().Publish(chat.Event{Kind: "message", Payload: map[string]any{"id": int64(i + 1)}})
			}
		}}, r)
	}))
	defer srv.Close()

	reqCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("expected event: lag within 3s of forced overflow: %v", err)
		}
		if strings.HasPrefix(line, "event: lag") {
			data, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			var event chat.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &event); err != nil {
				t.Fatalf("invalid lag envelope %q: %v", data, err)
			}
			dropped, ok := event.Payload["dropped"].(float64)
			if event.Kind != "lag" || !ok || dropped < overflow {
				t.Fatalf("unexpected lag envelope: %#v", event)
			}
			return
		}
	}
}

func TestSSE_PingsBeforeFirstEvent(t *testing.T) {
	db := newTestDB(t)
	s := New(db, testRepoID, Config{pingInterval: 50 * time.Millisecond})
	defer s.Close()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	reqCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, ": ping") {
			return
		}
	}
	t.Fatal("expected at least one ping comment within 400ms")
}
