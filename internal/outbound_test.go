package internal

import (
	"errors"
	"testing"

	"github.com/Muxcore-Media/core/sdk/go/module/netguard"
)

func TestGuardOutboundURL(t *testing.T) {
	if err := guardOutboundURL(""); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"http://127.0.0.1:32400/identity",
		"http://192.168.1.20:32400/status/sessions",
		"http://plex:32400/library/sections",
		"https://plex.tv/api/v2/user",
	} {
		if err := guardOutboundURL(raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/",
		"file:///etc/passwd",
	} {
		if err := guardOutboundURL(raw); !errors.Is(err, netguard.ErrBlocked) {
			t.Fatalf("%s: err=%v", raw, err)
		}
	}
}

func TestUpdateSettingRejectsMetadataBaseURL(t *testing.T) {
	m := NewModule(Config{DataDir: t.TempDir()})
	err := m.updateSetting("plex_url", "http://169.254.169.254")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("err=%v", err)
	}
	if m.baseURL != "" {
		t.Fatalf("stored %q", m.baseURL)
	}
	if err := m.updateSetting("plex_url", "http://127.0.0.1:32400"); err != nil {
		t.Fatal(err)
	}
}
