package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"codex-gate/internal/codexclient"
	"codex-gate/internal/redaction"
)

const (
	DefaultTimeout = 2 * time.Hour
	promptToken    = "{{prompt}}"
)

type Config struct {
	Command string
	Args    []string
	Env     []string
	Timeout time.Duration
	Logger  *log.Logger
}

type Client struct {
	command string
	args    []string
	env     []string
	timeout time.Duration
	logger  *log.Logger
}

func New(cfg Config) (*Client, error) {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return nil, errors.New("codex cli command is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, errors.New("codex cli timeout must be >= 0")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Client{
		command: command,
		args:    append([]string(nil), cfg.Args...),
		env:     append([]string(nil), cfg.Env...),
		timeout: timeout,
		logger:  logger,
	}, nil
}

func (c *Client) Create(
	ctx context.Context,
	payload map[string]any,
	_ codexclient.CallOptions,
) (*codexclient.CompletionResult, error) {
	prompt := buildPrompt(payload)
	if strings.TrimSpace(prompt) == "" {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindMalformedResponse,
			Retryable: false,
			Message:   "unable to build prompt for codex cli",
		}
	}

	output, err := c.run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(output)
	if text == "" {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindMalformedResponse,
			Retryable: false,
			Message:   "codex cli returned empty output",
		}
	}

	requestID := cliRequestID()
	c.logEvent(map[string]any{
		"event":      "codex_cli_response",
		"request_id": requestID,
	})
	return &codexclient.CompletionResult{
		RequestID:  requestID,
		HTTPStatus: 200,
		Response: map[string]any{
			"id":     "resp_" + requestID,
			"status": "completed",
			"output": []any{
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{
							"type": "output_text",
							"text": text,
						},
					},
				},
			},
		},
	}, nil
}

func (c *Client) Stream(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
) (*codexclient.StreamResult, error) {
	return c.StreamEvents(ctx, payload, options, nil)
}

func (c *Client) StreamEvents(
	ctx context.Context,
	payload map[string]any,
	_ codexclient.CallOptions,
	handler codexclient.StreamEventHandler,
) (*codexclient.StreamResult, error) {
	prompt := buildPrompt(payload)
	if strings.TrimSpace(prompt) == "" {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindMalformedResponse,
			Retryable: false,
			Message:   "unable to build prompt for codex cli",
		}
	}

	requestID := cliRequestID()
	responseID := "resp_" + requestID
	events := make([]map[string]any, 0, 4)
	started := false
	completed := false
	finalStatus := "completed"
	var streamErr error

	emit := func(event map[string]any) error {
		events = append(events, event)
		if handler == nil {
			return nil
		}
		return handler(event)
	}
	ensureStarted := func(nextResponseID string) error {
		if strings.TrimSpace(nextResponseID) != "" {
			responseID = nextResponseID
		}
		if started {
			return nil
		}
		started = true
		return emit(map[string]any{"event": "response.started", "response_id": responseID})
	}
	emitDelta := func(delta string) error {
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		if err := ensureStarted(""); err != nil {
			return err
		}
		return emit(map[string]any{"event": "response.output_text.delta", "delta": delta})
	}

	runErr := c.runStream(ctx, prompt, func(line string) error {
		mapped, err := mapCLIStreamLine(line, responseID)
		if err != nil {
			return err
		}
		if mapped.kind == cliStreamEventIgnored {
			return nil
		}
		switch mapped.kind {
		case cliStreamEventStarted:
			return ensureStarted(mapped.responseID)
		case cliStreamEventDelta:
			return emitDelta(mapped.delta)
		case cliStreamEventCompleted:
			if err := ensureStarted(mapped.responseID); err != nil {
				return err
			}
			completed = true
			finalStatus = "completed"
			return emit(map[string]any{
				"event":       "response.completed",
				"response_id": responseID,
				"status":      "completed",
			})
		case cliStreamEventFailed:
			if err := ensureStarted(mapped.responseID); err != nil {
				return err
			}
			finalStatus = "failed"
			message := firstNonEmpty(mapped.message, "codex cli stream failed")
			code := firstNonEmpty(mapped.code, "upstream_error")
			streamErr = &codexclient.ClientError{
				Kind:       codexclient.KindStreamFailed,
				StatusCode: 200,
				RequestID:  requestID,
				Retryable:  false,
				Message:    message,
			}
			return emit(map[string]any{
				"event":       "response.error",
				"response_id": responseID,
				"code":        code,
				"message":     message,
			})
		default:
			return nil
		}
	})
	if runErr != nil {
		if len(events) == 0 {
			return nil, runErr
		}
		finalStatus = "failed"
		return &codexclient.StreamResult{
			RequestID:   requestID,
			HTTPStatus:  200,
			Events:      events,
			FinalStatus: finalStatus,
		}, runErr
	}

	if !started {
		if err := ensureStarted(""); err != nil {
			return nil, err
		}
	}
	if !completed && streamErr == nil {
		completed = true
		if err := emit(map[string]any{
			"event":       "response.completed",
			"response_id": responseID,
			"status":      "completed",
		}); err != nil {
			return nil, err
		}
	}

	c.logEvent(map[string]any{
		"event":      "codex_cli_stream_response",
		"request_id": requestID,
		"status":     finalStatus,
	})
	return &codexclient.StreamResult{
		RequestID:   requestID,
		HTTPStatus:  200,
		Events:      events,
		FinalStatus: finalStatus,
	}, streamErr
}

func (c *Client) run(ctx context.Context, prompt string) (string, error) {
	runCtx := ctx
	cancel := func() {}
	if c.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	args, promptInArgs := expandPromptArgs(c.args, prompt)
	cmd := exec.CommandContext(runCtx, c.command, args...)
	if !promptInArgs {
		cmd.Stdin = strings.NewReader(prompt)
	}
	cmd.Env = append(os.Environ(), c.env...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if runCtx.Err() != nil {
		return "", &codexclient.ClientError{
			Kind:      codexclient.KindNetworkTimeout,
			Retryable: false,
			Message:   "codex cli command timed out",
			Cause:     runCtx.Err(),
		}
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 124 {
			return "", &codexclient.ClientError{
				Kind:      codexclient.KindNetworkTimeout,
				Retryable: false,
				Message:   "codex cli command timed out",
				Cause:     err,
			}
		}
		return "", &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: false,
			Message:   cliFailureMessage(err),
			Cause:     err,
		}
	}
	_ = stderr
	return stdout.String(), nil
}

func (c *Client) runStream(ctx context.Context, prompt string, onLine func(string) error) error {
	runCtx := ctx
	cancel := func() {}
	if c.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	args, promptInArgs := expandPromptArgs(c.args, prompt)
	cmd := exec.CommandContext(runCtx, c.command, args...)
	if !promptInArgs {
		cmd.Stdin = strings.NewReader(prompt)
	}
	cmd.Env = append(os.Environ(), c.env...)
	cmd.Env = append(cmd.Env, "CODEX_HARNESS_STREAM=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: false,
			Message:   "unable to open codex cli stdout pipe",
			Cause:     err,
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: false,
			Message:   cliFailureMessage(err),
			Cause:     err,
		}
	}

	var handlerErr error
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		if err := onLine(scanner.Text()); err != nil {
			handlerErr = err
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			break
		}
	}
	scanErr := scanner.Err()
	err = cmd.Wait()
	if handlerErr != nil {
		return handlerErr
	}
	if runCtx.Err() != nil {
		return &codexclient.ClientError{
			Kind:      codexclient.KindNetworkTimeout,
			Retryable: false,
			Message:   "codex cli command timed out",
			Cause:     runCtx.Err(),
		}
	}
	if scanErr != nil {
		return &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: false,
			Message:   "unable to read codex cli stream output",
			Cause:     scanErr,
		}
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 124 {
			return &codexclient.ClientError{
				Kind:      codexclient.KindNetworkTimeout,
				Retryable: false,
				Message:   "codex cli command timed out",
				Cause:     err,
			}
		}
		return &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: false,
			Message:   firstNonEmpty(redaction.RedactText(strings.TrimSpace(stderr.String())), cliFailureMessage(err)),
			Cause:     err,
		}
	}
	return nil
}

func expandPromptArgs(args []string, prompt string) ([]string, bool) {
	expanded := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if strings.Contains(arg, promptToken) {
			found = true
			expanded = append(expanded, strings.ReplaceAll(arg, promptToken, prompt))
			continue
		}
		expanded = append(expanded, arg)
	}
	return expanded, found
}

type cliStreamEventKind int

const (
	cliStreamEventIgnored cliStreamEventKind = iota
	cliStreamEventStarted
	cliStreamEventDelta
	cliStreamEventCompleted
	cliStreamEventFailed
)

type cliStreamEvent struct {
	kind       cliStreamEventKind
	responseID string
	delta      string
	code       string
	message    string
}

func mapCLIStreamLine(line string, fallbackResponseID string) (cliStreamEvent, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return cliStreamEvent{kind: cliStreamEventIgnored}, nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return cliStreamEvent{kind: cliStreamEventDelta, delta: line}, nil
	}

	eventType, _ := raw["type"].(string)
	switch eventType {
	case "thread.started":
		threadID, _ := raw["thread_id"].(string)
		return cliStreamEvent{
			kind:       cliStreamEventStarted,
			responseID: responseIDFromCLIID(threadID, fallbackResponseID),
		}, nil
	case "turn.started":
		return cliStreamEvent{kind: cliStreamEventStarted, responseID: fallbackResponseID}, nil
	case "item.completed":
		item, _ := raw["item"].(map[string]any)
		itemType, _ := item["type"].(string)
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) == "" {
			return cliStreamEvent{kind: cliStreamEventIgnored}, nil
		}
		switch itemType {
		case "agent_message", "assistant_message", "message":
			return cliStreamEvent{kind: cliStreamEventDelta, delta: text}, nil
		default:
			return cliStreamEvent{kind: cliStreamEventIgnored}, nil
		}
	case "turn.completed":
		return cliStreamEvent{kind: cliStreamEventCompleted, responseID: fallbackResponseID}, nil
	case "turn.failed", "turn.aborted":
		message, _ := raw["message"].(string)
		return cliStreamEvent{
			kind:       cliStreamEventFailed,
			responseID: fallbackResponseID,
			code:       "upstream_error",
			message:    message,
		}, nil
	case "error":
		// Codex CLI uses transient error events for reconnect attempts that can still finish successfully.
		return cliStreamEvent{kind: cliStreamEventIgnored}, nil
	default:
		return cliStreamEvent{kind: cliStreamEventIgnored}, nil
	}
}

func responseIDFromCLIID(raw string, fallback string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	if strings.HasPrefix(trimmed, "resp_") {
		return trimmed
	}
	return "resp_" + trimmed
}

func buildPrompt(payload map[string]any) string {
	var builder strings.Builder
	if instructions, ok := payload["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		builder.WriteString("System:\n")
		builder.WriteString(strings.TrimSpace(instructions))
		builder.WriteString("\n\n")
	}

	input, _ := payload["input"].([]any)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		role, _ := item["role"].(string)
		text := contentText(item["content"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		if strings.TrimSpace(role) != "" {
			builder.WriteString(strings.TrimSpace(role))
			builder.WriteString(":\n")
		}
		builder.WriteString(strings.TrimSpace(text))
		builder.WriteString("\n\n")
	}
	return strings.TrimSpace(builder.String())
}

func contentText(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, rawBlock := range value {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "input_text", "output_text":
				if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			case "tool_result":
				if text := anyText(block["content"]); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func anyText(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func completionText(response map[string]any) (string, error) {
	output, ok := response["output"].([]any)
	if !ok {
		return "", &codexclient.ClientError{
			Kind:      codexclient.KindMalformedResponse,
			Retryable: false,
			Message:   "codex cli completion missing output",
		}
	}
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if text := contentText(item["content"]); strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	return "", &codexclient.ClientError{
		Kind:      codexclient.KindMalformedResponse,
		Retryable: false,
		Message:   "codex cli completion missing output text",
	}
}

func cliRequestID() string {
	return fmt.Sprintf("codex_cli_%d", time.Now().UnixNano())
}

func cliFailureMessage(err error) string {
	if err == nil {
		return "codex cli command failed"
	}
	return "codex cli command failed: " + redaction.RedactText(err.Error())
}

func firstNonEmpty(values ...string) string {
	for _, item := range values {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func (c *Client) logEvent(event map[string]any) {
	c.logger.Print(redaction.ToJSON(event))
}
