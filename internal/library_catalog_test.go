package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
)

func TestListPlexSectionItemsPagination(t *testing.T) {
	const pageSize = 100
	totalItems := 150
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections/1/all" {
			http.NotFound(w, r)
			return
		}
		start, _ := strconv.Atoi(r.Header.Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(r.Header.Get("X-Plex-Container-Size"))
		if size == 0 {
			size = pageSize
		}
		end := start + size
		if end > totalItems {
			end = totalItems
		}
		var meta []map[string]any
		for i := start; i < end; i++ {
			meta = append(meta, map[string]any{
				"ratingKey": strconv.Itoa(i + 1),
				"type":      "movie",
				"title":     "Movie",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"MediaContainer": map[string]any{
				"size":     totalItems,
				"offset":   start,
				"Metadata": meta,
			},
		})
	}))
	defer srv.Close()

	m := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	items, err := m.listPlexSectionItems(context.Background(), plexLibrarySection{
		Key:   json.Number("1"),
		Type:  "movie",
		Title: "Movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != totalItems {
		t.Fatalf("items: got %d want %d", len(items), totalItems)
	}
}

func TestParsePlexGuids(t *testing.T) {
	imdb, tmdb, tvdb := parsePlexGuids([]plexGuid{
		{ID: "com.plexapp.agents.imdb://tt0111161?lang=en"},
		{ID: "com.plexapp.agents.themoviedb://550?lang=en"},
		{ID: "com.plexapp.agents.thetvdb://79169?lang=en"},
	})
	if imdb != "tt0111161" || tmdb != 550 || tvdb != 79169 {
		t.Fatalf("ids: imdb=%q tmdb=%d tvdb=%d", imdb, tmdb, tvdb)
	}
}

func TestSyncLibraryCatalogUpsertRemovedGuid(t *testing.T) {
	const sectionJSON = `{"MediaContainer":{"Directory":[{"key":"1","type":"movie","title":"Movies"}]}}`
	itemJSON := func(key, imdb string) string {
		return `{"MediaContainer":{"Metadata":[{"ratingKey":"` + key + `","type":"movie","title":"Title","Guid":[{"id":"com.plexapp.agents.imdb://` + imdb + `?lang=en"}]}]}}`
	}

	state := "a"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/library/sections":
			_, _ = w.Write([]byte(sectionJSON))
		case strings.HasPrefix(r.URL.Path, "/library/sections/1/all"):
			switch state {
			case "a":
				_, _ = w.Write([]byte(itemJSON("100", "tt100")))
			default:
				_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var events []libraryCatalogPayload
	testPublishHook = func(_ context.Context, _ string, _ string, payload []byte) error {
		var item libraryCatalogPayload
		if err := json.Unmarshal(payload, &item); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		events = append(events, item)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { testPublishHook = nil })

	m := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	ctx := context.Background()

	n, err := m.syncLibraryCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("published: %d", n)
	}
	mu.Lock()
	if len(events) != 1 || events[0].Action != "upsert" || events[0].ImdbID != "tt100" {
		t.Fatalf("first sync: %+v", events)
	}
	mu.Unlock()

	state = "b"
	n, err = m.syncLibraryCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("second published: %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[1].Action != "removed" || events[1].ItemID != "100" {
		t.Fatalf("removed: %+v", events[1:])
	}
}

func TestListPlexLibrarySectionsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := NewModule(Config{BaseURL: srv.URL, Token: "tok", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	_, err := m.listPlexLibrarySections(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("err: %v", err)
	}
}

func TestLibraryCatalogEventTypes(t *testing.T) {
	if playbackevents.EventPlaybackLibraryItem == "" {
		t.Fatal("missing event constant")
	}
}
