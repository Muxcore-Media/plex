package internal

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
)

const headerPlexBridgeSecret = "X-Plex-Bridge-Secret" //nolint:gosec // HTTP header name, not a credential

func (m *Module) Settings() []contracts.SettingDef {
	return m.settingsDefs()
}

func (m *Module) UpdateSetting(key, value string) error {
	return m.updateSetting(key, value)
}

func (m *Module) settingsDefs() []contracts.SettingDef {
	m.mu.RLock()
	baseURL := m.baseURL
	token := m.token
	poll := m.sessionsPollSec
	m.mu.RUnlock()
	masked := ""
	if token != "" {
		masked = "••••"
	}
	return []contracts.SettingDef{
		{
			Key:         "plex_url",
			Label:       "Plex Server URL",
			Type:        contracts.SettingTypeString,
			Value:       baseURL,
			Description: "Plex Media Server base URL (PLEX_URL)",
			Required:    true,
			Group:       "Connection",
		},
		{
			Key:         "plex_token",
			Label:       "Plex Token",
			Type:        contracts.SettingTypeString,
			Value:       masked,
			Description: "X-Plex-Token (PLEX_TOKEN)",
			Required:    true,
			Group:       "Connection",
		},
		{
			Key:         "sessions_poll_seconds",
			Label:       "Sessions Poll Interval",
			Type:        contracts.SettingTypeString,
			Value:       fmt.Sprintf("%d", poll),
			Description: "Poll /status/sessions interval in seconds (fallback when SSE disconnected)",
			Required:    false,
			Group:       "Playback",
		},
		{
			Key:         "plex_sse",
			Label:       "Plex SSE",
			Type:        contracts.SettingTypeString,
			Value:       sseSettingValue(m),
			Description: "Connect to /:/eventsource/notifications (PLEX_SSE, default on)",
			Required:    false,
			Group:       "Playback",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	switch key {
	case "plex_url", "PLEX_URL":
		next := trimSlash(value)
		if err := guardOutboundURL(next); err != nil {
			return err
		}
		m.mu.Lock()
		m.baseURL = next
		m.mu.Unlock()
	case "plex_token", "PLEX_TOKEN":
		if value != "" && value != "••••" {
			m.mu.Lock()
			m.token = value
			m.mu.Unlock()
		}
	case "sessions_poll_seconds", "PLEX_SESSIONS_POLL_SEC":
		if n, err := parseInt(value); err == nil && n > 0 {
			m.mu.Lock()
			m.sessionsPollSec = n
			m.mu.Unlock()
		}
	case "plex_sse", "PLEX_SSE":
		enabled := parseSSESetting(value)
		m.mu.Lock()
		m.sseEnabledFlag = enabled
		m.mu.Unlock()
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	return m.persistDurable()
}

func parseSSESetting(value string) bool {
	v := strings.TrimSpace(strings.ToLower(value))
	switch v {
	case "0", "false", "no", "off", "disabled":
		return false
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return envSSEEnabled(value)
	}
}

func sseSettingValue(m *Module) string {
	if m.sseEnabled() {
		return "enabled"
	}
	return "disabled"
}

func (m *Module) httpSecret() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.httpSecretVal != "" {
		return m.httpSecretVal
	}
	return os.Getenv("PLEX_HTTP_SECRET")
}

func (m *Module) checkHTTPSecret(provided string) bool {
	secret := m.httpSecret()
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 1
}

func (m *Module) Status(ctx context.Context, _ *plexv1.StatusRequest) (*plexv1.StatusResponse, error) {
	m.mu.RLock()
	base := m.baseURL
	active := int32(m.lastActive) //nolint:gosec // active session count from Plex fits int32 status field
	machineID := m.machineID
	m.mu.RUnlock()
	if machineID == "" && m.configured() {
		if id, err := m.machineIdentifier(ctx); err == nil {
			machineID = id
		}
	}
	return &plexv1.StatusResponse{
		Configured:     m.configured(),
		BaseUrl:        base,
		ActiveSessions: active,
		SseConnected:   m.sseConnectedNow(),
		MachineId:      machineID,
	}, nil
}

func (m *Module) ListSessions(ctx context.Context, _ *plexv1.ListSessionsRequest) (*plexv1.ListSessionsResponse, error) {
	if !m.configured() {
		return &plexv1.ListSessionsResponse{}, nil
	}
	sessions, err := m.listSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := &plexv1.ListSessionsResponse{}
	for _, s := range sessions {
		sessionID := s.SessionKey
		if sessionID == "" {
			sessionID = s.User.ID.String() + ":" + s.RatingKey
		}
		paused := sessionPaused(s)
		if state := strings.ToLower(strings.TrimSpace(m.sessionState(s.SessionKey))); state == "paused" {
			paused = true
		}
		out.Sessions = append(out.Sessions, &plexv1.PlexSessionMessage{
			SessionId:       sessionID,
			UserId:          s.User.ID.String(),
			UserName:        s.User.Title,
			ItemId:          s.RatingKey,
			Title:           displayTitle(s),
			PositionSeconds: msToSeconds(s.ViewOffset),
			DurationSeconds: msToSeconds(s.Duration),
			Paused:          paused,
			Device:          firstNonEmpty(s.Player.Title, s.Player.Platform),
		})
	}
	return out, nil
}

func (m *Module) PlayURL(_ context.Context, req *plexv1.PlayURLRequest) (*plexv1.PlayURLResponse, error) {
	m.mu.RLock()
	base := m.baseURL
	machineID := m.machineID
	m.mu.RUnlock()
	ratingKey := strings.TrimSpace(req.GetRatingKey())
	if base == "" || ratingKey == "" {
		return nil, fmt.Errorf("base_url and rating_key required")
	}
	if machineID == "" {
		return nil, fmt.Errorf("machine_id unknown; probe /identity first")
	}
	u := fmt.Sprintf("%s/web/index.html#!/server/%s/details?key=/library/metadata/%s", base, machineID, ratingKey)
	return &plexv1.PlayURLResponse{Url: u}, nil
}

func (m *Module) TerminateSession(ctx context.Context, req *plexv1.TerminateSessionRequest) (*plexv1.TerminateSessionResponse, error) {
	if req.GetSessionId() == "" {
		return &plexv1.TerminateSessionResponse{Ok: false, Error: "session_id required"}, nil
	}
	q := url.Values{}
	q.Set("sessionKey", req.GetSessionId())
	if r := strings.TrimSpace(req.GetReason()); r != "" {
		q.Set("reason", r)
	}
	path := "/status/sessions/terminate?" + q.Encode()
	_, code, err := m.plexGET(ctx, path, nil)
	if err != nil {
		return &plexv1.TerminateSessionResponse{Ok: false, Error: err.Error()}, nil //nolint:nilerr // application-level failure encoded in response
	}
	if code >= 300 {
		return &plexv1.TerminateSessionResponse{Ok: false, Error: fmt.Sprintf("plex terminate status %d", code)}, nil
	}
	return &plexv1.TerminateSessionResponse{Ok: true}, nil
}
