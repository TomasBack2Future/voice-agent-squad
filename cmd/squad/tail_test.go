package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/chat"
)

func TestRunTail_OneShotPrintsLatest(t *testing.T) {
	f := newChatFixture(t)
	if err := f.chat.Post(context.Background(), chat.PostRequest{
		AgentID: f.agentID, Thread: chat.ThreadGlobal, Kind: chat.KindSay, Body: "tail-me",
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if code := runTailBody(context.Background(), f.chat, f.db, "global", false, "", "", &buf); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(buf.String(), "tail-me") {
		t.Fatalf("output=%q", buf.String())
	}
}

func TestRunTail_RejectsBadSince(t *testing.T) {
	f := newChatFixture(t)
	var buf bytes.Buffer
	if code := runTailBody(context.Background(), f.chat, f.db, "all", false, "garbage", "", &buf); code == 0 {
		t.Fatal("expected non-zero on bad --since")
	}
}
