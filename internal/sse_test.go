package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
)

func TestHandleSSEPlaySessionNotification(t *testing.T) {
	const sample = `{
		"MediaContainer": {
			"Metadata": [{
				"sessionKey": "99",
				"ratingKey": "12345",
				"title": "Movie",
				"type": "movie",
				"viewOffset": 1000,
				"duration": 7200000,
				"User": {"id": 3, "title": "sam"},
				"Player": {"title": "Safari", "address": "10.0.0.2", "platform": "Safari"}
			}]
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(sample))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var events []string
	testPublishHook = func(_ context.Context, eventType, _ string, _ []byte) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, eventType)
		return nil
	}
	t.Cleanup(func() { testPublishHook = nil })

	m := NewModule(Config{
		BaseURL:         srv.URL,
		Token:           "tok",
		SessionsPollSec: 30,
		GRPCAddr:        "127.0.0.1:0",
		HTTPAddr:        "127.0.0.1:0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	data := `{"PlaySessionStateNotification":{"sessionKey":"99","state":"playing","ratingKey":"12345"}}`
	m.handleSSEData("playing", data)
	mu.Lock()
	n := len(events)
	e0 := events[0]
	mu.Unlock()
	if n != 1 || e0 != playbackevents.EventPlaybackStarted {
		t.Fatalf("events: %v", events)
	}

	m.handleSSEData("playing", `{"PlaySessionStateNotification":{"sessionKey":"99","state":"stopped"}}`)
	mu.Lock()
	n = len(events)
	e1 := events[1]
	mu.Unlock()
	if n != 2 || e1 != playbackevents.EventPlaybackStopped {
		t.Fatalf("after stop events: %v", events)
	}
}

func TestParseSSEStream(t *testing.T) {
	body := "event: playing\ndata: {\"x\":1}\n\nevent: ping\ndata: {}\n\n"
	var got []string
	err := readSSEStream(strings.NewReader(body), func(eventName, data string) error {
		got = append(got, eventName+":"+data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got: %v", got)
	}
}

func TestParsePlaySessionPayload(t *testing.T) {
	raw := map[string]json.RawMessage{
		"PlaySessionStateNotification": json.RawMessage(`{"sessionKey":"7","state":"paused"}`),
	}
	b, _ := json.Marshal(raw)
	var n playSessionStateNotification
	if err := json.Unmarshal(raw["PlaySessionStateNotification"], &n); err != nil {
		t.Fatal(err)
	}
	if n.SessionKey != "7" || n.State != "paused" {
		t.Fatalf("n: %+v", n)
	}
	_ = b
}
