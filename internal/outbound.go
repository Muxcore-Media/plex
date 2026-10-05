package internal

import (
	"net/http"
	"strings"
	"time"

	"github.com/Muxcore-Media/core/sdk/go/module/netguard"
)

// plexGuardOptions is the Integration profile for the operator's Plex server
// and plex.tv. A household server lives on the LAN or loopback. Link-local,
// cloud metadata, and non-HTTP schemes stay refused.
func plexGuardOptions(timeout time.Duration) netguard.Options {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return netguard.Options{
		AllowPrivate:  true,
		AllowLoopback: true,
		Timeout:       timeout,
	}
}

func newGuardedClient(timeout time.Duration) *http.Client {
	return netguard.NewClient(netguard.Integration, plexGuardOptions(timeout))
}

// guardOutboundURL checks raw before a request. An empty URL is left to the
// caller (unset configuration).
func guardOutboundURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return netguard.ValidateURL(raw, netguard.Integration, plexGuardOptions(20*time.Second))
}
