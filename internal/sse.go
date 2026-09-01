package internal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	playbackevents "github.com/Muxcore-Media/contracts-playback/events"
)

const sseIdleTimeout = 120 * time.Second

type playSessionStateNotification struct { //nolint:govet // field order matches Plex SSE payload
	SessionKey string `json:"sessionKey"`
	State      string `json:"state"`
	ViewOffset int64  `json:"viewOffset"`
	RatingKey  string `json:"ratingKey"`
}

func (m *Module) sseLoop(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}
		if !m.sseEnabled() || !m.configured() {
			select {
			case <-m.stopCh:
				return
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		err := m.runSSEConnection(ctx)
		m.setSSEConnected(false)
		if err != nil {
			slog.Debug("plex: sse disconnected", "error", err)
		}
		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func plexNotificationsURL(base string) string {
	return base + "/:/eventsource/notifications"
}

func (m *Module) runSSEConnection(ctx context.Context) error {
	m.mu.RLock()
	base, token := m.baseURL, m.token
	m.mu.RUnlock()
	reqURL := plexNotificationsURL(base)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("X-Plex-Product", "MuxCore")
	req.Header.Set("X-Plex-Client-Identifier", "muxcore-plex-bridge")

	sseClient := &http.Client{Timeout: 0}
	resp, err := sseClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("plex sse status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	m.setSSEConnected(true)
	m.touchSSEActivity()
	slog.Info("plex: sse connected")

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go m.watchSSEIdle(streamCtx, cancel)

	return readSSEStream(resp.Body, func(eventName, data string) error {
		select {
		case <-m.stopCh:
			return io.EOF
		case <-streamCtx.Done():
			return streamCtx.Err()
		default:
		}
		m.touchSSEActivity()
		m.handleSSEData(ctx, eventName, data)
		return nil
	})
}

func (m *Module) watchSSEIdle(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sseActivityMu.Lock()
			last := m.sseLastActivity
			m.sseActivityMu.Unlock()
			if time.Since(last) > sseIdleTimeout {
				cancel()
				return
			}
		}
	}
}

func (m *Module) touchSSEActivity() {
	m.sseActivityMu.Lock()
	m.sseLastActivity = time.Now()
	m.sseActivityMu.Unlock()
}

func readSSEStream(r io.Reader, onEvent func(eventName, data string) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	var eventName string
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(dataLines) > 0 {
				if err := onEvent(eventName, strings.Join(dataLines, "\n")); err != nil {
					return err
				}
			}
			eventName = ""
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (m *Module) handleSSEData(ctx context.Context, eventName, data string) {
	if strings.TrimSpace(data) == "" {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return
	}
	if payload, ok := raw["PlaySessionStateNotification"]; ok {
		var direct playSessionStateNotification
		if err := json.Unmarshal(payload, &direct); err == nil {
			m.handlePlaySessionNotification(ctx, direct)
			return
		}
		var list []playSessionStateNotification
		if err := json.Unmarshal(payload, &list); err == nil {
			for _, n := range list {
				m.handlePlaySessionNotification(ctx, n)
			}
			return
		}
	}
	var container struct {
		Type                         string          `json:"type"`
		PlaySessionStateNotification json.RawMessage `json:"PlaySessionStateNotification"`
	}
	if err := json.Unmarshal([]byte(data), &container); err == nil && len(container.PlaySessionStateNotification) > 0 {
		var direct playSessionStateNotification
		if err := json.Unmarshal(container.PlaySessionStateNotification, &direct); err == nil {
			m.handlePlaySessionNotification(ctx, direct)
			return
		}
		var list []playSessionStateNotification
		if err := json.Unmarshal(container.PlaySessionStateNotification, &list); err == nil {
			for _, n := range list {
				m.handlePlaySessionNotification(ctx, n)
			}
		}
	}
	_ = eventName
}

func (m *Module) handlePlaySessionNotification(ctx context.Context, n playSessionStateNotification) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch strings.ToLower(strings.TrimSpace(n.State)) {
	case "stopped":
		key := strings.TrimSpace(n.SessionKey)
		if key == "" {
			return
		}
		m.mu.Lock()
		delete(m.sessionSeen, key)
		m.mu.Unlock()
		m.publishPlayback(ctx, playbackevents.EventPlaybackStopped, playbackEventPayload{
			SessionID:  key,
			ServerType: "plex",
		})
		return
	case "playing", "paused", "buffering":
		m.syncSessionFromPlex(ctx, n)
	}
}

func (m *Module) syncSessionFromPlex(ctx context.Context, hint playSessionStateNotification) {
	sessions, err := m.listSessions(ctx)
	if err != nil {
		return
	}
	for _, s := range sessions {
		if hint.SessionKey != "" && s.SessionKey != hint.SessionKey {
			continue
		}
		if hint.RatingKey != "" && s.RatingKey != hint.RatingKey {
			continue
		}
		key := s.SessionKey
		if key == "" {
			key = s.User.ID.String() + ":" + s.RatingKey
		}
		ev := sessionToEvent(s)
		eventType := playbackevents.EventPlaybackProgress
		m.mu.Lock()
		prev := m.sessionSeen[key]
		if prev == "" {
			eventType = playbackevents.EventPlaybackStarted
		}
		m.sessionSeen[key] = strings.ToLower(strings.TrimSpace(hint.State))
		if m.sessionSeen[key] == "" {
			m.sessionSeen[key] = "playing"
		}
		m.mu.Unlock()
		m.publishPlayback(ctx, eventType, ev)
		return
	}
}

func (m *Module) sseEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sseEnabledFlag
}

func (m *Module) setSSEConnected(v bool) {
	m.sseMu.Lock()
	m.sseConnected = v
	m.sseMu.Unlock()
}

func (m *Module) sseConnectedNow() bool {
	m.sseMu.RLock()
	defer m.sseMu.Unlock()
	return m.sseConnected
}

func envSSEEnabled(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
