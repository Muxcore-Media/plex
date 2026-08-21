package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
	playbackv1 "github.com/Muxcore-Media/playback-contract/proto/playbackv1"
)

func TestPlexSessionPollPublishesEvents(t *testing.T) {
	const sample = `{
		"MediaContainer": {
			"Metadata": [{
				"sessionKey": "42",
				"ratingKey": "12345",
				"title": "Episode 1",
				"type": "episode",
				"grandparentTitle": "Show",
				"viewOffset": 60000,
				"duration": 3600000,
				"width": 1920,
				"height": 1080,
				"User": {"id": 1, "title": "alice"},
				"Player": {"title": "Chrome", "address": "10.0.0.5", "platform": "Chrome"},
				"TranscodeSession": {"key": "t1"}
			}]
		}
	}`

	active := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/sessions":
			w.Header().Set("Content-Type", "application/json")
			if active {
				_, _ = w.Write([]byte(sample))
			} else {
				_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
			}
		case "/identity":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"abc"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var events []string
	testPublishHook = func(_ context.Context, eventType, _ string, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, eventType)
		msg, err := playbackv1.UnmarshalSessionEvent(payload)
		if err != nil {
			t.Errorf("unmarshal: %v", err)
			return nil
		}
		if eventType == playbackevents.EventPlaybackStarted {
			if msg.GetUserName() != "alice" || msg.GetItemId() != "12345" || !msg.GetIsTranscode() {
				t.Errorf("bad payload: %+v", msg)
			}
		}
		return nil
	}
	t.Cleanup(func() { testPublishHook = nil })

	m := NewModule(Config{
		BaseURL:         srv.URL,
		Token:           "test-token",
		SessionsPollSec: 30,
		GRPCAddr:        "127.0.0.1:0",
		HTTPAddr:        "127.0.0.1:0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	m.pollSessionsOnce()
	mu.Lock()
	n := len(events)
	mu.Unlock()
	if n != 1 || events[0] != playbackevents.EventPlaybackStarted {
		t.Fatalf("events: %v", events)
	}

	m.pollSessionsOnce()
	mu.Lock()
	n = len(events)
	e1 := events[1]
	mu.Unlock()
	if n != 2 || e1 != playbackevents.EventPlaybackProgress {
		t.Fatalf("after progress events: %v", events)
	}

	active = false
	m.pollSessionsOnce()
	mu.Lock()
	n = len(events)
	e2 := events[2]
	mu.Unlock()
	if n != 3 || e2 != playbackevents.EventPlaybackStopped {
		t.Fatalf("after stop events: %v", events)
	}
}

func TestTerminateSession(t *testing.T) {
	var hit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status/sessions/terminate" {
			hit = r.URL.Query().Get("sessionKey")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	resp, err := m.TerminateSession(ctx, &plexv1.TerminateSessionRequest{SessionId: "42", Reason: "limit"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetOk() || hit != "42" {
		t.Fatalf("resp=%+v hit=%q", resp, hit)
	}
}

func TestSessionToEvent(t *testing.T) {
	ev := sessionToEvent(plexSession{
		RatingKey:  "99",
		Title:      "Pilot",
		Type:       "episode",
		GrandparentTitle: "Series",
		ViewOffset: 125000,
		Duration:   3600000,
		User:       plexUser{ID: json.Number("7"), Title: "bob"},
		Player:     plexPlayer{Title: "Apple TV", Address: "192.168.1.10", Platform: "tvOS"},
		TranscodeSession: map[string]any{"x": 1},
	})
	if ev.PositionSeconds != 125 || ev.DurationSeconds != 3600 {
		t.Fatalf("seconds: %+v", ev)
	}
	if ev.Title != "Series — Pilot" || !ev.IsTranscode {
		t.Fatalf("title/transcode: %+v", ev)
	}
}
