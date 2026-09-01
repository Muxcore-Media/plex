package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
)

const moduleVersion = "0.1.0"

type Module struct { //nolint:govet // fieldalignment: lifecycle fields grouped for readability
	plexv1.UnimplementedPlexBridgeServiceServer

	grpcLis   net.Listener
	httpLis   net.Listener
	grpcSrv   *grpc.Server
	httpSrv   *http.Server
	runCancel context.CancelFunc

	sessionSeen map[string]string
	stopCh      chan struct{}
	meshDownCh  chan struct{}
	mc          *client.Client
	httpCli     *http.Client

	token           string
	baseURL         string
	httpAddr        string
	grpcAddr        string
	dataDir         string
	httpSecretVal   string
	machineID       string
	plexTVBaseURL   string
	id              string
	sessionsPollSec int
	lastActive      int

	syncListsMu    sync.RWMutex
	syncListsCache syncListsSnapshot

	sseMu           sync.RWMutex
	sseActivityMu   sync.Mutex
	sseLastActivity time.Time
	sseConnected    bool
	sseEnabledFlag  bool

	catalogSeenMu   sync.Mutex
	catalogSeenKeys map[string]struct{}

	mu sync.RWMutex
}

type Config struct { //nolint:govet // fieldalignment: config fields grouped for readability
	ID              string
	BaseURL         string
	Token           string
	DataDir         string
	HTTPSecret      string
	SessionsPollSec int
	GRPCAddr        string
	HTTPAddr        string
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "plex"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9476"
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8476"
	}
	if cfg.SessionsPollSec <= 0 {
		cfg.SessionsPollSec = 30
	}
	if v := os.Getenv("PLEX_GRPC_ADDR"); v != "" && cfg.GRPCAddr == "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("PLEX_HTTP_ADDR"); v != "" && cfg.HTTPAddr == "" {
		cfg.HTTPAddr = v
	}
	if cfg.DataDir == "" {
		if v := os.Getenv("PLEX_DATA_DIR"); v != "" {
			cfg.DataDir = v
		}
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/muxcore-plex"
	}
	if v := os.Getenv("PLEX_URL"); v != "" && cfg.BaseURL == "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("PLEX_TOKEN"); v != "" && cfg.Token == "" {
		cfg.Token = v
	}
	if v := os.Getenv("PLEX_HTTP_SECRET"); v != "" && cfg.HTTPSecret == "" {
		cfg.HTTPSecret = v
	}
	if cfg.SessionsPollSec == 30 {
		if v := os.Getenv("PLEX_SESSIONS_POLL_SEC"); v != "" {
			if n, err := parseInt(v); err == nil && n > 0 {
				cfg.SessionsPollSec = n
			}
		}
	}
	return &Module{
		id:              cfg.ID,
		baseURL:         trimSlash(cfg.BaseURL),
		token:           cfg.Token,
		dataDir:         cfg.DataDir,
		httpSecretVal:   cfg.HTTPSecret,
		sessionsPollSec: cfg.SessionsPollSec,
		grpcAddr:        cfg.GRPCAddr,
		httpAddr:        cfg.HTTPAddr,
		stopCh:          make(chan struct{}),
		meshDownCh:      make(chan struct{}, 1),
		sessionSeen:     map[string]string{},
		catalogSeenKeys: map[string]struct{}{},
		httpCli:         &http.Client{Timeout: 20 * time.Second},
		sseEnabledFlag:  envSSEEnabled(os.Getenv("PLEX_SSE")),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:          m.id,
		Name:        "Plex Playback Bridge",
		Version:     moduleVersion,
		Roles:       []string{"playback"},
		Description: "Plex session poll and playback mesh events",
		Author:      "MuxCore",
		Capabilities: []string{
			"playback.plex",
			"playback",
			"settings",
		},
		MinCoreVersion: "0.5.0",
		HTTPAddr:       m.httpAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	if err := os.MkdirAll(m.dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", m.dataDir, err)
	}
	if err := m.loadDurable(); err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", m.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = lis
	httpLis, err := lc.Listen(ctx, "tcp", m.httpAddr)
	if err != nil {
		_ = lis.Close()
		return fmt.Errorf("listen HTTP %s: %w", m.httpAddr, err)
	}
	m.httpLis = httpLis
	slog.Info("plex bridge initialized", "grpc", m.grpcAddr, "http", m.httpAddr, "data_dir", m.dataDir, "poll_sec", m.sessionsPollSec)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	runCtx, runCancel := context.WithCancel(context.WithoutCancel(ctx))
	m.runCancel = runCancel
	m.grpcSrv = grpc.NewServer()
	plexv1.RegisterPlexBridgeServiceServer(m.grpcSrv, m)
	modulesdk.RegisterSettings(m.grpcSrv, m.id, m)
	go func() {
		if err := m.grpcSrv.Serve(m.grpcLis); err != nil {
			slog.Error("plex gRPC error", "error", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", m.handleHealthz)
	mux.HandleFunc("GET /sync-lists", m.handleSyncListsHTTP)
	m.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := m.httpSrv.Serve(m.httpLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("plex HTTP error", "error", err)
		}
	}()

	go m.connectCore()
	go m.sseLoop(runCtx)
	go m.pollSessionsLoop(runCtx)
	go m.catalogSyncLoop(runCtx)
	go m.syncListsLoop(runCtx)
	return nil
}

func (m *Module) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := m.Health(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (m *Module) Stop(ctx context.Context) error {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	if m.runCancel != nil {
		m.runCancel()
	}
	if m.httpSrv != nil {
		_ = m.httpSrv.Shutdown(ctx)
	} else if m.httpLis != nil {
		_ = m.httpLis.Close()
	}
	m.invalidateMeshClient()
	slog.Info("plex bridge stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	if !m.configured() {
		return fmt.Errorf("plex not configured (PLEX_URL / PLEX_TOKEN)")
	}
	return m.probeIdentity(ctx)
}

func (m *Module) configured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.baseURL != "" && m.token != ""
}

func (m *Module) connectCore() {
	addr := os.Getenv("MUXCORE_GRPC_ADDR")
	if addr == "" {
		return
	}
	var opts []client.Option
	if os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true" {
		opts = append(opts, client.WithInsecure())
	}
	backoff := time.Second
	for {
		select {
		case <-m.stopCh:
			return
		default:
		}
		c, err := client.Dial(addr, opts...)
		if err != nil {
			select {
			case <-m.stopCh:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		m.mu.Lock()
		if m.mc != nil {
			_ = m.mc.Close()
		}
		m.mc = c
		m.mu.Unlock()
		slog.Info("plex: connected to core mesh", "addr", addr)
		backoff = time.Second

		select {
		case <-m.stopCh:
			return
		case <-m.meshDownCh:
			m.mu.Lock()
			if m.mc == c {
				_ = m.mc.Close()
				m.mc = nil
			}
			m.mu.Unlock()
			slog.Warn("plex: mesh client invalidated, reconnecting")
		}

		select {
		case <-m.stopCh:
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (m *Module) invalidateMeshClient() {
	m.mu.Lock()
	mc := m.mc
	m.mc = nil
	m.mu.Unlock()
	if mc != nil {
		_ = mc.Close()
	}
	select {
	case m.meshDownCh <- struct{}{}:
	default:
	}
}

func (m *Module) eventClient() *client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mc
}

var testPublishHook func(ctx context.Context, eventType, source string, payload []byte) error

func (m *Module) publishEvent(ctx context.Context, eventType string, payload []byte) error {
	if testPublishHook != nil {
		err := testPublishHook(ctx, eventType, m.id, payload)
		if err != nil {
			m.invalidateMeshClient()
		}
		return err
	}
	mc := m.eventClient()
	if mc == nil {
		return nil
	}
	err := mc.Events.Publish(ctx, eventType, m.id, payload)
	if err != nil {
		m.invalidateMeshClient()
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func trimSlash(s string) string {
	for len(s) > 1 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscan(s, &n)
	return n, err
}
