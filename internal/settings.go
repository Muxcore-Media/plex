package internal

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
)

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
		m.mu.Lock()
		m.baseURL = trimSlash(value)
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
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	return nil
}

func sseSettingValue(m *Module) string {
	if m.sseEnabled() {
		return "enabled"
	}
	return "disabled"
}

func (m *Module) Status(ctx context.Context, _ *plexv1.StatusRequest) (*plexv1.StatusResponse, error) {
	m.mu.RLock()
	base := m.baseURL
	active := int32(m.lastActive) //nolint:gosec // active session count from Plex fits int32 status field
	m.mu.RUnlock()
	return &plexv1.StatusResponse{
		Configured:     m.configured(),
		BaseUrl:        base,
		ActiveSessions: active,
	}, nil
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
	_, code, err := m.plexGET(ctx, path)
	if err != nil {
		return &plexv1.TerminateSessionResponse{Ok: false, Error: err.Error()}, nil //nolint:nilerr // application-level failure encoded in response
	}
	if code >= 300 {
		return &plexv1.TerminateSessionResponse{Ok: false, Error: fmt.Sprintf("plex terminate status %d", code)}, nil
	}
	return &plexv1.TerminateSessionResponse{Ok: true}, nil
}
