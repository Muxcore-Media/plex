package internal

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func (m *Module) catalogSyncIntervalSec() int {
	if v := os.Getenv("PLEX_CATALOG_SYNC_SEC"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 6 * 3600
}

func (m *Module) catalogSyncLoop() {
	interval := time.Duration(m.catalogSyncIntervalSec()) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	run := func() {
		if !m.configured() {
			return
		}
		n, err := m.syncLibraryCatalog(context.Background())
		if err != nil {
			slog.Debug("plex: library catalog sync failed", "error", err)
			return
		}
		if n > 0 {
			slog.Info("plex: library catalog synced", "items", n)
		}
	}
	run()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			run()
		}
	}
}
