package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"codex-gate/internal/codexclient"
	"codex-gate/internal/redaction"
)

type messagesSSEEvent struct {
	Name string
	Data map[string]any
}

type messagesStreamState struct {
	Request     messagesRequest
	Stream      *codexclient.StreamResult
	ResponseID  string
	MessageID   string
	MessageOpen bool

	NextIndex     int
	TextBlockOpen bool
	TextBlockIdx  int
	ToolUseSeen   bool
}

func (s *Server) messagesStreamHandler(w http.ResponseWriter, r *http.Request, request messagesRequest) {
	codexPayload := s.convertMessagesRequestToCodexPayload(request)
	codexPayload["stream"] = true

	if streamer, ok := s.codexClient.(codexclient.EventStreamer); ok {
		s.messagesRealtimeStreamHandler(w, r, request, codexPayload, streamer)
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

	events, convertErr := convertCodexStreamToMessagesSSE(request, streamResult, streamErr)
	if convertErr != nil {
		s.writeMessagesError(w, r, convertErr.Status, convertErr.ErrType, convertErr.Code, convertErr.Message)
		return
	}

	s.writeSSE(w, events)
}

func (s *Server) messagesRealtimeStreamHandler(
	w http.ResponseWriter,
	r *http.Request,
	request messagesRequest,
	codexPayload map[string]any,
	streamer codexclient.EventStreamer,
) {
	state := messagesStreamState{
		Request:       request,
		ResponseID:    "",
		MessageID:     "msg_stream",
		TextBlockIdx:  -1,
		NextIndex:     0,
		TextBlockOpen: false,
	}
	stopped := false
	sawErrorEvent := false
	streamIndex := 0
	var writeMu sync.Mutex
	startMessagesSSE(w)
	writeMu.Lock()
	for _, event := range append(ensureMessageStart(&state), ensureTextBlockStart(&state)...) {
		writeMessagesSSEEvent(w, event)
	}
	writeMu.Unlock()
	stopKeepalive := startMessagesPingKeepalive(r.Context(), w, &writeMu)

	writeEvents := func(events []messagesSSEEvent) {
		if len(events) == 0 {
			return
		}
		for _, event := range events {
			if event.Name == "message_stop" {
				stopped = true
			}
			writeMu.Lock()
			writeMessagesSSEEvent(w, event)
			writeMu.Unlock()
		}
	}

	streamResult, streamErr := streamer.StreamEvents(
		r.Context(),
		codexPayload,
		codexclient.CallOptions{SafeToRetry: false},
		func(raw map[string]any) error {
			events, apiErr := convertCodexStreamEventToMessagesSSE(
				&state,
				raw,
				streamIndex,
				nil,
				&sawErrorEvent,
			)
			streamIndex++
			if apiErr != nil {
				return errors.New(apiErr.Message)
			}
			writeEvents(events)
			return nil
		},
	)

	if streamErr != nil && !sawErrorEvent && !stopped {
		writeEvents(closeOpenTextBlock(&state))
		writeEvents([]messagesSSEEvent{buildSynthesizedStreamErrorEvent(&state, streamErr)})
		sawErrorEvent = true
	}
	if !stopped && state.MessageOpen {
		writeEvents(closeOpenTextBlock(&state))
		stopReason := streamStopReason(&state, streamResult, streamErr)
		writeEvents(messageCompleteEvents(&state, stopReason))
	}
	stopKeepalive()
	s.logSSERequest(r, http.StatusOK)
}

func (s *Server) writeSSE(w http.ResponseWriter, events []messagesSSEEvent) {
	startMessagesSSE(w)
	for _, item := range events {
		writeMessagesSSEEvent(w, item)
	}
	s.logSSERequest(nil, http.StatusOK)
}

func startMessagesSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeMessagesSSEEvent(w http.ResponseWriter, item messagesSSEEvent) {
	data, err := json.Marshal(item.Data)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: " + item.Name + "\n"))
	_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) logSSERequest(r *http.Request, status int) {
	method := http.MethodPost
	path := "/v1/messages"
	if r != nil {
		method = r.Method
		path = r.URL.Path
	}
	s.logger.Print(
		redaction.ToJSON(map[string]any{
			"event":             "http_request",
			"method":            method,
			"path":              path,
			"status":            status,
			"request_body":      redaction.RedactBody(nil),
			"response_body":     redaction.RedactBody(nil),
			"redaction_enabled": s.config.RedactLogs,
		}),
	)
}

func convertCodexStreamToMessagesSSE(
	request messagesRequest,
	streamResult *codexclient.StreamResult,
	streamErr error,
) ([]messagesSSEEvent, *messagesAPIError) {
	state := messagesStreamState{
		Request:       request,
		Stream:        streamResult,
		ResponseID:    "",
		MessageID:     responseIDFromStream(streamResult),
		MessageOpen:   false,
		NextIndex:     0,
		TextBlockOpen: false,
		TextBlockIdx:  -1,
	}

	events := make([]messagesSSEEvent, 0, len(streamResult.Events)+8)
	sawErrorEvent := false
	for index, raw := range streamResult.Events {
		converted, apiErr := convertCodexStreamEventToMessagesSSE(
			&state,
			raw,
			index,
			streamErr,
			&sawErrorEvent,
		)
		if apiErr != nil {
			return nil, apiErr
		}
		events = append(events, converted...)
	}

	if !state.MessageOpen {
		events = append(events, ensureMessageStart(&state)...)
	}

	events = append(events, closeOpenTextBlock(&state)...)
	if streamErr != nil && !sawErrorEvent {
		events = append(events, buildSynthesizedStreamErrorEvent(&state, streamErr))
		sawErrorEvent = true
	}
	if !containsSSEEvent(events, "message_stop") {
		stopReason := streamStopReason(&state, streamResult, streamErr)
		events = append(events, messageCompleteEvents(&state, stopReason)...)
	}

	return events, nil
}

func convertCodexStreamEventToMessagesSSE(
	state *messagesStreamState,
	raw map[string]any,
	index int,
	streamErr error,
	sawErrorEvent *bool,
) ([]messagesSSEEvent, *messagesAPIError) {
	events := []messagesSSEEvent{}
	name, _ := raw["event"].(string)
	switch name {
	case "response.started":
		if responseID, ok := raw["response_id"].(string); ok && strings.TrimSpace(responseID) != "" && !state.MessageOpen {
			state.ResponseID = responseID
			state.MessageID = responseID
		}
		events = append(events, ensureMessageStart(state)...)
	case "response.output_text.delta":
		events = append(events, ensureMessageStart(state)...)
		events = append(events, ensureTextBlockStart(state)...)
		delta, _ := raw["delta"].(string)
		events = append(
			events,
			messagesSSEEvent{
				Name: "content_block_delta",
				Data: map[string]any{
					"type":        "content_block_delta",
					"index":       state.TextBlockIdx,
					"delta":       map[string]any{"type": "text_delta", "text": delta},
					"message_id":  state.MessageID,
					"response_id": state.ResponseID,
				},
			},
		)
	case "response.tool_call":
		events = append(events, ensureMessageStart(state)...)
		events = append(events, closeOpenTextBlock(state)...)
		toolEvents, err := convertToolCallStreamEvent(state, raw, index)
		if err != nil {
			return nil, err
		}
		events = append(events, toolEvents...)
	case "response.tool_result":
		events = append(events, ensureMessageStart(state)...)
		events = append(events, closeOpenTextBlock(state)...)
		toolEvents, err := convertToolResultStreamEvent(state, raw, index)
		if err != nil {
			return nil, err
		}
		events = append(events, toolEvents...)
	case "response.error":
		events = append(events, closeOpenTextBlock(state)...)
		code, _ := raw["code"].(string)
		message, _ := raw["message"].(string)
		if strings.TrimSpace(message) == "" {
			message = "upstream stream error"
		}
		if strings.TrimSpace(code) == "" {
			code = "upstream_error"
		}
		events = append(
			events,
			messagesSSEEvent{
				Name: "error",
				Data: map[string]any{
					"type": "error",
					"error": map[string]any{
						"type":    "api_error",
						"code":    code,
						"message": message,
					},
					"message_id":  state.MessageID,
					"response_id": state.ResponseID,
				},
			},
		)
		*sawErrorEvent = true
	case "response.completed":
		events = append(events, closeOpenTextBlock(state)...)
		if streamErr != nil && !*sawErrorEvent {
			events = append(events, buildSynthesizedStreamErrorEvent(state, streamErr))
			*sawErrorEvent = true
		}
		stopReason := streamStopReason(state, state.Stream, streamErr)
		events = append(events, messageCompleteEvents(state, stopReason)...)
	default:
		// Preserve deterministic behavior by failing closed on unknown stream event kinds.
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "unsupported_upstream_stream_event",
			Message: fmt.Sprintf("codex stream event[%d]=%q is unsupported", index, name),
		}
	}
	return events, nil
}

func buildSynthesizedStreamErrorEvent(
	state *messagesStreamState,
	streamErr error,
) messagesSSEEvent {
	translated := mapCodexClientError(streamErr)
	return messagesSSEEvent{
		Name: "error",
		Data: map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    translated.ErrType,
				"code":    translated.Code,
				"message": translated.Message,
			},
			"message_id":  state.MessageID,
			"response_id": state.ResponseID,
		},
	}
}

func ensureMessageStart(state *messagesStreamState) []messagesSSEEvent {
	if state.MessageOpen {
		return nil
	}
	state.MessageOpen = true
	return []messagesSSEEvent{
		{
			Name: "message_start",
			Data: map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":            state.MessageID,
					"type":          "message",
					"role":          "assistant",
					"model":         state.Request.Model,
					"content":       []any{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage": map[string]any{
						"input_tokens":  estimateInputTokens(state.Request),
						"output_tokens": 0,
					},
				},
			},
		},
	}
}

func ensureTextBlockStart(state *messagesStreamState) []messagesSSEEvent {
	if state.TextBlockOpen {
		return nil
	}
	state.TextBlockOpen = true
	state.TextBlockIdx = state.NextIndex
	state.NextIndex++
	return []messagesSSEEvent{
		{
			Name: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": state.TextBlockIdx,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
	}
}

func closeOpenTextBlock(state *messagesStreamState) []messagesSSEEvent {
	if !state.TextBlockOpen || state.TextBlockIdx < 0 {
		return nil
	}
	index := state.TextBlockIdx
	state.TextBlockOpen = false
	state.TextBlockIdx = -1
	return []messagesSSEEvent{
		{
			Name: "content_block_stop",
			Data: map[string]any{
				"type":        "content_block_stop",
				"index":       index,
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
	}
}

func messageCompleteEvents(state *messagesStreamState, stopReason string) []messagesSSEEvent {
	return []messagesSSEEvent{
		{
			Name: "message_delta",
			Data: map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]any{
					"output_tokens": 1,
				},
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
		{
			Name: "message_stop",
			Data: map[string]any{
				"type":        "message_stop",
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
	}
}

func convertToolCallStreamEvent(
	state *messagesStreamState,
	raw map[string]any,
	streamIndex int,
) ([]messagesSSEEvent, *messagesAPIError) {
	state.ToolUseSeen = true
	id, _ := raw["id"].(string)
	name, _ := raw["name"].(string)
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: fmt.Sprintf("codex tool_call stream event[%d] requires id and name", streamIndex),
		}
	}

	arguments := map[string]any{}
	if value, ok := raw["arguments"].(map[string]any); ok {
		arguments = value
	}
	encodedArgs, _ := json.Marshal(arguments)
	index := state.NextIndex
	state.NextIndex++

	return []messagesSSEEvent{
		{
			Name: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    id,
					"name":  name,
					"input": map[string]any{},
				},
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
		{
			Name: "content_block_delta",
			Data: map[string]any{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": string(encodedArgs),
				},
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
		{
			Name: "content_block_stop",
			Data: map[string]any{
				"type":        "content_block_stop",
				"index":       index,
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
	}, nil
}

func convertToolResultStreamEvent(
	state *messagesStreamState,
	raw map[string]any,
	streamIndex int,
) ([]messagesSSEEvent, *messagesAPIError) {
	toolUseID, _ := raw["tool_call_id"].(string)
	if strings.TrimSpace(toolUseID) == "" {
		toolUseID, _ = raw["tool_use_id"].(string)
	}
	if strings.TrimSpace(toolUseID) == "" {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: fmt.Sprintf("codex tool_result stream event[%d] requires tool_call_id", streamIndex),
		}
	}
	content := raw["content"]
	if content == nil {
		content = ""
	}
	index := state.NextIndex
	state.NextIndex++

	return []messagesSSEEvent{
		{
			Name: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":        "tool_result",
					"tool_use_id": toolUseID,
					"content":     content,
				},
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
		{
			Name: "content_block_stop",
			Data: map[string]any{
				"type":        "content_block_stop",
				"index":       index,
				"message_id":  state.MessageID,
				"response_id": state.ResponseID,
			},
		},
	}, nil
}

func mapStreamStopReason(streamResult *codexclient.StreamResult, streamErr error) string {
	if streamErr != nil {
		var clientErr *codexclient.ClientError
		if errors.As(streamErr, &clientErr) {
			if clientErr.Kind == codexclient.KindStreamFailed && streamResult != nil {
				if strings.EqualFold(strings.TrimSpace(streamResult.FinalStatus), "failed") {
					return "error"
				}
			}
		}
		return "error"
	}
	if streamResult == nil {
		// Realtime callbacks receive response.completed before StreamEvents returns
		// its aggregate StreamResult. The completed event itself is the success signal.
		return "end_turn"
	}
	if strings.EqualFold(strings.TrimSpace(streamResult.FinalStatus), "completed") {
		return "end_turn"
	}
	return "error"
}

func streamStopReason(state *messagesStreamState, streamResult *codexclient.StreamResult, streamErr error) string {
	if streamErr != nil {
		return mapStreamStopReason(streamResult, streamErr)
	}
	if state != nil && state.ToolUseSeen {
		return "tool_use"
	}
	return mapStreamStopReason(streamResult, streamErr)
}

func responseIDFromStream(streamResult *codexclient.StreamResult) string {
	if streamResult == nil {
		return "msg_stream"
	}
	if strings.TrimSpace(streamResult.RequestID) != "" {
		return "msg_" + streamResult.RequestID
	}
	return "msg_stream"
}

func containsSSEEvent(events []messagesSSEEvent, name string) bool {
	for _, item := range events {
		if item.Name == name {
			return true
		}
	}
	return false
}

func asSSEEventNameList(events []messagesSSEEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}

func sequenceContainsOrder(sequence []string, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	current := 0
	for _, item := range sequence {
		if item == expected[current] {
			current++
			if current == len(expected) {
				return true
			}
		}
	}
	return false
}
