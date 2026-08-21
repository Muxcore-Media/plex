package internal

import (
	"context"
	"encoding/json"
	"strings"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
	playbackv1 "github.com/Muxcore-Media/playback-contract/proto/playbackv1"
)

type libraryCatalogPayload struct {
	Action        string `json:"action"`
	ServerID      string `json:"server_id"`
	ServerType    string `json:"server_type"`
	ItemID        string `json:"item_id"`
	MediaType     string `json:"media_type,omitempty"`
	MuxcoreID     string `json:"muxcore_id,omitempty"`
	Title         string `json:"title,omitempty"`
	MediaPath     string `json:"media_path,omitempty"`
	LibraryName   string `json:"library_name,omitempty"`
	FileSizeBytes int64  `json:"file_size_bytes,omitempty"`
	VideoResolution string `json:"video_resolution,omitempty"`
	ParentID      string `json:"parent_id,omitempty"`
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
	Key  json.Number `json:"key"`
	Type string      `json:"type"`
	Title string     `json:"title"`
}

type plexMediaItem struct {
	RatingKey       string      `json:"ratingKey"`
	ParentRatingKey string      `json:"parentRatingKey"`
	Type            string      `json:"type"`
	Title     string      `json:"title"`
	Size      int64       `json:"size"`
	Media     []plexMedia `json:"Media"`
}

type plexMedia struct {
	VideoResolution string `json:"videoResolution"`
	Part []struct {
		Size int64  `json:"size"`
		File string `json:"file"`
	} `json:"Part"`
}

func (m *Module) listPlexLibrarySections(ctx context.Context) ([]plexLibrarySection, error) {
	body, code, err := m.plexGET(ctx, "/library/sections")
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, err
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

func (m *Module) listPlexSectionItems(ctx context.Context, sectionKey string) ([]plexMediaItem, error) {
	path := "/library/sections/" + sectionKey + "/all"
	body, code, err := m.plexGET(ctx, path)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, err
	}
	var resp struct {
		MediaContainer struct {
			Metadata []plexMediaItem `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.MediaContainer.Metadata, nil
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
	published := 0
	for _, sec := range sections {
		key := strings.TrimSpace(sec.Key.String())
		if key == "" {
			continue
		}
		items, err := m.listPlexSectionItems(ctx, key)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.RatingKey == "" {
				continue
			}
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
			})
			published++
		}
	}
	return published, nil
}
