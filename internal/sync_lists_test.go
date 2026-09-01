package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
)

func TestFetchAndParseSyncLists(t *testing.T) {
	const syncListsJSON = `{
		"MediaContainer": {
			"SyncList": [{
				"id": "list-1",
				"clientIdentifier": "client-abc",
				"Device": {
					"userID": "42",
					"name": "Pixel Phone",
					"platform": "Android",
					"product": "Plex for Android"
				},
				"SyncItem": [{
					"id": "item-1",
					"title": "Pilot",
					"rootTitle": "Show Name",
					"metadataType": "episode",
					"contentType": "video",
					"Location": {"uri": "library://x/metadata/99999/"},
					"Status": {
						"state": "complete",
						"itemsCount": "10",
						"itemsCompleteCount": "10",
						"itemsDownloadedCount": "10",
						"totalSize": "1073741824"
					},
					"MediaSettings": {"videoResolution": "720"}
				}]
			}]
		}
	}`

	pms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"machine-123"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer pms.Close()

	plextv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/servers/machine-123/sync_lists" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(syncListsJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer plextv.Close()

	m := NewModule(Config{
		BaseURL:  pms.URL,
		Token:    "token",
		GRPCAddr: "127.0.0.1:0",
		HTTPAddr: "127.0.0.1:0",
	})
	m.mu.Lock()
	m.plexTVBaseURL = plextv.URL
	m.mu.Unlock()

	snap, err := m.fetchSyncLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.MachineIdentifier != "machine-123" {
		t.Fatalf("machine: %q", snap.MachineIdentifier)
	}
	if len(snap.Lists) != 1 {
		t.Fatalf("lists: %#v", snap.Lists)
	}
	list := snap.Lists[0]
	if list.DeviceUserID != "42" || list.ClientIdentifier != "client-abc" {
		t.Fatalf("list meta: %#v", list)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items: %#v", list.Items)
	}
	item := list.Items[0]
	if item.RatingKey != "99999" || item.State != "complete" || item.VideoResolution != "720" {
		t.Fatalf("item: %#v", item)
	}
	if item.TotalSizeBytes != 1073741824 {
		t.Fatalf("size: %d", item.TotalSizeBytes)
	}
}

func TestSyncListsHTTPFilter(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", HTTPSecret: "secret"})
	m.mu.Lock()
	m.baseURL = "http://example"
	m.token = "token"
	m.syncListsCache = syncListsSnapshot{
		MachineIdentifier: "machine-123",
		UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
		Lists: []PlexSyncList{
			{ClientIdentifier: "c1", DeviceUserID: "1", Items: []PlexSyncItem{{ID: "a"}}},
			{ClientIdentifier: "c2", DeviceUserID: "2", Items: []PlexSyncItem{{ID: "b"}}},
		},
	}
	m.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/sync-lists?user_id=2", nil)
	req.Header.Set(headerPlexBridgeSecret, "secret")
	rec := httptest.NewRecorder()
	m.handleSyncListsHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body syncListsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lists) != 1 || body.Lists[0].DeviceUserID != "2" {
		t.Fatalf("filtered: %#v", body.Lists)
	}
}

func TestSyncListsHTTPUnauthorized(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", HTTPSecret: "secret"})
	rec := httptest.NewRecorder()
	m.handleSyncListsHTTP(rec, httptest.NewRequest(http.MethodGet, "/sync-lists", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestSyncListsHTTPUnauthorizedEmptySecret(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync-lists", nil)
	req.Header.Set(headerPlexBridgeSecret, "anything")
	m.handleSyncListsHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestListSyncListsRPC(t *testing.T) {
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	m.syncListsMu.Lock()
	m.syncListsCache = syncListsSnapshot{
		MachineIdentifier: "machine-123",
		UpdatedAt:         "2026-01-01T00:00:00Z",
		Lists: []PlexSyncList{
			{ID: "list-1", ClientIdentifier: "c1", DeviceUserID: "9"},
		},
	}
	m.syncListsMu.Unlock()
	m.mu.Lock()
	m.baseURL = "http://example"
	m.token = "token"
	m.mu.Unlock()

	resp, err := m.ListSyncLists(context.Background(), &plexv1.ListSyncListsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetMachineIdentifier() != "machine-123" || len(resp.GetLists()) != 1 {
		t.Fatalf("resp: %#v", resp)
	}
}

func TestParsePlexTVSyncListsJSON(t *testing.T) {
	lists, err := parsePlexTVSyncListsJSON([]byte(`{"SyncList":{"id":"x","SyncItem":{"id":"i1","title":"Movie"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 || len(lists[0].Items) != 1 || lists[0].Items[0].Title != "Movie" {
		t.Fatalf("lists: %#v", lists)
	}
}
