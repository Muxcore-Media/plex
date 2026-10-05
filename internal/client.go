package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type plexGETHeaders struct {
	ContainerStart int
	ContainerSize  int
}

func (m *Module) plexGET(ctx context.Context, path string, headers *plexGETHeaders) ([]byte, int, error) {
	m.mu.RLock()
	base, token := m.baseURL, m.token
	m.mu.RUnlock()
	if base == "" || token == "" {
		return nil, 0, fmt.Errorf("plex not configured")
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
	if headers != nil {
		if headers.ContainerSize > 0 {
			req.Header.Set("X-Plex-Container-Size", strconv.Itoa(headers.ContainerSize))
		}
		if headers.ContainerStart > 0 {
			req.Header.Set("X-Plex-Container-Start", strconv.Itoa(headers.ContainerStart))
		}
	}
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

func (m *Module) probeIdentity(ctx context.Context) error {
	body, code, err := m.plexGET(ctx, "/identity", nil)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("plex /identity status %d", code)
	}
	var resp struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("plex /identity parse: %w", err)
	}
	id := strings.TrimSpace(resp.MediaContainer.MachineIdentifier)
	if id != "" {
		m.mu.Lock()
		m.machineID = id
		m.mu.Unlock()
	}
	return nil
}

type plexSession struct { //nolint:govet // field order matches Plex API JSON grouping
	SessionKey       string         `json:"sessionKey"`
	RatingKey        string         `json:"ratingKey"`
	Title            string         `json:"title"`
	Type             string         `json:"type"`
	ViewOffset       int64          `json:"viewOffset"`
	Duration         int64          `json:"duration"`
	GrandparentTitle string         `json:"grandparentTitle"`
	ParentTitle      string         `json:"parentTitle"`
	User             plexUser       `json:"User"`
	Player           plexPlayer     `json:"Player"`
	TranscodeSession map[string]any `json:"TranscodeSession"`
	Width            int            `json:"width"`
	Height           int            `json:"height"`
}

type plexUser struct {
	ID    json.Number `json:"id"`
	Title string      `json:"title"`
}

type plexPlayer struct {
	Title    string `json:"title"`
	Address  string `json:"address"`
	Platform string `json:"platform"`
	State    string `json:"state"`
}

func (m *Module) listSessions(ctx context.Context) ([]plexSession, error) {
	body, code, err := m.plexGET(ctx, "/status/sessions", nil)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, fmt.Errorf("plex /status/sessions status %d", code)
	}
	var resp struct {
		MediaContainer struct {
			Metadata []plexSession `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("plex sessions parse: %w", err)
	}
	return resp.MediaContainer.Metadata, nil
}

func displayTitle(s plexSession) string {
	if s.GrandparentTitle != "" {
		if s.ParentTitle != "" {
			return s.GrandparentTitle + " — " + s.ParentTitle + " — " + s.Title
		}
		return s.GrandparentTitle + " — " + s.Title
	}
	return s.Title
}

func msToSeconds(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms / 1000
}

func sessionPaused(s plexSession) bool {
	state := strings.ToLower(strings.TrimSpace(s.Player.State))
	return state == "paused"
}

func (m *Module) sessionState(sessionKey string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionSeen[sessionKey]
}
