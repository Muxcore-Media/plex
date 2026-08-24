package internal

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func (m *Module) syncListsPollSec() int {
	if v := os.Getenv("PLEX_SYNC_LIST_POLL_SEC"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 3600
}

func (m *Module) syncListsLoop(ctx context.Context) {
	interval := time.Duration(m.syncListsPollSec()) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	run := func() {
		if !m.configured() {
			return
		}
		if err := m.refreshSyncLists(ctx); err != nil {
			slog.Debug("plex: sync list refresh failed", "error", err)
			return
		}
		m.syncListsMu.RLock()
		n := len(m.syncListsCache.Lists)
		m.syncListsMu.RUnlock()
		if n > 0 {
			slog.Info("plex: sync lists refreshed", "lists", n)
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
