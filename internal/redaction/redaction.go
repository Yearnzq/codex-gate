package redaction

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	RedactedValue = "[REDACTED]"
	RedactedBody  = "[REDACTED_BODY]"
)

var sensitiveHeaderKeys = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"api-key":             {},
}

var sensitiveFieldTokens = []string{
	"authorization",
	"cookie",
	"token",
	"api_key",
	"apikey",
	"password",
	"secret",
	"credential",
}

var (
	bearerPattern            = regexp.MustCompile(`(?i)bearer\s+[^\s,;]+`)
	keyValueRegex            = regexp.MustCompile(`(?i)\b(api[_-]?key|token|password|secret)\b\s*([=:])\s*([^\s,;]+)`)
	standaloneSecretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`ghp_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(
			`(?i)` + `-----BEGIN ` + `(?:RSA |OPENSSH |EC |DSA )?` + `PRIVATE` + ` KEY-----`,
		),
	}
)

func isSensitiveField(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, token := range sensitiveFieldTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func RedactHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		if _, sensitive := sensitiveHeaderKeys[strings.ToLower(strings.TrimSpace(key))]; sensitive {
			result[key] = RedactedValue
			continue
		}
		result[key] = RedactText(value)
	}
	return result
}

func RedactText(text string) string {
	line := bearerPattern.ReplaceAllString(text, "Bearer "+RedactedValue)
	line = keyValueRegex.ReplaceAllString(line, "$1$2"+RedactedValue)
	for _, pattern := range standaloneSecretPatterns {
		line = pattern.ReplaceAllString(line, RedactedValue)
	}
	return line
}

func RedactBody(raw []byte) string {
	if len(raw) == 0 {
		return RedactedBody
	}
	return RedactedBody
}

func RedactError(err error) string {
	if err == nil {
		return ""
	}
	return RedactText(err.Error())
}

func RedactStructured(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, val := range typed {
			if isSensitiveField(key) {
				out[key] = RedactedValue
				continue
			}
			out[key] = RedactStructured(val)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, val := range typed {
			if isSensitiveField(key) {
				out[key] = RedactedValue
				continue
			}
			out[key] = RedactText(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, RedactStructured(item))
		}
		return out
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, RedactText(item))
		}
		return out
	case string:
		return RedactText(typed)
	default:
		return typed
	}
}

func ToJSON(value any) string {
	safe := RedactStructured(value)
	encoded, err := json.Marshal(safe)
	if err != nil {
		return `{"event":"log_encode_failure","error":"` + RedactText(err.Error()) + `"}`
	}
	return string(encoded)
}
