package internal

import (
	"context"
	"log/slog"
	"time"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
	playbackv1 "github.com/Muxcore-Media/playback-contract/proto/playbackv1"
)

type playbackEventPayload struct {
	ItemID          string `json:"item_id"`
	PlexRatingKey   string `json:"plex_rating_key"`
	UserID          string `json:"user_id"`
	UserName        string `json:"user_name"`
	SessionID       string `json:"session_id"`
	PositionSeconds int64  `json:"position_seconds"`
	DurationSeconds int64  `json:"duration_seconds"`
	Title           string `json:"title"`
	MediaType       string `json:"media_type"`
	ServerType      string `json:"server_type"`
	IsTranscode     bool   `json:"is_transcode,omitempty"`
	PlayMethod      string `json:"play_method,omitempty"`
	Platform        string `json:"platform,omitempty"`
	Device          string `json:"device,omitempty"`
	Player          string `json:"player,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`
	StreamResolution string `json:"stream_resolution,omitempty"`
	VideoHeight     int    `json:"video_height,omitempty"`
	VideoWidth      int    `json:"video_width,omitempty"`
}

func (m *Module) pollSessionsLoop() {
	for {
		m.mu.RLock()
		sec := m.sessionsPollSec
		m.mu.RUnlock()
		wait := time.Second
		if sec > 0 && m.configured() {
			if m.sseConnectedNow() {
				wait = time.Duration(sec*2) * time.Second
				if wait < 60*time.Second {
					wait = 60 * time.Second
				}
			} else {
				wait = time.Duration(sec) * time.Second
				m.pollSessionsOnce()
			}
		}
		select {
		case <-m.stopCh:
			return
		case <-time.After(wait):
		}
	}
}

func (m *Module) pollSessionsOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sessions, err := m.listSessions(ctx)
	if err != nil {
		slog.Debug("plex: sessions poll failed", "error", err)
		return
	}
	m.mu.Lock()
	m.lastActive = len(sessions)
	m.mu.Unlock()

	active := map[string]bool{}
	for _, s := range sessions {
		key := s.SessionKey
		if key == "" {
			key = s.User.ID.String() + ":" + s.RatingKey
		}
		active[key] = true
		ev := sessionToEvent(s)
		m.mu.Lock()
		prev := m.sessionSeen[key]
		m.sessionSeen[key] = "playing"
		m.mu.Unlock()
		if prev == "" {
			m.publishPlayback(ctx, playbackevents.EventPlaybackStarted, ev)
		} else {
			m.publishPlayback(ctx, playbackevents.EventPlaybackProgress, ev)
		}
	}
	m.mu.Lock()
	for key := range m.sessionSeen {
		if !active[key] {
			delete(m.sessionSeen, key)
			m.mu.Unlock()
			m.publishPlayback(ctx, playbackevents.EventPlaybackStopped, playbackEventPayload{
				SessionID:  key,
				ServerType: "plex",
			})
			m.mu.Lock()
		}
	}
	m.mu.Unlock()
}

func sessionToEvent(s plexSession) playbackEventPayload {
	isTranscode := s.TranscodeSession != nil && len(s.TranscodeSession) > 0
	playMethod := "DirectPlay"
	if isTranscode {
		playMethod = "Transcode"
	}
	return playbackEventPayload{
		ItemID:          s.RatingKey,
		PlexRatingKey:   s.RatingKey,
		UserID:          s.User.ID.String(),
		UserName:        s.User.Title,
		SessionID:       s.SessionKey,
		PositionSeconds: msToSeconds(s.ViewOffset),
		DurationSeconds: msToSeconds(s.Duration),
		Title:           displayTitle(s),
		MediaType:       s.Type,
		ServerType:      "plex",
		IsTranscode:     isTranscode,
		PlayMethod:      playMethod,
		Platform:        firstNonEmpty(s.Player.Platform, s.Player.Title),
		Device:          s.Player.Title,
		Player:          s.Player.Title,
		IPAddress:       s.Player.Address,
		StreamResolution: streamResolutionFromPlexSession(s),
	}
}

func (m *Module) publishPlayback(ctx context.Context, eventType string, ev playbackEventPayload) {
	data, err := playbackv1.MarshalSessionEvent(m.sessionInputFromPayload(eventType, ev))
	if err != nil {
		return
	}
	if err := m.publishEvent(ctx, eventType, data); err != nil {
		slog.Debug("plex: publish playback failed", "type", eventType, "error", err)
	}
}

func (m *Module) sessionInputFromPayload(eventType string, ev playbackEventPayload) playbackv1.SessionInput {
	itemID := ev.ItemID
	if itemID == "" {
		itemID = ev.PlexRatingKey
	}
	serverType := ev.ServerType
	if serverType == "" {
		serverType = "plex"
	}
	return playbackv1.SessionInput{
		EventType:         eventType,
		SourceModule:      m.id,
		ServerID:          m.id,
		ServerType:        serverType,
		ExternalSessionID: ev.SessionID,
		UserID:            ev.UserID,
		UserName:          ev.UserName,
		ItemID:            itemID,
		Title:             ev.Title,
		MediaType:         ev.MediaType,
		PositionSeconds:   ev.PositionSeconds,
		DurationSeconds:   ev.DurationSeconds,
		IsTranscode:       ev.IsTranscode,
		PlayMethod:        ev.PlayMethod,
		Platform:          ev.Platform,
		Device:            ev.Device,
		Player:            ev.Player,
		IPAddress:         ev.IPAddress,
		StreamResolution:  streamResolutionFromPayload(ev),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
