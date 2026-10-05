package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
	playbackv1 "github.com/Muxcore-Media/contracts-playback/playbackv1"
)

const plexCatalogPageSize = 100

type libraryCatalogPayload struct { //nolint:govet // field order matches library catalog event JSON
	Action          string `json:"action"`
	ServerID        string `json:"server_id"`
	ServerType      string `json:"server_type"`
	ItemID          string `json:"item_id"`
	MediaType       string `json:"media_type,omitempty"`
	MuxcoreID       string `json:"muxcore_id,omitempty"`
	Title           string `json:"title,omitempty"`
	MediaPath       string `json:"media_path,omitempty"`
	LibraryName     string `json:"library_name,omitempty"`
	FileSizeBytes   int64  `json:"file_size_bytes,omitempty"`
	VideoResolution string `json:"video_resolution,omitempty"`
	ParentID        string `json:"parent_id,omitempty"`
	ImdbID          string `json:"imdb_id,omitempty"`
	TmdbID          int64  `json:"tmdb_id,omitempty"`
	TvdbID          int64  `json:"tvdb_id,omitempty"`
}

func (m *Module) publishLibraryCatalogEvent(ctx context.Context, action string, item libraryCatalogPayload) {
	item.Action = action
	item.ServerID = m.id
	item.ServerType = "plex"
	if item.ItemID == "" {
		return
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return
	}
	if err := m.publishEvent(ctx, playbackevents.EventPlaybackLibraryItem, payload); err != nil {
		_ = err
	}
}

type plexLibrarySection struct {
	Key   json.Number `json:"key"`
	Type  string      `json:"type"`
	Title string      `json:"title"`
}

type plexGuid struct {
	ID string `json:"id"`
}

type plexMediaItem struct { //nolint:govet // field order matches Plex API JSON
	RatingKey       string      `json:"ratingKey"`
	ParentRatingKey string      `json:"parentRatingKey"`
	Type            string      `json:"type"`
	Title           string      `json:"title"`
	Size            int64       `json:"size"`
	Guid            []plexGuid  `json:"Guid"`
	Media           []plexMedia `json:"Media"`
}

type plexMediaPart struct { //nolint:govet // field order matches Plex API JSON
	Size int64  `json:"size"`
	File string `json:"file"`
}

type plexMedia struct {
	VideoResolution string          `json:"videoResolution"`
	Part            []plexMediaPart `json:"Part"`
}

func (m *Module) listPlexLibrarySections(ctx context.Context) ([]plexLibrarySection, error) {
	body, code, err := m.plexGET(ctx, "/library/sections", nil)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, fmt.Errorf("plex /library/sections status %d", code)
	}
	var resp struct {
		MediaContainer struct {
			Directory []plexLibrarySection `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.MediaContainer.Directory, nil
}

func sectionItemTypeFilters(sec plexLibrarySection) []string {
	switch strings.ToLower(strings.TrimSpace(sec.Type)) {
	case "show":
		return []string{"4"}
	case "artist":
		return []string{"10"}
	default:
		return []string{""}
	}
}

func (m *Module) listPlexSectionItems(ctx context.Context, section plexLibrarySection) ([]plexMediaItem, error) {
	key := strings.TrimSpace(section.Key.String())
	if key == "" {
		return nil, nil
	}
	var all []plexMediaItem
	for _, typeFilter := range sectionItemTypeFilters(section) {
		items, err := m.listPlexSectionItemsPaged(ctx, key, typeFilter)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func (m *Module) listPlexSectionItemsPaged(ctx context.Context, sectionKey, typeFilter string) ([]plexMediaItem, error) {
	path := "/library/sections/" + sectionKey + "/all"
	if typeFilter != "" {
		path += "?type=" + typeFilter
	}
	var all []plexMediaItem
	start := 0
	for {
		body, code, err := m.plexGET(ctx, path, &plexGETHeaders{
			ContainerStart: start,
			ContainerSize:  plexCatalogPageSize,
		})
		if err != nil {
			return nil, err
		}
		if code >= 300 {
			return nil, fmt.Errorf("plex %s status %d", path, code)
		}
		var resp struct {
			MediaContainer struct {
				Metadata []plexMediaItem `json:"Metadata"`
				Size     json.Number     `json:"size"`
				Offset   json.Number     `json:"offset"`
			} `json:"MediaContainer"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		page := resp.MediaContainer.Metadata
		all = append(all, page...)
		if len(page) < plexCatalogPageSize {
			break
		}
		total, _ := resp.MediaContainer.Size.Int64()
		start += len(page)
		if total > 0 && int64(start) >= total {
			break
		}
	}
	return all, nil
}

func parsePlexGuids(guids []plexGuid) (imdb string, tmdb, tvdb int64) {
	for _, g := range guids {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		lower := strings.ToLower(id)
		switch {
		case strings.Contains(lower, "imdb://"):
			if imdb == "" {
				imdb = plexGUIDValue(id, "imdb://")
				if imdb != "" && !strings.HasPrefix(imdb, "tt") {
					imdb = "tt" + imdb
				}
			}
		case strings.Contains(lower, "themoviedb://"):
			if tmdb == 0 {
				if v, err := strconv.ParseInt(plexGUIDValue(id, "themoviedb://"), 10, 64); err == nil {
					tmdb = v
				}
			}
		case strings.Contains(lower, "thetvdb://"):
			if tvdb == 0 {
				if v, err := strconv.ParseInt(plexGUIDValue(id, "thetvdb://"), 10, 64); err == nil {
					tvdb = v
				}
			}
		}
	}
	return imdb, tmdb, tvdb
}

func plexGUIDValue(guid, marker string) string {
	idx := strings.Index(strings.ToLower(guid), strings.ToLower(marker))
	if idx < 0 {
		return ""
	}
	rest := guid[idx+len(marker):]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[:q]
	}
	return strings.TrimSpace(rest)
}

func plexItemFileSize(item plexMediaItem) int64 {
	if item.Size > 0 {
		return item.Size
	}
	for _, media := range item.Media {
		for _, part := range media.Part {
			if part.Size > 0 {
				return part.Size
			}
		}
	}
	return 0
}

func plexItemPath(item plexMediaItem) string {
	for _, media := range item.Media {
		for _, part := range media.Part {
			if part.File != "" {
				return part.File
			}
		}
	}
	return ""
}

func plexItemVideoResolution(item plexMediaItem) string {
	for _, media := range item.Media {
		if label := strings.TrimSpace(media.VideoResolution); label != "" {
			return playbackv1.NormalizeStreamResolution(0, 0, label)
		}
	}
	return ""
}

func (m *Module) syncLibraryCatalog(ctx context.Context) (int, error) {
	if !m.configured() {
		return 0, nil
	}
	sections, err := m.listPlexLibrarySections(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{})
	published := 0
	for _, sec := range sections {
		items, err := m.listPlexSectionItems(ctx, sec)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.RatingKey == "" {
				continue
			}
			seen[it.RatingKey] = struct{}{}
			imdb, tmdb, tvdb := parsePlexGuids(it.Guid)
			m.publishLibraryCatalogEvent(ctx, "upsert", libraryCatalogPayload{
				ItemID:          it.RatingKey,
				MediaType:       it.Type,
				Title:           it.Title,
				MediaPath:       plexItemPath(it),
				LibraryName:     sec.Title,
				FileSizeBytes:   plexItemFileSize(it),
				MuxcoreID:       "plex:" + it.RatingKey,
				VideoResolution: plexItemVideoResolution(it),
				ParentID:        strings.TrimSpace(it.ParentRatingKey),
				ImdbID:          imdb,
				TmdbID:          tmdb,
				TvdbID:          tvdb,
			})
			published++
		}
	}
	m.catalogSeenMu.Lock()
	prev := m.catalogSeenKeys
	m.catalogSeenKeys = seen
	m.catalogSeenMu.Unlock()
	for key := range prev {
		if _, ok := seen[key]; !ok {
			m.publishLibraryCatalogEvent(ctx, "removed", libraryCatalogPayload{ItemID: key})
			published++
		}
	}
	return published, nil
}
