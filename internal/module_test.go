package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
	playbackv1 "github.com/Muxcore-Media/playback-contract/proto/playbackv1"
	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
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
		DataDir:         t.TempDir(),
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	m.pollSessionsOnce(ctx)
	mu.Lock()
	n := len(events)
	mu.Unlock()
	if n != 1 || events[0] != playbackevents.EventPlaybackStarted {
		t.Fatalf("events: %v", events)
	}

	m.pollSessionsOnce(ctx)
	mu.Lock()
	n = len(events)
	e1 := events[1]
	mu.Unlock()
	if n != 2 || e1 != playbackevents.EventPlaybackProgress {
		t.Fatalf("after progress events: %v", events)
	}

	active = false
	m.pollSessionsOnce(ctx)
	mu.Lock()
	n = len(events)
	e2 := events[2]
	mu.Unlock()
	if n != 3 || e2 != playbackevents.EventPlaybackStopped {
		t.Fatalf("after stop events: %v", events)
	}
}

func TestTerminateSessionEmptyID(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	resp, err := m.TerminateSession(context.Background(), &plexv1.TerminateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetOk() || resp.GetError() != "session_id required" {
		t.Fatalf("resp=%+v", resp)
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

	m := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: t.TempDir()})
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
		RatingKey:        "99",
		Title:            "Pilot",
		Type:             "episode",
		GrandparentTitle: "Series",
		ViewOffset:       125000,
		Duration:         3600000,
		User:             plexUser{ID: json.Number("7"), Title: "bob"},
		Player:           plexPlayer{Title: "Apple TV", Address: "192.168.1.10", Platform: "tvOS"},
		TranscodeSession: map[string]any{"x": 1},
	})
	if ev.PositionSeconds != 125 || ev.DurationSeconds != 3600 {
		t.Fatalf("seconds: %+v", ev)
	}
	if ev.Title != "Series — Pilot" || !ev.IsTranscode {
		t.Fatalf("title/transcode: %+v", ev)
	}
}

func TestHealthConfiguredVsEmpty(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	if err := m.Health(context.Background()); err == nil {
		t.Fatal("expected unconfigured error")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"mid-1"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m2 := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	if err := m2.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthz503WhenUnconfigured(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	rec := httptest.NewRecorder()
	m.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHealthzOKWhenConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"mid-1"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: t.TempDir()})
	rec := httptest.NewRecorder()
	m.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlayURL(t *testing.T) {
	m := NewModule(Config{BaseURL: "http://plex.example:32400", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	m.mu.Lock()
	m.machineID = "machine-abc"
	m.mu.Unlock()
	resp, err := m.PlayURL(context.Background(), &plexv1.PlayURLRequest{RatingKey: "999"})
	if err != nil {
		t.Fatal(err)
	}
	want := "http://plex.example:32400/web/index.html#!/server/machine-abc/details?key=/library/metadata/999"
	if resp.GetUrl() != want {
		t.Fatalf("url: %q", resp.GetUrl())
	}
}

func TestPersistSettings(t *testing.T) {
	dir := t.TempDir()
	m := NewModule(Config{
		BaseURL:  "http://plex.local:32400",
		Token:    "secret-token",
		DataDir:  dir,
		GRPCAddr: "127.0.0.1:0",
		HTTPAddr: "127.0.0.1:0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	m2 := NewModule(Config{DataDir: dir, GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	if err := m2.loadDurable(); err != nil {
		t.Fatal(err)
	}
	if !m2.configured() || m2.baseURL != "http://plex.local:32400" {
		t.Fatalf("reloaded: url=%q configured=%v", m2.baseURL, m2.configured())
	}
}

func TestUpdateSettingPlexSSE(t *testing.T) {
	dir := t.TempDir()
	m := NewModule(Config{DataDir: dir, GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSetting("plex_sse", "disabled"); err != nil {
		t.Fatal(err)
	}
	if m.sseEnabled() {
		t.Fatal("expected sse disabled")
	}
}
