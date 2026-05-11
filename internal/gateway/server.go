package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"codex-gate/internal/codexclient"
	"codex-gate/internal/redaction"
)

const (
	ServiceName = "codex-gate"
)

var (
	ServiceVersion        = "0.6.0-phase6"
	BuildTime             = "unknown"
	BuildCommit           = "unknown"
	BuildTarget           = ""
	ProtocolCompatibility = "anthropic-messages-v1/openai-responses-v1"
)

type Server struct {
	httpServer  *http.Server
	logger      *log.Logger
	config      Config
	codexClient codexclient.Client
}

func NewServer(cfg Config, logger *log.Logger) (*Server, error) {
	return NewServerWithClient(cfg, logger, nil)
}

func NewServerWithClient(cfg Config, logger *log.Logger, client codexclient.Client) (*Server, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.Default()
	}

	s := &Server{
		logger:      logger,
		config:      cfg,
		codexClient: client,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthzHandler)
	mux.HandleFunc("/version", s.versionHandler)
	mux.HandleFunc("/v1/messages", s.messagesHandler)
	mux.HandleFunc("/v1/messages/count_tokens", s.countTokensHandler)
	mux.HandleFunc("/v1/chat/completions", s.chatCompletionsHandler)

	s.httpServer = &http.Server{
		Addr:        cfg.Host + ":" + strconv.Itoa(cfg.Port),
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
		// LLM calls and SSE streams can run longer than a fixed write deadline.
		WriteTimeout: 0,
		IdleTimeout:  30 * time.Second,
	}

	return s, nil
}

func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

func (s *Server) Serve(listener net.Listener) error {
	addr, _ := net.ResolveTCPAddr("tcp", listener.Addr().String())
	host := s.config.Host
	port := s.config.Port
	if addr != nil {
		host = addr.IP.String()
		port = addr.Port
	}

	s.logger.Print(
		redaction.ToJSON(map[string]any{
			"event":             "gateway_start",
			"name":              ServiceName,
			"version":           ServiceVersion,
			"build_time":        BuildTime,
			"target_platform":   targetPlatform(),
			"host":              host,
			"port":              port,
			"redaction_enabled": s.config.RedactLogs,
		}),
	)
	err := s.httpServer.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) versionHandler(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(
		w,
		r,
		http.StatusOK,
		map[string]any{
			"name":                   ServiceName,
			"version":                ServiceVersion,
			"build_time":             BuildTime,
			"git_version":            BuildCommit,
			"target_platform":        targetPlatform(),
			"active_backend":         "codex",
			"backend_mode":           backendModeForVersion(s.config.BackendMode),
			"protocol_compatibility": ProtocolCompatibility,
		},
	)
}

func targetPlatform() string {
	if BuildTarget != "" {
		return BuildTarget
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

func backendModeForVersion(mode string) string {
	if mode != "" {
		return mode
	}
	return "api"
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, code int, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		safeError := redaction.RedactError(err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		s.logger.Print(
			redaction.ToJSON(map[string]any{
				"event": "response_encode_error",
				"error": safeError,
			}),
		)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(body)

	headers := make(map[string]string, len(r.Header))
	for key, values := range r.Header {
		if len(values) == 0 {
			headers[key] = ""
			continue
		}
		headers[key] = values[0]
	}
	if s.config.RedactLogs {
		headers = redaction.RedactHeaders(headers)
	}

	s.logger.Print(
		redaction.ToJSON(map[string]any{
			"event":             "http_request",
			"method":            r.Method,
			"path":              r.URL.Path,
			"status":            code,
			"headers":           headers,
			"request_body":      redaction.RedactBody(nil),
			"response_body":     redaction.RedactBody(body),
			"redaction_enabled": s.config.RedactLogs,
		}),
	)
}
