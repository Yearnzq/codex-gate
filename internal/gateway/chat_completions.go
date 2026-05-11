package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"codex-gate/internal/codexclient"
	"codex-gate/internal/redaction"
)

func (s *Server) chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMessagesError(
			w,
			r,
			http.StatusMethodNotAllowed,
			"invalid_request_error",
			"method_not_allowed",
			"use POST /v1/chat/completions",
		)
		return
	}
	if s.codexClient == nil {
		s.writeMessagesError(
			w,
			r,
			http.StatusServiceUnavailable,
			"api_error",
			"backend_unavailable",
			"codex backend client is not configured",
		)
		return
	}

	body, err := readMessagesBody(r)
	if err != nil {
		s.writeMessagesError(
			w,
			r,
			http.StatusBadRequest,
			"invalid_request_error",
			"invalid_json",
			"request body must be valid JSON",
		)
		return
	}

	request, parseErr := parseChatCompletionsRequest(body)
	if parseErr != nil {
		s.writeMessagesError(w, r, parseErr.Status, parseErr.ErrType, parseErr.Code, parseErr.Message)
		return
	}
	if request.Stream {
		s.chatCompletionsStreamHandler(w, r, request)
		return
	}

	codexPayload := s.convertMessagesRequestToCodexPayload(request)
	completion, callErr := s.codexClient.Create(
		r.Context(),
		codexPayload,
		codexclient.CallOptions{SafeToRetry: false},
	)
	if callErr != nil {
		translated := mapCodexClientError(callErr)
		s.writeMessagesError(
			w,
			r,
			translated.Status,
			translated.ErrType,
			translated.Code,
			translated.Message,
		)
		return
	}

	response, convertErr := convertCodexResponseToChatCompletion(request, completion)
	if convertErr != nil {
		s.writeMessagesError(w, r, convertErr.Status, convertErr.ErrType, convertErr.Code, convertErr.Message)
		return
	}

	s.writeJSON(w, r, http.StatusOK, response)
}

func parseChatCompletionsRequest(body []byte) (messagesRequest, *messagesAPIError) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_json",
			"request body must be valid JSON",
		)
	}

	normalizeChatCompletionTokenLimit(raw)
	normalizeChatCompletionStop(raw)
	if apiErr := normalizeChatCompletionToolChoice(raw); apiErr != nil {
		return messagesRequest{}, apiErr
	}
	dropIgnoredChatCompletionFields(raw)

	normalized, err := json.Marshal(raw)
	if err != nil {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_request",
			"request body could not be normalized",
		)
	}
	return parseMessagesRequest(normalized, messagesParseOptions{AllowStream: true})
}

func normalizeChatCompletionTokenLimit(raw map[string]json.RawMessage) {
	if _, ok := raw["max_tokens"]; ok {
		delete(raw, "max_completion_tokens")
		return
	}
	if value, ok := raw["max_completion_tokens"]; ok {
		raw["max_tokens"] = value
		delete(raw, "max_completion_tokens")
		return
	}
	raw["max_tokens"] = json.RawMessage(`4096`)
}

func normalizeChatCompletionStop(raw map[string]json.RawMessage) {
	if _, ok := raw["stop_sequences"]; ok {
		delete(raw, "stop")
		return
	}
	stop, ok := raw["stop"]
	if !ok {
		return
	}
	var single string
	if err := json.Unmarshal(stop, &single); err == nil {
		encoded, _ := json.Marshal([]string{single})
		raw["stop_sequences"] = encoded
		delete(raw, "stop")
		return
	}
	raw["stop_sequences"] = stop
	delete(raw, "stop")
}

func normalizeChatCompletionToolChoice(raw map[string]json.RawMessage) *messagesAPIError {
	choice, ok := raw["tool_choice"]
	if !ok {
		return nil
	}

	var text string
	if err := json.Unmarshal(choice, &text); err == nil {
		choiceType := strings.ToLower(strings.TrimSpace(text))
		if choiceType == "required" {
			choiceType = "any"
		}
		normalized, _ := json.Marshal(map[string]any{"type": choiceType})
		raw["tool_choice"] = normalized
		return nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(choice, &object); err != nil {
		return newInvalidRequestError("invalid_request", "tool_choice must be a string or object")
	}
	var choiceType string
	if rawType, ok := object["type"]; ok {
		if err := json.Unmarshal(rawType, &choiceType); err != nil {
			return newInvalidRequestError("invalid_request", "tool_choice.type must be a string")
		}
	}
	choiceType = strings.ToLower(strings.TrimSpace(choiceType))
	if choiceType != "function" {
		return nil
	}

	var functionFields map[string]json.RawMessage
	if rawFunction, ok := object["function"]; ok {
		if err := json.Unmarshal(rawFunction, &functionFields); err != nil {
			return newInvalidRequestError("invalid_request", "tool_choice.function must be an object")
		}
	}
	name, apiErr := decodeRequiredString(functionFields, "name")
	if apiErr != nil {
		return apiErr
	}
	normalized, _ := json.Marshal(map[string]any{"type": "tool", "name": name})
	raw["tool_choice"] = normalized
	return nil
}

func dropIgnoredChatCompletionFields(raw map[string]json.RawMessage) {
	for _, field := range []string{
		"frequency_penalty",
		"logit_bias",
		"metadata",
		"n",
		"parallel_tool_calls",
		"presence_penalty",
		"response_format",
		"seed",
		"stream_options",
		"temperature",
		"top_p",
		"user",
	} {
		delete(raw, field)
	}
}

func convertCodexResponseToChatCompletion(
	request messagesRequest,
	completion *codexclient.CompletionResult,
) (map[string]any, *messagesAPIError) {
	messageResponse, apiErr := convertCodexResponseToMessagesResponse(request, completion)
	if apiErr != nil {
		return nil, apiErr
	}
	content, apiErr := chatCompletionTextFromContent(messageResponse["content"])
	if apiErr != nil {
		return nil, apiErr
	}

	return map[string]any{
		"id":      chatCompletionID(messageResponse["id"]),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   request.Model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": chatCompletionFinishReason(messageResponse["stop_reason"]),
			},
		},
		"usage": messageResponse["usage"],
	}, nil
}

func chatCompletionTextFromContent(raw any) (string, *messagesAPIError) {
	var builder strings.Builder
	appendText := func(block map[string]any) *messagesAPIError {
		blockType, _ := block["type"].(string)
		if blockType != "text" {
			return unsupportedChatCompletionOutputError(
				fmt.Sprintf("chat completions cannot represent assistant content block type %q", blockType),
			)
		}
		text, _ := block["text"].(string)
		builder.WriteString(text)
		return nil
	}

	switch typed := raw.(type) {
	case []map[string]any:
		for _, block := range typed {
			if err := appendText(block); err != nil {
				return "", err
			}
		}
	case []any:
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				return "", unsupportedChatCompletionOutputError("chat completion content blocks must be objects")
			}
			if err := appendText(block); err != nil {
				return "", err
			}
		}
	default:
		return "", unsupportedChatCompletionOutputError("chat completion content must be an array")
	}
	return builder.String(), nil
}

func unsupportedChatCompletionOutputError(message string) *messagesAPIError {
	return &messagesAPIError{
		Status:  http.StatusBadGateway,
		ErrType: "api_error",
		Code:    "unsupported_upstream_output",
		Message: message,
	}
}

func (s *Server) chatCompletionsStreamHandler(w http.ResponseWriter, r *http.Request, request messagesRequest) {
	codexPayload := s.convertMessagesRequestToCodexPayload(request)
	codexPayload["stream"] = true

	if streamer, ok := s.codexClient.(codexclient.EventStreamer); ok {
		s.writeChatCompletionRealtimeSSE(w, r, request, codexPayload, streamer)
		return
	}

	streamResult, streamErr := s.codexClient.Stream(
		r.Context(),
		codexPayload,
		codexclient.CallOptions{SafeToRetry: false},
	)
	if streamResult == nil {
		translated := mapCodexClientError(streamErr)
		s.writeMessagesError(w, r, translated.Status, translated.ErrType, translated.Code, translated.Message)
		return
	}

	s.writeChatCompletionSSE(w, r, request, streamResult, streamErr)
}

func (s *Server) writeChatCompletionRealtimeSSE(
	w http.ResponseWriter,
	r *http.Request,
	request messagesRequest,
	codexPayload map[string]any,
	streamer codexclient.EventStreamer,
) {
	id := chatCompletionID("codex_cli_stream")
	created := time.Now().Unix()
	var writeMu sync.Mutex

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	writeMu.Lock()
	writeOpenAIStreamChunk(w, id, created, request.Model, map[string]any{"role": "assistant"}, nil)
	writeMu.Unlock()
	stopKeepalive := startOpenAIChunkKeepalive(r.Context(), w, &writeMu, func() string {
		return id
	}, created, request.Model)

	sawErrorEvent := false
	var streamError map[string]any
	_, streamErr := streamer.StreamEvents(
		r.Context(),
		codexPayload,
		codexclient.CallOptions{SafeToRetry: false},
		func(raw map[string]any) error {
			name, _ := raw["event"].(string)
			if responseID, _ := raw["response_id"].(string); strings.TrimSpace(responseID) != "" {
				id = chatCompletionID(responseID)
			}
			switch name {
			case "response.output_text.delta":
				delta, _ := raw["delta"].(string)
				writeMu.Lock()
				writeOpenAIStreamChunk(w, id, created, request.Model, map[string]any{"content": delta}, nil)
				writeMu.Unlock()
			case "response.error":
				sawErrorEvent = true
				streamError = chatCompletionStreamErrorFromRaw(raw)
			}
			return nil
		},
	)
	stopKeepalive()
	writeMu.Lock()
	if streamErr != nil {
		writeOpenAIStreamError(w, chatCompletionStreamErrorFromClient(streamErr))
	} else if sawErrorEvent {
		writeOpenAIStreamError(w, streamError)
	} else {
		writeOpenAIStreamChunk(w, id, created, request.Model, map[string]any{}, "stop")
	}
	writeOpenAIStreamDone(w)
	writeMu.Unlock()
	s.logChatCompletionSSE(r, http.StatusOK)
}

func (s *Server) writeChatCompletionSSE(
	w http.ResponseWriter,
	r *http.Request,
	request messagesRequest,
	streamResult *codexclient.StreamResult,
	streamErr error,
) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	id := chatCompletionID(responseIDFromStream(streamResult))
	created := time.Now().Unix()
	writeOpenAIStreamChunk(w, id, created, request.Model, map[string]any{"role": "assistant"}, nil)

	for _, raw := range streamResult.Events {
		name, _ := raw["event"].(string)
		switch name {
		case "response.output_text.delta":
			delta, _ := raw["delta"].(string)
			writeOpenAIStreamChunk(w, id, created, request.Model, map[string]any{"content": delta}, nil)
		case "response.error":
			writeOpenAIStreamError(w, chatCompletionStreamErrorFromRaw(raw))
			writeOpenAIStreamDone(w)
			s.logChatCompletionSSE(r, http.StatusOK)
			return
		}
	}

	finishReason := "stop"
	if streamErr != nil {
		writeOpenAIStreamError(w, chatCompletionStreamErrorFromClient(streamErr))
		writeOpenAIStreamDone(w)
		s.logChatCompletionSSE(r, http.StatusOK)
		return
	}
	writeOpenAIStreamChunk(w, id, created, request.Model, map[string]any{}, finishReason)
	writeOpenAIStreamDone(w)
	s.logChatCompletionSSE(r, http.StatusOK)
}

func writeOpenAIStreamChunk(
	w http.ResponseWriter,
	id string,
	created int64,
	model string,
	delta map[string]any,
	finishReason any,
) {
	payload := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("data: " + string(body) + "\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeOpenAIStreamDone(w http.ResponseWriter) {
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeOpenAIStreamError(w http.ResponseWriter, errorPayload map[string]any) {
	if errorPayload == nil {
		errorPayload = map[string]any{
			"type":    "api_error",
			"code":    "upstream_error",
			"message": "upstream stream failed",
		}
	}
	body, err := json.Marshal(map[string]any{"error": errorPayload})
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: error\n"))
	_, _ = w.Write([]byte("data: " + string(body) + "\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func chatCompletionStreamErrorFromClient(err error) map[string]any {
	translated := mapCodexClientError(err)
	return map[string]any{
		"type":    translated.ErrType,
		"code":    translated.Code,
		"message": translated.Message,
	}
}

func chatCompletionStreamErrorFromRaw(raw map[string]any) map[string]any {
	code, _ := raw["code"].(string)
	message, _ := raw["message"].(string)
	if strings.TrimSpace(code) == "" {
		code = "upstream_error"
	}
	if strings.TrimSpace(message) == "" {
		message = "upstream stream error"
	}
	return map[string]any{
		"type":    "api_error",
		"code":    code,
		"message": message,
	}
}

func (s *Server) logChatCompletionSSE(r *http.Request, status int) {
	s.logger.Print(
		redaction.ToJSON(map[string]any{
			"event":             "http_request",
			"method":            r.Method,
			"path":              r.URL.Path,
			"status":            status,
			"request_body":      redaction.RedactBody(nil),
			"response_body":     redaction.RedactBody(nil),
			"redaction_enabled": s.config.RedactLogs,
		}),
	)
}

func chatCompletionID(raw any) string {
	if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
		if strings.HasPrefix(text, "chatcmpl_") {
			return text
		}
		return "chatcmpl_" + text
	}
	return "chatcmpl_compat"
}

func chatCompletionFinishReason(raw any) string {
	reason, _ := raw.(string)
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		return "stop"
	}
}
