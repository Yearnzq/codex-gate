package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"codex-gate/internal/codexcli"
	"codex-gate/internal/codexclient"
	"codex-gate/internal/codexweb"
	"codex-gate/internal/credentials"
	"codex-gate/internal/gateway"
	"codex-gate/internal/redaction"
)

func main() {
	configPath := flag.String("config", ".codex/config.toml", "path to gateway config")
	credentialKey := flag.String("credential-key", "CODEX_API_KEY", "credential environment key")
	flag.Parse()

	logger := log.New(os.Stdout, "", 0)

	cfg, err := gateway.LoadConfig(*configPath)
	if err != nil {
		logger.Print(redaction.ToJSON(map[string]any{
			"event": "gateway_config_error",
			"error": redaction.RedactError(err),
		}))
		os.Exit(1)
	}
	if upstreamModel := strings.TrimSpace(os.Getenv("CODEX_UPSTREAM_MODEL")); upstreamModel != "" {
		cfg.UpstreamModel = upstreamModel
	}
	cfg.BackendMode = codexBackendModeFromEnv()

	codexService, err := buildCodexBackend(context.Background(), logger, *credentialKey)
	if err != nil {
		logger.Print(redaction.ToJSON(map[string]any{
			"event": "codex_client_init_error",
			"error": redaction.RedactError(err),
		}))
		os.Exit(1)
	}

	server, err := gateway.NewServerWithClient(cfg, logger, codexService)
	if err != nil {
		logger.Print(redaction.ToJSON(map[string]any{
			"event": "gateway_init_error",
			"error": redaction.RedactError(err),
		}))
		os.Exit(1)
	}

	done := make(chan error, 1)
	go func() {
		done <- server.Start()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-stop:
		logger.Print(redaction.ToJSON(map[string]any{
			"event":  "gateway_shutdown_signal",
			"signal": sig.String(),
		}))
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Print(redaction.ToJSON(map[string]any{
				"event": "gateway_runtime_error",
				"error": redaction.RedactError(err),
			}))
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Print(redaction.ToJSON(map[string]any{
			"event": "gateway_shutdown_error",
			"error": redaction.RedactError(err),
		}))
		os.Exit(1)
	}
}

func buildCodexBackend(ctx context.Context, logger *log.Logger, credentialKey string) (codexclient.Client, error) {
	switch codexBackendModeFromEnv() {
	case "", "api":
		return buildAPICodexBackend(ctx, logger, credentialKey)
	case "codex-web", "codex_web", "chatgpt-codex":
		return buildCodexWebBackend(logger)
	case "cli":
		return buildCLICodexBackend(logger)
	default:
		return nil, fmt.Errorf("unsupported CODEX_BACKEND %q", os.Getenv("CODEX_BACKEND"))
	}
}

func codexBackendModeFromEnv() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_BACKEND")))
}

func buildAPICodexBackend(
	ctx context.Context,
	logger *log.Logger,
	credentialKey string,
) (codexclient.Client, error) {
	loader := credentials.ChainLoader{
		Loaders: []credentials.Loader{
			credentials.NewEnvProvider(),
			&credentials.SecureStoreProvider{},
		},
	}

	apiKey := ""
	cred, credErr := loader.Load(ctx, credentialKey)
	if credErr != nil && credentialKey == "CODEX_API_KEY" {
		cred, credErr = loader.Load(ctx, "OPENAI_API_KEY")
	}
	if credErr != nil {
		// Credential may be absent in local dev; emit a safe, actionable warning.
		logger.Print(redaction.ToJSON(map[string]any{
			"event":   "credential_unavailable",
			"warning": redaction.RedactError(credErr),
			"action":  "set credential environment variable or provide secure store adapter",
		}))
	} else {
		logger.Print(redaction.ToJSON(map[string]any{
			"event":             "credential_loaded",
			"source":            cred.Source,
			"redaction_enabled": true,
		}))
		apiKey = cred.Value
	}

	codexBaseURL := strings.TrimSpace(os.Getenv("CODEX_BASE_URL"))
	return codexclient.New(codexclient.Config{
		BaseURL: codexBaseURL,
		APIKey:  apiKey,
		Logger:  logger,
	})
}

func buildCodexWebBackend(logger *log.Logger) (codexclient.Client, error) {
	accessToken := strings.TrimSpace(os.Getenv("CODEX_WEB_ACCESS_TOKEN"))
	if accessToken == "" {
		accessToken = strings.TrimSpace(os.Getenv("CODEX_ACCESS_TOKEN"))
	}
	if accessToken == "" {
		return nil, errors.New("CODEX_BACKEND=codex-web requires CODEX_WEB_ACCESS_TOKEN or CODEX_ACCESS_TOKEN")
	}
	streamStartRetries, err := parseNonNegativeEnvInt("CODEX_WEB_STREAM_START_RETRIES", codexweb.DefaultStreamStartRetries)
	if err != nil {
		return nil, err
	}
	streamResume, err := parseBoolEnv("CODEX_WEB_STREAM_RESUME")
	if err != nil {
		return nil, err
	}
	streamResumeRetries, err := parseNonNegativeEnvInt("CODEX_WEB_STREAM_RESUME_RETRIES", 1)
	if err != nil {
		return nil, err
	}
	webTimeout, webTimeoutDisabled, err := parseCodexWebTimeout()
	if err != nil {
		return nil, err
	}
	client, err := codexweb.New(codexweb.Config{
		BaseURL:             strings.TrimSpace(os.Getenv("CODEX_WEB_BASE_URL")),
		AccessToken:         accessToken,
		AccountID:           strings.TrimSpace(os.Getenv("CODEX_CHATGPT_ACCOUNT_ID")),
		Logger:              logger,
		Timeout:             webTimeout,
		TimeoutDisabled:     webTimeoutDisabled,
		ReasoningEffort:     strings.TrimSpace(os.Getenv("CODEX_WEB_REASONING_EFFORT")),
		ServiceTier:         strings.TrimSpace(os.Getenv("CODEX_WEB_SERVICE_TIER")),
		StreamStartRetries:  &streamStartRetries,
		StreamResume:        streamResume,
		StreamResumeRetries: &streamResumeRetries,
	})
	if err != nil {
		return nil, err
	}
	logger.Print(redaction.ToJSON(map[string]any{
		"event":                 "codex_web_backend_enabled",
		"base_url":              firstNonEmpty(strings.TrimSpace(os.Getenv("CODEX_WEB_BASE_URL")), codexweb.DefaultBaseURL),
		"account_id_set":        strings.TrimSpace(os.Getenv("CODEX_CHATGPT_ACCOUNT_ID")) != "",
		"stream_start_retries":  streamStartRetries,
		"stream_resume_enabled": streamResume,
		"stream_resume_retries": streamResumeRetries,
		"timeout_seconds":       timeoutSecondsForLog(webTimeout, webTimeoutDisabled),
		"redaction_enabled":     true,
	}))
	return client, nil
}

func parseCodexWebTimeout() (time.Duration, bool, error) {
	raw := strings.TrimSpace(os.Getenv("CODEX_WEB_TIMEOUT"))
	if raw != "" {
		return parseDurationOrSeconds("CODEX_WEB_TIMEOUT", raw)
	}
	raw = strings.TrimSpace(os.Getenv("CODEX_WEB_TIMEOUT_SECONDS"))
	if raw == "" {
		return 0, false, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0, false, fmt.Errorf("CODEX_WEB_TIMEOUT_SECONDS must be a non-negative integer")
	}
	if seconds == 0 {
		return 0, true, nil
	}
	return time.Duration(seconds) * time.Second, false, nil
}

func parseDurationOrSeconds(name string, raw string) (time.Duration, bool, error) {
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			return 0, false, fmt.Errorf("%s must be a non-negative duration or seconds value", name)
		}
		if seconds == 0 {
			return 0, true, nil
		}
		return time.Duration(seconds) * time.Second, false, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout < 0 {
		return 0, false, fmt.Errorf("%s must be a non-negative duration such as 60s or seconds value", name)
	}
	if timeout == 0 {
		return 0, true, nil
	}
	return timeout, false, nil
}

func timeoutSecondsForLog(timeout time.Duration, disabled bool) int64 {
	if disabled {
		return 0
	}
	if timeout == 0 {
		return int64(codexweb.DefaultTimeout / time.Second)
	}
	return int64(timeout / time.Second)
}

func buildCLICodexBackend(logger *log.Logger) (codexclient.Client, error) {
	command := strings.TrimSpace(os.Getenv("CODEX_CLI_COMMAND"))
	if command == "" {
		command = strings.TrimSpace(os.Getenv("CODEX_CLI_PATH"))
	}
	args, err := parseCLIArgsJSON(os.Getenv("CODEX_CLI_ARGS_JSON"))
	if err != nil {
		return nil, err
	}
	timeout, err := parseCLITimeout(os.Getenv("CODEX_CLI_TIMEOUT"))
	if err != nil {
		return nil, err
	}
	client, err := codexcli.New(codexcli.Config{
		Command: command,
		Args:    args,
		Timeout: timeout,
		Logger:  logger,
	})
	if err != nil {
		return nil, err
	}
	logger.Print(redaction.ToJSON(map[string]any{
		"event":   "codex_cli_backend_enabled",
		"command": redaction.RedactText(command),
	}))
	return client, nil
}

func parseNonNegativeEnvInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return value, nil
}

func parseBoolEnv(name string) (bool, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be one of: 1, 0, true, false, yes, no, on, off", name)
	}
}

func parseCLIArgsJSON(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return nil, fmt.Errorf("CODEX_CLI_ARGS_JSON must be a JSON string array: %w", err)
	}
	return args, nil
}

func parseCLITimeout(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("CODEX_CLI_TIMEOUT must be a duration such as 60s: %w", err)
	}
	return timeout, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
