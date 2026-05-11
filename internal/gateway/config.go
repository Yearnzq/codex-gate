package gateway

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid gateway config")

type Config struct {
	Host          string
	Port          int
	LogLevel      string
	RedactLogs    bool
	AllowWideBind bool
	UpstreamModel string
	BackendMode   string
}

func DefaultConfig() Config {
	return Config{
		Host:          "127.0.0.1",
		Port:          8080,
		LogLevel:      "info",
		RedactLogs:    true,
		AllowWideBind: false,
		UpstreamModel: "",
	}
}

func IsLoopbackHost(host string) bool {
	normalized := strings.TrimSpace(host)
	if strings.EqualFold(normalized, "localhost") {
		return true
	}

	trimmed := strings.Trim(normalized, "[]")
	ip := net.ParseIP(trimmed)
	if ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func ParseConfigText(content string) (Config, error) {
	cfg := DefaultConfig()
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = trimInlineComment(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if section != "gateway" {
			continue
		}

		switch key {
		case "host":
			parsed, err := parseStringValue(value)
			if err != nil {
				return Config{}, wrapConfigErr("gateway.host must be a string")
			}
			cfg.Host = parsed
		case "port":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, wrapConfigErr("gateway.port must be an integer")
			}
			cfg.Port = parsed
		case "log_level":
			parsed, err := parseStringValue(value)
			if err != nil {
				return Config{}, wrapConfigErr("gateway.log_level must be a string")
			}
			cfg.LogLevel = parsed
		case "redact_logs":
			parsed, err := parseBoolValue(value)
			if err != nil {
				return Config{}, wrapConfigErr("gateway.redact_logs must be boolean")
			}
			cfg.RedactLogs = parsed
		case "allow_wide_bind":
			parsed, err := parseBoolValue(value)
			if err != nil {
				return Config{}, wrapConfigErr("gateway.allow_wide_bind must be boolean")
			}
			cfg.AllowWideBind = parsed
		case "upstream_model":
			parsed, err := parseStringValue(value)
			if err != nil {
				return Config{}, wrapConfigErr("gateway.upstream_model must be a string")
			}
			cfg.UpstreamModel = parsed
		}
	}

	if err := scanner.Err(); err != nil {
		return Config{}, wrapConfigErr(err.Error())
	}
	return cfg, ValidateConfig(cfg)
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if strings.TrimSpace(path) == "" {
		return cfg, ValidateConfig(cfg)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, ValidateConfig(cfg)
		}
		return Config{}, wrapConfigErr(err.Error())
	}

	return ParseConfigText(string(content))
}

func ValidateConfig(cfg Config) error {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return wrapConfigErr("gateway.host must be non-empty")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return wrapConfigErr("gateway.port must be between 1 and 65535")
	}
	if !isValidLogLevel(cfg.LogLevel) {
		return wrapConfigErr("gateway.log_level must be one of debug/info/warn/error")
	}
	if !IsLoopbackHost(host) && !cfg.AllowWideBind {
		return wrapConfigErr(
			fmt.Sprintf("gateway.host=%s requires gateway.allow_wide_bind=true", host),
		)
	}
	return nil
}

func trimInlineComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

func parseStringValue(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 2 || trimmed[0] != '"' || trimmed[len(trimmed)-1] != '"' {
		return "", errors.New("not a quoted string")
	}
	return strings.TrimSpace(trimmed[1 : len(trimmed)-1]), nil
}

func parseBoolValue(value string) (bool, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	switch trimmed {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("not a boolean")
	}
}

func isValidLogLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

func wrapConfigErr(msg string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, msg)
}
