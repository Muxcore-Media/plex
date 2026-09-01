package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type durableSettings struct {
	BaseURL         string `json:"base_url"`
	Token           string `json:"token"`
	SessionsPollSec int    `json:"sessions_poll_seconds"`
	SSEEnabled      *bool  `json:"plex_sse_enabled,omitempty"`
}

func (m *Module) settingsPath() string {
	return filepath.Join(m.dataDir, "settings.json")
}

func (m *Module) loadDurable() error {
	path := m.settingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m.persistDurable()
		}
		return err
	}
	var s durableSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseURL == "" && s.BaseURL != "" {
		m.baseURL = trimSlash(s.BaseURL)
	}
	if m.token == "" && s.Token != "" {
		m.token = s.Token
	}
	if s.SessionsPollSec > 0 {
		m.sessionsPollSec = s.SessionsPollSec
	}
	if s.SSEEnabled != nil {
		m.sseEnabledFlag = *s.SSEEnabled
	}
	return nil
}

func (m *Module) persistDurable() error {
	m.mu.RLock()
	sse := m.sseEnabledFlag
	s := durableSettings{
		BaseURL:         m.baseURL,
		Token:           m.token,
		SessionsPollSec: m.sessionsPollSec,
		SSEEnabled:      &sse,
	}
	m.mu.RUnlock()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := m.settingsPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
