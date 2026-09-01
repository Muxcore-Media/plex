package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	var events []string
	testPublishHook = func(_ context.Context, eventType, _ string, _ []byte) error {
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
		DataDir:         t.TempDir(),
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	data := `{"PlaySessionStateNotification":{"sessionKey":"99","state":"playing","ratingKey":"12345"}}`
	m.handleSSEData(ctx, "playing", data)
	if len(events) != 1 || events[0] != playbackevents.EventPlaybackStarted {
		t.Fatalf("events: %v", events)
	}

	m.handleSSEData(ctx, "playing", `{"PlaySessionStateNotification":{"sessionKey":"99","state":"stopped"}}`)
	if len(events) != 2 || events[1] != playbackevents.EventPlaybackStopped {
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

func TestSSERequestURLWithoutToken(t *testing.T) {
	url := plexNotificationsURL("http://plex.example:32400")
	if strings.Contains(url, "X-Plex-Token") || strings.Contains(url, "token=") {
		t.Fatalf("token leaked in URL: %q", url)
	}
	if url != "http://plex.example:32400/:/eventsource/notifications" {
		t.Fatalf("url: %q", url)
	}
}
