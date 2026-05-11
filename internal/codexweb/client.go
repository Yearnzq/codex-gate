package codexweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"codex-gate/internal/codexclient"
	"codex-gate/internal/redaction"
)

const (
	DefaultBaseURL            = "https://chatgpt.com/backend-api/codex"
	DefaultTimeout            = 6 * time.Hour
	DefaultStreamStartRetries = 1
)

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	BaseURL             string
	AccessToken         string
	AccountID           string
	HTTPClient          HTTPDoer
	Logger              *log.Logger
	Timeout             time.Duration
	ReasoningEffort     string
	ServiceTier         string
	StreamStartRetries  *int
	StreamResume        bool
	StreamResumeRetries *int
	TimeoutDisabled     bool
}

type Client struct {
	baseURL             string
	accessToken         string
	accountID           string
	httpClient          HTTPDoer
	logger              *log.Logger
	reasoningEffort     string
	serviceTier         string
	streamStartRetries  int
	streamResume        bool
	streamResumeRetries int
}

func New(cfg Config) (*Client, error) {
	accessToken := strings.TrimSpace(cfg.AccessToken)
	if accessToken == "" {
		return nil, errors.New("codex web access token is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	accountID := strings.TrimSpace(cfg.AccountID)
	if accountID == "" {
		accountID = extractAccountIDFromJWT(accessToken)
	}
	timeout := cfg.Timeout
	if cfg.TimeoutDisabled {
		timeout = 0
	} else if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, errors.New("codex web timeout must be >= 0")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	streamStartRetries := DefaultStreamStartRetries
	if cfg.StreamStartRetries != nil {
		streamStartRetries = *cfg.StreamStartRetries
	}
	if streamStartRetries < 0 {
		return nil, errors.New("codex web stream start retries must be >= 0")
	}
	streamResumeRetries := 0
	if cfg.StreamResumeRetries != nil {
		streamResumeRetries = *cfg.StreamResumeRetries
	}
	if streamResumeRetries < 0 {
		return nil, errors.New("codex web stream resume retries must be >= 0")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Client{
		baseURL:             baseURL,
		accessToken:         accessToken,
		accountID:           accountID,
		httpClient:          httpClient,
		logger:              logger,
		reasoningEffort:     strings.TrimSpace(cfg.ReasoningEffort),
		serviceTier:         strings.TrimSpace(cfg.ServiceTier),
		streamStartRetries:  streamStartRetries,
		streamResume:        cfg.StreamResume,
		streamResumeRetries: streamResumeRetries,
	}, nil
}

func (c *Client) Create(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
) (*codexclient.CompletionResult, error) {
	streamResult, err := c.Stream(ctx, payload, options)
	if streamResult == nil {
		return nil, err
	}
	completion, convertErr := completionFromStream(streamResult)
	if convertErr != nil {
		return nil, convertErr
	}
	return completion, err
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
	body := c.normalizeRequestPayload(payload)
	body["stream"] = true
	body["store"] = false

	requestBytes, err := json.Marshal(body)
	if err != nil {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindMalformedResponse,
			Retryable: false,
			Message:   "unable to encode codex web request",
			Cause:     err,
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.streamStartRetries; attempt++ {
		result, err := c.streamEventsAttempt(ctx, requestBytes, handler)
		if err == nil || !c.canRetryBeforeFirstEvent(ctx, attempt, result, err) {
			return result, err
		}
		lastErr = err
		c.logger.Print(redaction.ToJSON(map[string]any{
			"event":       "codex_web_stream_start_retry",
			"attempt":     attempt + 1,
			"max_retries": c.streamStartRetries,
			"error":       redaction.RedactError(err),
		}))
	}
	return nil, lastErr
}

func (c *Client) streamEventsAttempt(
	ctx context.Context,
	requestBytes []byte,
	handler codexclient.StreamEventHandler,
) (*codexclient.StreamResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(requestBytes))
	if err != nil {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindRequestBuildFailed,
			Retryable: false,
			Message:   "unable to build codex web request",
			Cause:     err,
		}
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: true,
			Message:   "transport error contacting codex web endpoint",
			Cause:     err,
		}
	}
	defer resp.Body.Close()
	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = fmt.Sprintf("codex_web_%d", time.Now().UnixNano())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyStatus(resp.StatusCode, requestID, resp.Body)
	}

	events := make([]map[string]any, 0, 8)
	finalStatus := "completed"
	state := codexWebStreamState{lastSequenceNumber: -1}
	emit := func(event map[string]any) error {
		events = append(events, event)
		if handler == nil {
			return nil
		}
		return handler(event)
	}

	parseErr := readEventStream(resp.Body, func(raw map[string]any) error {
		state.record(raw)
		mapped, err := mapCodexWebEvent(raw, requestID)
		if err != nil {
			return err
		}
		for _, event := range mapped {
			if event["event"] == "response.error" {
				finalStatus = "failed"
			}
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	})
	if parseErr != nil {
		c.logStreamReadError(requestID, len(events), state, parseErr)
		if len(events) == 0 {
			return nil, parseErr
		}
		if c.canResumeStream(state, parseErr) {
			resumeResult, resumeErr := c.resumeStream(ctx, requestID, resp.StatusCode, state, events, handler)
			if resumeResult != nil {
				return resumeResult, resumeErr
			}
			c.logger.Print(redaction.ToJSON(map[string]any{
				"event":             "codex_web_stream_resume_failed",
				"request_id":        requestID,
				"response_id":       state.responseID,
				"sequence_number":   state.lastSequenceNumber,
				"error":             redaction.RedactError(resumeErr),
				"redaction_enabled": true,
			}))
		}
		finalStatus = "failed"
		return &codexclient.StreamResult{
			RequestID:   requestID,
			HTTPStatus:  resp.StatusCode,
			Events:      events,
			FinalStatus: finalStatus,
		}, parseErr
	}
	if len(events) == 0 {
		return nil, &codexclient.ClientError{
			Kind:       codexclient.KindMalformedResponse,
			StatusCode: resp.StatusCode,
			RequestID:  requestID,
			Retryable:  false,
			Message:    "codex web stream returned no events",
		}
	}

	c.logger.Print(redaction.ToJSON(map[string]any{
		"event":      "codex_web_stream_response",
		"request_id": requestID,
		"status":     finalStatus,
	}))
	return &codexclient.StreamResult{
		RequestID:   requestID,
		HTTPStatus:  resp.StatusCode,
		Events:      events,
		FinalStatus: finalStatus,
	}, streamFailureError(resp.StatusCode, requestID, events, finalStatus)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("version", "0.0.0-codex-gate")
	req.Header.Set("User-Agent", "codex-gate/0.1")
	if c.accountID != "" {
		req.Header.Set("chatgpt-account-id", c.accountID)
	}
}

type codexWebStreamState struct {
	responseID         string
	lastSequenceNumber int64
}

func (s *codexWebStreamState) record(raw map[string]any) {
	if responseID := actualResponseIDFromRaw(raw); responseID != "" {
		s.responseID = responseID
	}
	if sequenceNumber, ok := sequenceNumberFromRaw(raw); ok {
		s.lastSequenceNumber = sequenceNumber
	}
}

func (c *Client) canRetryBeforeFirstEvent(
	ctx context.Context,
	attempt int,
	result *codexclient.StreamResult,
	err error,
) bool {
	if attempt >= c.streamStartRetries || ctx.Err() != nil {
		return false
	}
	if result != nil && len(result.Events) > 0 {
		return false
	}
	return isRetryableStreamStartError(err)
}

func isRetryableStreamStartError(err error) bool {
	var clientErr *codexclient.ClientError
	if !errors.As(err, &clientErr) {
		return false
	}
	return clientErr.Retryable &&
		(clientErr.Kind == codexclient.KindTransportFailed ||
			clientErr.Kind == codexclient.KindTimeout ||
			clientErr.Kind == codexclient.KindNetworkTimeout ||
			clientErr.Kind == codexclient.KindServerError ||
			clientErr.Kind == codexclient.KindRateLimited)
}

func (c *Client) canResumeStream(state codexWebStreamState, err error) bool {
	return c.streamResume &&
		c.streamResumeRetries > 0 &&
		state.responseID != "" &&
		state.lastSequenceNumber >= 0 &&
		isStreamReadTransportError(err)
}

func isStreamReadTransportError(err error) bool {
	var clientErr *codexclient.ClientError
	return errors.As(err, &clientErr) &&
		clientErr.Kind == codexclient.KindTransportFailed &&
		clientErr.Retryable
}

func (c *Client) resumeStream(
	ctx context.Context,
	requestID string,
	statusCode int,
	state codexWebStreamState,
	events []map[string]any,
	handler codexclient.StreamEventHandler,
) (*codexclient.StreamResult, error) {
	var lastErr error
	finalStatus := "completed"
	emit := func(event map[string]any) error {
		events = append(events, event)
		if handler == nil {
			return nil
		}
		return handler(event)
	}

	for attempt := 0; attempt < c.streamResumeRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.logger.Print(redaction.ToJSON(map[string]any{
			"event":             "codex_web_stream_resume_attempt",
			"request_id":        requestID,
			"response_id":       state.responseID,
			"sequence_number":   state.lastSequenceNumber,
			"attempt":           attempt + 1,
			"max_retries":       c.streamResumeRetries,
			"redaction_enabled": true,
		}))
		resumedState, err := c.resumeStreamAttempt(ctx, state, emit, &finalStatus)
		if err == nil {
			c.logger.Print(redaction.ToJSON(map[string]any{
				"event":             "codex_web_stream_resume_succeeded",
				"request_id":        requestID,
				"response_id":       resumedState.responseID,
				"sequence_number":   resumedState.lastSequenceNumber,
				"redaction_enabled": true,
			}))
			return &codexclient.StreamResult{
				RequestID:   requestID,
				HTTPStatus:  statusCode,
				Events:      events,
				FinalStatus: finalStatus,
			}, streamFailureError(statusCode, requestID, events, finalStatus)
		}
		lastErr = err
		if !isRetryableStreamStartError(err) {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) resumeStreamAttempt(
	ctx context.Context,
	state codexWebStreamState,
	emit func(map[string]any) error,
	finalStatus *string,
) (codexWebStreamState, error) {
	resumeURL, err := c.resumeURL(state.responseID, state.lastSequenceNumber)
	if err != nil {
		return state, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resumeURL, nil)
	if err != nil {
		return state, &codexclient.ClientError{
			Kind:      codexclient.KindRequestBuildFailed,
			Retryable: false,
			Message:   "unable to build codex web stream resume request",
			Cause:     err,
		}
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return state, &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: true,
			Message:   "transport error resuming codex web stream",
			Cause:     err,
		}
	}
	defer resp.Body.Close()
	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = state.responseID
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return state, classifyStatus(resp.StatusCode, requestID, resp.Body)
	}

	resumedState := state
	err = readEventStream(resp.Body, func(raw map[string]any) error {
		resumedState.record(raw)
		mapped, err := mapCodexWebEvent(raw, requestID)
		if err != nil {
			return err
		}
		for _, event := range mapped {
			if event["event"] == "response.error" {
				*finalStatus = "failed"
			}
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return resumedState, err
	}
	return resumedState, nil
}

func (c *Client) resumeURL(responseID string, sequenceNumber int64) (string, error) {
	baseURL, err := url.Parse(c.responsesURL() + "/" + url.PathEscape(responseID))
	if err != nil {
		return "", &codexclient.ClientError{
			Kind:      codexclient.KindRequestBuildFailed,
			Retryable: false,
			Message:   "unable to build codex web stream resume URL",
			Cause:     err,
		}
	}
	query := baseURL.Query()
	query.Set("stream", "true")
	query.Set("starting_after", strconv.FormatInt(sequenceNumber, 10))
	baseURL.RawQuery = query.Encode()
	return baseURL.String(), nil
}

func (c *Client) logStreamReadError(
	requestID string,
	eventsSeen int,
	state codexWebStreamState,
	err error,
) {
	category, _ := classifyStreamReadError(causeOf(err))
	c.logger.Print(redaction.ToJSON(map[string]any{
		"event":             "codex_web_stream_read_error",
		"request_id":        requestID,
		"category":          category,
		"events_seen":       eventsSeen,
		"response_id_set":   state.responseID != "",
		"sequence_number":   state.lastSequenceNumber,
		"error":             redaction.RedactError(err),
		"redaction_enabled": true,
	}))
}

func (c *Client) responsesURL() string {
	if strings.HasSuffix(c.baseURL, "/responses") {
		return c.baseURL
	}
	return c.baseURL + "/responses"
}

func clonePayload(payload map[string]any) map[string]any {
	cloned := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func (c *Client) normalizeRequestPayload(payload map[string]any) map[string]any {
	cloned := clonePayload(payload)
	instructions, _ := cloned["instructions"].(string)
	if strings.TrimSpace(instructions) == "" {
		cloned["instructions"] = defaultInstructions()
	}
	delete(cloned, "max_output_tokens")
	normalizeResponsesToolChoice(cloned)
	normalizeInputMessageTypes(cloned)
	normalizeResponsesInputItems(cloned)
	if c.reasoningEffort != "" {
		cloned["reasoning"] = map[string]any{"effort": c.reasoningEffort}
	}
	if c.serviceTier != "" {
		cloned["service_tier"] = c.serviceTier
	}
	return cloned
}

func normalizeResponsesToolChoice(payload map[string]any) {
	choice, ok := payload["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	choiceType := strings.ToLower(strings.TrimSpace(stringValue(choice["type"])))
	switch choiceType {
	case "auto", "none":
		payload["tool_choice"] = choiceType
	case "any":
		payload["tool_choice"] = "required"
	case "tool":
		name := stringValue(choice["name"])
		payload["tool_choice"] = map[string]any{"type": "function", "name": name}
	}
}

func defaultInstructions() string {
	return strings.TrimSpace(firstNonEmpty(
		os.Getenv("CODEX_WEB_DEFAULT_INSTRUCTIONS"),
		"You are Codex, a coding assistant.",
	))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeInputMessageTypes(payload map[string]any) {
	input, ok := payload["input"].([]any)
	if !ok {
		return
	}
	for _, raw := range input {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, hasType := message["type"]; hasType {
			continue
		}
		if _, hasRole := message["role"]; !hasRole {
			continue
		}
		if _, hasContent := message["content"]; !hasContent {
			continue
		}
		message["type"] = "message"
	}
}

func normalizeResponsesInputItems(payload map[string]any) {
	input, ok := payload["input"].([]any)
	if !ok {
		return
	}
	normalized := make([]any, 0, len(input))
	for _, raw := range input {
		message, ok := raw.(map[string]any)
		if !ok {
			normalized = append(normalized, raw)
			continue
		}
		role, _ := message["role"].(string)
		content, ok := message["content"].([]any)
		if !ok {
			normalized = append(normalized, message)
			continue
		}

		switch strings.TrimSpace(role) {
		case "user":
			appendUserResponsesItems(&normalized, message, content)
		case "assistant":
			appendAssistantResponsesItems(&normalized, message, content)
		default:
			normalized = append(normalized, message)
		}
	}
	payload["input"] = normalized
}

func appendUserResponsesItems(out *[]any, message map[string]any, content []any) {
	parts := make([]any, 0, len(content))
	flushParts := func() {
		if len(parts) == 0 {
			return
		}
		*out = append(*out, cloneMessageWithContent(message, parts))
		parts = make([]any, 0, len(content))
	}

	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			parts = append(parts, rawBlock)
			continue
		}
		if isEmptyResponsesTextBlock(block) {
			continue
		}
		blockType, _ := block["type"].(string)
		if blockType != "tool_result" {
			parts = append(parts, block)
			continue
		}
		flushParts()
		*out = append(*out, functionCallOutputItem(block))
	}
	flushParts()
}

func appendAssistantResponsesItems(out *[]any, message map[string]any, content []any) {
	parts := make([]any, 0, len(content))
	flushParts := func() {
		if len(parts) == 0 {
			return
		}
		*out = append(*out, cloneMessageWithContent(message, parts))
		parts = make([]any, 0, len(content))
	}

	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			parts = append(parts, rawBlock)
			continue
		}
		blockType, _ := block["type"].(string)
		if isEmptyResponsesTextBlock(block) {
			continue
		}
		switch blockType {
		case "input_text":
			block["type"] = "output_text"
			parts = append(parts, block)
		case "tool_call":
			flushParts()
			*out = append(*out, functionCallItem(block))
		default:
			parts = append(parts, block)
		}
	}
	flushParts()
}

func isEmptyResponsesTextBlock(block map[string]any) bool {
	blockType, _ := block["type"].(string)
	switch blockType {
	case "input_text", "output_text", "text":
		text, _ := block["text"].(string)
		return strings.TrimSpace(text) == ""
	default:
		return false
	}
}

func cloneMessageWithContent(message map[string]any, content []any) map[string]any {
	cloned := make(map[string]any, len(message))
	for key, value := range message {
		cloned[key] = value
	}
	cloned["content"] = content
	return cloned
}

func functionCallItem(block map[string]any) map[string]any {
	return map[string]any{
		"type":      "function_call",
		"call_id":   firstString(block["call_id"], block["id"]),
		"name":      stringValue(block["name"]),
		"arguments": jsonStringValue(block["arguments"], "{}"),
	}
}

func functionCallOutputItem(block map[string]any) map[string]any {
	output := toolOutputString(block["content"])
	if isError, _ := block["is_error"].(bool); isError {
		output = "[tool execution error]\n" + output
	}
	return map[string]any{
		"type":    "function_call_output",
		"call_id": firstString(block["call_id"], block["tool_call_id"], block["tool_use_id"]),
		"output":  output,
	}
}

func firstString(values ...any) string {
	for _, raw := range values {
		if value, _ := raw.(string); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(raw any) string {
	value, _ := raw.(string)
	return value
}

func jsonStringValue(raw any, fallback string) string {
	if text, ok := raw.(string); ok {
		if strings.TrimSpace(text) == "" {
			return fallback
		}
		return text
	}
	if raw == nil {
		return fallback
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fallback
	}
	return string(encoded)
}

func toolOutputString(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if block, ok := item.(map[string]any); ok {
				if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
					continue
				}
			}
			parts = append(parts, jsonStringValue(item, ""))
		}
		return strings.Join(parts, "\n")
	default:
		return jsonStringValue(value, fmt.Sprint(value))
	}
}

func readEventStream(reader io.Reader, onEvent func(map[string]any) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	dataLines := make([]string, 0, 4)
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
			return nil
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			return &codexclient.ClientError{
				Kind:      codexclient.KindMalformedResponse,
				Retryable: false,
				Message:   "unable to parse codex web SSE event",
				Cause:     err,
			}
		}
		return onEvent(raw)
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "event:") {
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			continue
		}
		if strings.HasPrefix(trimmed, "{") {
			dataLines = append(dataLines, trimmed)
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_, message := classifyStreamReadError(err)
		return &codexclient.ClientError{
			Kind:      codexclient.KindTransportFailed,
			Retryable: true,
			Message:   "unable to read codex web stream: " + message,
			Cause:     err,
		}
	}
	return flush()
}

func classifyStreamReadError(err error) (string, string) {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled", "downstream request context was canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "stream read timed out"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof", "upstream stream ended unexpectedly"
	case errors.Is(err, io.EOF):
		return "eof", "upstream stream ended"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout", "network stream read timed out"
	}

	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "connection reset") || strings.Contains(lower, "forcibly closed"):
		return "connection_reset", "upstream connection was reset"
	case strings.Contains(lower, "unexpected eof"):
		return "unexpected_eof", "upstream stream ended unexpectedly"
	case strings.Contains(lower, "eof"):
		return "eof", "upstream stream ended"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "timeout", "stream read timed out"
	case strings.Contains(lower, "context canceled") || strings.Contains(lower, "context cancelled"):
		return "context_canceled", "downstream request context was canceled"
	default:
		return "read_error", "stream read failed"
	}
}

func causeOf(err error) error {
	var clientErr *codexclient.ClientError
	if errors.As(err, &clientErr) && clientErr.Cause != nil {
		return clientErr.Cause
	}
	return err
}

func mapCodexWebEvent(raw map[string]any, fallbackID string) ([]map[string]any, error) {
	if internalName, _ := raw["event"].(string); strings.HasPrefix(internalName, "response.") {
		return []map[string]any{raw}, nil
	}

	eventType, _ := raw["type"].(string)
	responseID := responseIDFromRaw(raw, fallbackID)
	switch eventType {
	case "response.created", "response.in_progress":
		return []map[string]any{{"event": "response.started", "response_id": responseID}}, nil
	case "response.output_text.delta":
		delta, _ := raw["delta"].(string)
		if delta == "" {
			return nil, nil
		}
		return []map[string]any{{"event": "response.output_text.delta", "delta": delta}}, nil
	case "response.output_item.done":
		item, _ := raw["item"].(map[string]any)
		if strings.TrimSpace(typeString(item)) != "function_call" {
			return nil, nil
		}
		toolCall, err := toolCallFromItem(item)
		if err != nil {
			return nil, err
		}
		return []map[string]any{toolCall}, nil
	case "response.completed":
		return []map[string]any{{
			"event":       "response.completed",
			"response_id": responseID,
			"status":      "completed",
		}}, nil
	case "response.failed", "response.incomplete":
		return []map[string]any{{
			"event":       "response.error",
			"response_id": responseID,
			"code":        "upstream_error",
			"message":     errorMessage(raw),
		}}, nil
	case "error":
		return []map[string]any{{
			"event":       "response.error",
			"response_id": responseID,
			"code":        "upstream_error",
			"message":     errorMessage(raw),
		}}, nil
	default:
		return nil, nil
	}
}

func responseIDFromRaw(raw map[string]any, fallback string) string {
	if id := actualResponseIDFromRaw(raw); id != "" {
		return id
	}
	return "resp_" + fallback
}

func actualResponseIDFromRaw(raw map[string]any) string {
	if response, _ := raw["response"].(map[string]any); response != nil {
		if id, _ := response["id"].(string); strings.TrimSpace(id) != "" {
			return id
		}
	}
	if id, _ := raw["response_id"].(string); strings.TrimSpace(id) != "" {
		return id
	}
	if id, _ := raw["id"].(string); strings.TrimSpace(id) != "" {
		return id
	}
	return ""
}

func sequenceNumberFromRaw(raw map[string]any) (int64, bool) {
	switch value := raw["sequence_number"].(type) {
	case float64:
		if value < 0 || value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	case int64:
		if value < 0 {
			return 0, false
		}
		return value, true
	case int:
		if value < 0 {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil || parsed < 0 {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func typeString(item map[string]any) string {
	value, _ := item["type"].(string)
	return value
}

func toolCallFromItem(item map[string]any) (map[string]any, error) {
	id, _ := item["call_id"].(string)
	if strings.TrimSpace(id) == "" {
		id, _ = item["id"].(string)
	}
	name, _ := item["name"].(string)
	arguments := map[string]any{}
	switch rawArgs := item["arguments"].(type) {
	case string:
		if strings.TrimSpace(rawArgs) != "" {
			if err := json.Unmarshal([]byte(rawArgs), &arguments); err != nil {
				return nil, &codexclient.ClientError{
					Kind:      codexclient.KindMalformedResponse,
					Retryable: false,
					Message:   "codex web function_call arguments must be JSON",
					Cause:     err,
				}
			}
		}
	case map[string]any:
		arguments = rawArgs
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return nil, &codexclient.ClientError{
			Kind:      codexclient.KindMalformedResponse,
			Retryable: false,
			Message:   "codex web function_call requires call_id and name",
		}
	}
	return map[string]any{
		"event":     "response.tool_call",
		"id":        id,
		"name":      name,
		"arguments": arguments,
	}, nil
}

func errorMessage(raw map[string]any) string {
	if message, _ := raw["message"].(string); strings.TrimSpace(message) != "" {
		return message
	}
	if errObj, _ := raw["error"].(map[string]any); errObj != nil {
		if message, _ := errObj["message"].(string); strings.TrimSpace(message) != "" {
			return message
		}
	}
	return "codex web stream failed"
}

func completionFromStream(streamResult *codexclient.StreamResult) (*codexclient.CompletionResult, error) {
	var text strings.Builder
	output := make([]any, 0)
	for _, event := range streamResult.Events {
		switch event["event"] {
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			text.WriteString(delta)
		case "response.tool_call":
			output = append(output, map[string]any{
				"type":      "tool_call",
				"id":        event["id"],
				"name":      event["name"],
				"arguments": event["arguments"],
			})
		}
	}
	if text.Len() > 0 {
		output = append([]any{
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": text.String()},
				},
			},
		}, output...)
	}
	if len(output) == 0 {
		return nil, &codexclient.ClientError{
			Kind:       codexclient.KindMalformedResponse,
			StatusCode: streamResult.HTTPStatus,
			RequestID:  streamResult.RequestID,
			Retryable:  false,
			Message:    "codex web completion missing output",
		}
	}
	return &codexclient.CompletionResult{
		RequestID:  streamResult.RequestID,
		HTTPStatus: streamResult.HTTPStatus,
		Response: map[string]any{
			"id":     "resp_" + streamResult.RequestID,
			"status": streamResult.FinalStatus,
			"output": output,
		},
	}, nil
}

func streamFailureError(statusCode int, requestID string, events []map[string]any, finalStatus string) error {
	if strings.EqualFold(strings.TrimSpace(finalStatus), "completed") {
		return nil
	}
	message := "codex web stream failed"
	for _, event := range events {
		if event["event"] == "response.error" {
			if text, _ := event["message"].(string); strings.TrimSpace(text) != "" {
				message = text
				break
			}
		}
	}
	return &codexclient.ClientError{
		Kind:       codexclient.KindStreamFailed,
		StatusCode: statusCode,
		RequestID:  requestID,
		Retryable:  false,
		Message:    message,
	}
}

func classifyStatus(status int, requestID string, reader io.Reader) *codexclient.ClientError {
	body, _ := io.ReadAll(io.LimitReader(reader, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("codex web endpoint returned HTTP %d", status)
	}
	kind := codexclient.KindMalformedResponse
	retryable := false
	switch status {
	case http.StatusUnauthorized:
		kind = codexclient.KindUnauthorized
	case http.StatusForbidden:
		kind = codexclient.KindForbidden
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		kind = codexclient.KindTimeout
		retryable = true
	case http.StatusTooManyRequests:
		kind = codexclient.KindRateLimited
		retryable = true
	default:
		if status >= 500 {
			kind = codexclient.KindServerError
			retryable = true
		}
	}
	return &codexclient.ClientError{
		Kind:       kind,
		StatusCode: status,
		RequestID:  requestID,
		Retryable:  retryable,
		Message:    message,
	}
}

func extractAccountIDFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return findStringClaim(claims, "chatgpt_account_id", "account_id")
}

func findStringClaim(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if raw, ok := typed[key].(string); ok && strings.TrimSpace(raw) != "" {
				return raw
			}
		}
		for _, nested := range typed {
			if found := findStringClaim(nested, keys...); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range typed {
			if found := findStringClaim(nested, keys...); found != "" {
				return found
			}
		}
	}
	return ""
}
