package internal

import (
	"context"
	"encoding/json"
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

type Module struct {
	plexv1.UnimplementedPlexBridgeServiceServer

	mu    sync.RWMutex
	cfgMu sync.RWMutex

	id              string
	baseURL         string
	token           string
	sessionsPollSec int
	grpcAddr        string
	httpAddr        string

	grpcSrv *grpc.Server
	grpcLis net.Listener
	httpLis net.Listener

	httpCli *http.Client
	mc      *client.Client
	stopCh  chan struct{}

	sessionSeen map[string]string
	lastActive  int

	machineID     string
	plexTVBaseURL string
	syncListsMu   sync.RWMutex
	syncListsCache syncListsSnapshot

	sseEnabledFlag bool
	sseMu          sync.RWMutex
	sseConnected   bool
}

type Config struct {
	ID              string
	BaseURL         string
	Token           string
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
	if v := os.Getenv("PLEX_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("PLEX_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("PLEX_SESSIONS_POLL_SEC"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			cfg.SessionsPollSec = n
		}
	}
	if v := os.Getenv("PLEX_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("PLEX_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	return &Module{
		id:              cfg.ID,
		baseURL:         trimSlash(cfg.BaseURL),
		token:           cfg.Token,
		sessionsPollSec: cfg.SessionsPollSec,
		grpcAddr:        cfg.GRPCAddr,
		httpAddr:        cfg.HTTPAddr,
		stopCh:          make(chan struct{}),
		sessionSeen:     map[string]string{},
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
	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = lis
	httpLis, err := net.Listen("tcp", m.httpAddr)
	if err != nil {
		lis.Close()
		return fmt.Errorf("listen HTTP %s: %w", m.httpAddr, err)
	}
	m.httpLis = httpLis
	slog.Info("plex bridge initialized", "grpc", m.grpcAddr, "http", m.httpAddr, "poll_sec", m.sessionsPollSec)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	plexv1.RegisterPlexBridgeServiceServer(m.grpcSrv, m)
	modulesdk.RegisterSettings(m.grpcSrv, m.id, m)
	go func() {
		if err := m.grpcSrv.Serve(m.grpcLis); err != nil {
			slog.Error("plex gRPC error", "error", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /sync-lists", m.handleSyncListsHTTP)
	go func() {
		if err := http.Serve(m.httpLis, mux); err != nil && err != http.ErrServerClosed {
			slog.Error("plex HTTP error", "error", err)
		}
	}()

	go m.connectCore()
	go m.sseLoop()
	go m.pollSessionsLoop()
	go m.catalogSyncLoop()
	go m.syncListsLoop()
	return nil
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
	if m.httpLis != nil {
		_ = m.httpLis.Close()
	}
	m.mu.Lock()
	mc := m.mc
	m.mc = nil
	m.mu.Unlock()
	if mc != nil {
		mc.Close()
	}
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
			m.mc.Close()
		}
		m.mc = c
		m.mu.Unlock()
		slog.Info("plex: connected to core mesh", "addr", addr)
		return
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
		return testPublishHook(ctx, eventType, m.id, payload)
	}
	mc := m.eventClient()
	if mc == nil {
		return nil
	}
	return mc.Events.Publish(ctx, eventType, m.id, payload)
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
