package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultPlexTVBaseURL = "https://plex.tv"

func (m *Module) plexTVGET(ctx context.Context, path string) ([]byte, int, error) {
	m.mu.RLock()
	token := m.token
	base := m.plexTVBaseURL
	m.mu.RUnlock()
	if token == "" {
		return nil, 0, fmt.Errorf("plex token not configured")
	}
	if base == "" {
		base = defaultPlexTVBaseURL
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if err := guardOutboundURL(base + path); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("X-Plex-Product", "MuxCore")
	req.Header.Set("X-Plex-Client-Identifier", "muxcore-plex-bridge")
	resp, err := m.httpCli.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (m *Module) machineIdentifier(ctx context.Context) (string, error) {
	m.mu.RLock()
	cached := m.machineID
	m.mu.RUnlock()
	if cached != "" {
		return cached, nil
	}
	body, code, err := m.plexGET(ctx, "/identity", nil)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", fmt.Errorf("plex /identity status %d", code)
	}
	var resp struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("plex /identity parse: %w", err)
	}
	id := strings.TrimSpace(resp.MediaContainer.MachineIdentifier)
	if id == "" {
		return "", fmt.Errorf("plex machine identifier missing")
	}
	m.mu.Lock()
	m.machineID = id
	m.mu.Unlock()
	return id, nil
}

type PlexSyncItem struct { //nolint:govet // field order matches Plex sync API JSON
	ID                   string `json:"id"`
	Title                string `json:"title"`
	RootTitle            string `json:"root_title"`
	MetadataType         string `json:"metadata_type"`
	ContentType          string `json:"content_type"`
	MediaType            string `json:"media_type"`
	RatingKey            string `json:"rating_key"`
	State                string `json:"state"`
	Failure              string `json:"failure,omitempty"`
	ItemsCount           int    `json:"items_count"`
	ItemsCompleteCount   int    `json:"items_complete_count"`
	ItemsDownloadedCount int    `json:"items_downloaded_count"`
	TotalSizeBytes       int64  `json:"total_size_bytes"`
	VideoResolution      string `json:"video_resolution,omitempty"`
}

type PlexSyncList struct {
	ID               string         `json:"id"`
	ClientIdentifier string         `json:"client_identifier"`
	DeviceUserID     string         `json:"device_user_id"`
	DeviceName       string         `json:"device_name"`
	DevicePlatform   string         `json:"device_platform"`
	DeviceProduct    string         `json:"device_product"`
	Items            []PlexSyncItem `json:"items"`
}

type syncListsSnapshot struct {
	MachineIdentifier string         `json:"machine_identifier"`
	UpdatedAt         string         `json:"updated_at"`
	Lists             []PlexSyncList `json:"lists"`
}

func (m *Module) fetchSyncLists(ctx context.Context) (syncListsSnapshot, error) {
	machineID, err := m.machineIdentifier(ctx)
	if err != nil {
		return syncListsSnapshot{}, err
	}
	path := fmt.Sprintf("/servers/%s/sync_lists", url.PathEscape(machineID))
	body, code, err := m.plexTVGET(ctx, path)
	if err != nil {
		return syncListsSnapshot{}, err
	}
	if code >= 300 {
		return syncListsSnapshot{}, fmt.Errorf("plex.tv sync_lists status %d", code)
	}
	lists, err := parsePlexTVSyncListsJSON(body)
	if err != nil {
		return syncListsSnapshot{}, err
	}
	return syncListsSnapshot{
		MachineIdentifier: machineID,
		Lists:             lists,
	}, nil
}

func parsePlexTVSyncListsJSON(body []byte) ([]PlexSyncList, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	rawLists := extractSyncListNodes(root)
	out := make([]PlexSyncList, 0, len(rawLists))
	for _, raw := range rawLists {
		list, err := parseSyncListNode(raw)
		if err != nil {
			continue
		}
		out = append(out, list)
	}
	return out, nil
}

func extractSyncListNodes(root map[string]json.RawMessage) []json.RawMessage {
	if raw, ok := root["SyncList"]; ok {
		return jsonArrayOrSingle(raw)
	}
	if mc, ok := root["MediaContainer"]; ok {
		var container map[string]json.RawMessage
		if err := json.Unmarshal(mc, &container); err != nil {
			return nil
		}
		if raw, ok := container["SyncList"]; ok {
			return jsonArrayOrSingle(raw)
		}
	}
	return nil
}

func jsonArrayOrSingle(raw json.RawMessage) []json.RawMessage {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return []json.RawMessage{raw}
}

func parseSyncListNode(raw json.RawMessage) (PlexSyncList, error) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return PlexSyncList{}, err
	}
	list := PlexSyncList{
		ID:               jsonStringField(node, "id"),
		ClientIdentifier: jsonStringField(node, "clientIdentifier", "client_identifier"),
	}
	if deviceRaw, ok := node["Device"]; ok {
		var devices []map[string]json.RawMessage
		if err := json.Unmarshal(deviceRaw, &devices); err != nil {
			var one map[string]json.RawMessage
			if err := json.Unmarshal(deviceRaw, &one); err == nil {
				devices = []map[string]json.RawMessage{one}
			}
		}
		if len(devices) > 0 {
			d := devices[0]
			list.DeviceUserID = jsonStringField(d, "userID", "userId", "user_id")
			list.DeviceName = jsonStringField(d, "name")
			list.DevicePlatform = jsonStringField(d, "platform")
			list.DeviceProduct = jsonStringField(d, "product")
		}
	}
	list.Items = parseSyncItems(node)
	return list, nil
}

func parseSyncItems(node map[string]json.RawMessage) []PlexSyncItem {
	rawItems := node["SyncItem"]
	if rawItems == nil {
		if syncItems, ok := node["SyncItems"]; ok {
			var wrap map[string]json.RawMessage
			if err := json.Unmarshal(syncItems, &wrap); err == nil {
				rawItems = wrap["SyncItem"]
			}
		}
	}
	itemsRaw := jsonArrayOrSingle(rawItems)
	out := make([]PlexSyncItem, 0, len(itemsRaw))
	for _, itemRaw := range itemsRaw {
		item, ok := parseSyncItemNode(itemRaw)
		if ok {
			out = append(out, item)
		}
	}
	return out
}

func parseSyncItemNode(raw json.RawMessage) (PlexSyncItem, bool) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return PlexSyncItem{}, false
	}
	item := PlexSyncItem{
		ID:           jsonStringField(node, "id"),
		Title:        jsonStringField(node, "title"),
		RootTitle:    jsonStringField(node, "rootTitle", "root_title"),
		MetadataType: jsonStringField(node, "metadataType", "metadata_type"),
		ContentType:  jsonStringField(node, "contentType", "content_type"),
		MediaType:    inferSyncMediaType(node),
		RatingKey:    extractSyncRatingKey(node),
	}
	if statusRaw, ok := node["Status"]; ok {
		var statuses []map[string]json.RawMessage
		if err := json.Unmarshal(statusRaw, &statuses); err != nil {
			var one map[string]json.RawMessage
			if err := json.Unmarshal(statusRaw, &one); err == nil {
				statuses = []map[string]json.RawMessage{one}
			}
		}
		if len(statuses) > 0 {
			s := statuses[0]
			item.State = jsonStringField(s, "state")
			item.Failure = jsonStringField(s, "failure")
			item.ItemsCount = jsonIntField(s, "itemsCount", "items_count")
			item.ItemsCompleteCount = jsonIntField(s, "itemsCompleteCount", "items_complete_count")
			item.ItemsDownloadedCount = jsonIntField(s, "itemsDownloadedCount", "items_downloaded_count")
			item.TotalSizeBytes = jsonInt64Field(s, "totalSize", "total_size")
		}
	}
	if settingsRaw, ok := node["MediaSettings"]; ok {
		var settings []map[string]json.RawMessage
		if err := json.Unmarshal(settingsRaw, &settings); err != nil {
			var one map[string]json.RawMessage
			if err := json.Unmarshal(settingsRaw, &one); err == nil {
				settings = []map[string]json.RawMessage{one}
			}
		}
		if len(settings) > 0 {
			item.VideoResolution = jsonStringField(settings[0], "videoResolution", "video_resolution")
		}
	}
	return item, true
}

func inferSyncMediaType(node map[string]json.RawMessage) string {
	for _, raw := range jsonArrayOrSingle(node["Location"]) {
		var loc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &loc); err != nil {
			continue
		}
		uri := jsonStringField(loc, "uri")
		if strings.Contains(uri, "playlist://") {
			return "playlist"
		}
		if strings.Contains(uri, "collection") {
			return "collection"
		}
	}
	meta := strings.ToLower(jsonStringField(node, "metadataType", "metadata_type"))
	if meta != "" {
		return meta
	}
	return jsonStringField(node, "contentType", "content_type")
}

func extractSyncRatingKey(node map[string]json.RawMessage) string {
	for _, raw := range jsonArrayOrSingle(node["Location"]) {
		var loc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &loc); err != nil {
			continue
		}
		uri, err := url.QueryUnescape(jsonStringField(loc, "uri"))
		if err != nil {
			uri = jsonStringField(loc, "uri")
		}
		if key := ratingKeyFromLocationURI(uri); key != "" {
			return key
		}
	}
	return ""
}

func ratingKeyFromLocationURI(uri string) string {
	if uri == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(uri, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "metadata", "collections":
			return parts[i+1]
		}
	}
	return ""
}

func jsonStringField(node map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := node[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func jsonIntField(node map[string]json.RawMessage, keys ...string) int {
	for _, key := range keys {
		raw, ok := node[key]
		if !ok {
			continue
		}
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			return n
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				return v
			}
		}
	}
	return 0
}

func jsonInt64Field(node map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		raw, ok := node[key]
		if !ok {
			continue
		}
		var n int64
		if err := json.Unmarshal(raw, &n); err == nil {
			return n
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

func (m *Module) refreshSyncLists(ctx context.Context) error {
	snap, err := m.fetchSyncLists(ctx)
	if err != nil {
		return err
	}
	snap.UpdatedAt = timeNowRFC3339()
	m.syncListsMu.Lock()
	m.syncListsCache = snap
	m.syncListsMu.Unlock()
	return nil
}

func (m *Module) cachedSyncLists(userID, clientID string) syncListsSnapshot {
	m.syncListsMu.RLock()
	defer m.syncListsMu.RUnlock()
	snap := m.syncListsCache
	if userID == "" && clientID == "" {
		return snap
	}
	filtered := syncListsSnapshot{
		MachineIdentifier: snap.MachineIdentifier,
		UpdatedAt:         snap.UpdatedAt,
		Lists:             make([]PlexSyncList, 0, len(snap.Lists)),
	}
	for _, list := range snap.Lists {
		if clientID != "" && !strings.EqualFold(list.ClientIdentifier, clientID) {
			continue
		}
		if userID != "" && !strings.EqualFold(list.DeviceUserID, userID) {
			continue
		}
		filtered.Lists = append(filtered.Lists, list)
	}
	return filtered
}

func timeNowRFC3339() string {
	return timeNow().UTC().Format("2006-01-02T15:04:05Z07:00")
}

var timeNow = time.Now
