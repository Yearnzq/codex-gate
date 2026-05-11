package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"codex-gate/internal/codexclient"
)

const (
	maxMessagesRequestBodyBytes = 2 * 1024 * 1024
)

var (
	supportedMessagesFields = map[string]struct{}{
		"model":              {},
		"system":             {},
		"messages":           {},
		"max_tokens":         {},
		"stop_sequences":     {},
		"stream":             {},
		"tools":              {},
		"tool_choice":        {},
		"context_management": {},
		"metadata":           {},
		"output_config":      {},
		"thinking":           {},
		"reasoning":          {},
	}
	supportedMessageObjectFields = map[string]struct{}{
		"role":    {},
		"content": {},
	}
	supportedTextBlockFields = map[string]struct{}{
		"type":          {},
		"text":          {},
		"cache_control": {},
	}
	supportedSystemTextBlockFields = map[string]struct{}{
		"type":          {},
		"text":          {},
		"cache_control": {},
	}
	supportedImageBlockFields = map[string]struct{}{
		"type":          {},
		"source":        {},
		"cache_control": {},
	}
	supportedImageSourceFields = map[string]struct{}{
		"type":       {},
		"media_type": {},
		"data":       {},
	}
	supportedToolUseBlockFields = map[string]struct{}{
		"type":          {},
		"id":            {},
		"name":          {},
		"input":         {},
		"cache_control": {},
	}
	supportedToolResultBlockFields = map[string]struct{}{
		"type":          {},
		"tool_use_id":   {},
		"content":       {},
		"is_error":      {},
		"cache_control": {},
	}
	supportedToolDefinitionFields = map[string]struct{}{
		"name":          {},
		"description":   {},
		"input_schema":  {},
		"cache_control": {},
		"type":          {},
		"function":      {},
	}
	supportedOpenAIFunctionFields = map[string]struct{}{
		"name":        {},
		"description": {},
		"parameters":  {},
		"strict":      {},
	}
	supportedToolChoiceFields = map[string]struct{}{
		"type": {},
		"name": {},
	}
	supportedJSONSchemaFields = map[string]struct{}{
		"$schema":               {},
		"$id":                   {},
		"$ref":                  {},
		"$defs":                 {},
		"type":                  {},
		"title":                 {},
		"description":           {},
		"default":               {},
		"properties":            {},
		"patternProperties":     {},
		"required":              {},
		"additionalProperties":  {},
		"unevaluatedProperties": {},
		"definitions":           {},
		"enum":                  {},
		"const":                 {},
		"items":                 {},
		"prefixItems":           {},
		"contains":              {},
		"minimum":               {},
		"maximum":               {},
		"exclusiveMinimum":      {},
		"exclusiveMaximum":      {},
		"multipleOf":            {},
		"minItems":              {},
		"maxItems":              {},
		"uniqueItems":           {},
		"minContains":           {},
		"maxContains":           {},
		"minProperties":         {},
		"maxProperties":         {},
		"propertyNames":         {},
		"pattern":               {},
		"format":                {},
		"minLength":             {},
		"maxLength":             {},
		"anyOf":                 {},
		"oneOf":                 {},
		"allOf":                 {},
		"not":                   {},
		"if":                    {},
		"then":                  {},
		"else":                  {},
		"dependentRequired":     {},
		"dependentSchemas":      {},
		"examples":              {},
		"deprecated":            {},
		"readOnly":              {},
		"writeOnly":             {},
	}
	supportedJSONSchemaTypes = map[string]struct{}{
		"string":  {},
		"number":  {},
		"integer": {},
		"boolean": {},
		"object":  {},
		"array":   {},
		"null":    {},
	}
)

type messagesParseOptions struct {
	AllowStream bool
}

type messagesRequest struct {
	Model         string
	System        string
	Messages      []messagesMessage
	MaxTokens     int
	StopSequences []string
	Stream        bool
	Tools         []messagesToolDefinition
	ToolChoice    *messagesToolChoice
}

type messagesMessage struct {
	Role    string
	Content []messagesContentBlock
}

type messagesContentBlock struct {
	Type              string
	Text              string
	Source            *messagesImageSource
	ID                string
	Name              string
	Input             map[string]any
	ToolUseID         string
	ToolResultContent any
	IsError           *bool
}

type messagesImageSource struct {
	Type      string
	MediaType string
	Data      string
}

type messagesToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type messagesToolChoice struct {
	Type string
	Name string
}

type messagesAPIError struct {
	Status  int
	ErrType string
	Code    string
	Message string
}

func (s *Server) messagesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMessagesError(
			w,
			r,
			http.StatusMethodNotAllowed,
			"invalid_request_error",
			"method_not_allowed",
			"use POST /v1/messages",
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

	request, parseErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if parseErr != nil {
		s.writeMessagesError(w, r, parseErr.Status, parseErr.ErrType, parseErr.Code, parseErr.Message)
		return
	}
	if request.Stream {
		s.messagesStreamHandler(w, r, request)
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

	response, convertErr := convertCodexResponseToMessagesResponse(request, completion)
	if convertErr != nil {
		s.writeMessagesError(w, r, convertErr.Status, convertErr.ErrType, convertErr.Code, convertErr.Message)
		return
	}

	s.writeJSON(w, r, http.StatusOK, response)
}

func (s *Server) countTokensHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMessagesError(
			w,
			r,
			http.StatusMethodNotAllowed,
			"invalid_request_error",
			"method_not_allowed",
			"use POST /v1/messages/count_tokens",
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

	request, parseErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if parseErr != nil {
		s.writeMessagesError(w, r, parseErr.Status, parseErr.ErrType, parseErr.Code, parseErr.Message)
		return
	}

	s.writeJSON(
		w,
		r,
		http.StatusOK,
		map[string]any{
			"input_tokens": estimateInputTokens(request),
		},
	)
}

func (s *Server) writeMessagesError(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	errType string,
	code string,
	message string,
) {
	s.writeJSON(
		w,
		r,
		status,
		map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errType,
				"code":    code,
				"message": message,
			},
		},
	)
}

func readMessagesBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMessagesRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("empty body")
	}
	if len(body) > maxMessagesRequestBodyBytes {
		return nil, errors.New("body too large")
	}
	return body, nil
}

func parseMessagesRequest(body []byte, options messagesParseOptions) (messagesRequest, *messagesAPIError) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_json",
			"request body must be valid JSON",
		)
	}
	if len(raw) == 0 {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_request",
			"request object cannot be empty",
		)
	}

	if unsupported := unsupportedFields(raw, supportedMessagesFields); len(unsupported) > 0 {
		return messagesRequest{}, newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf(
				"unsupported top-level field(s): %s. supported fields: %s",
				strings.Join(unsupported, ", "),
				fieldList(supportedMessagesFields),
			),
		)
	}
	if apiErr := validateIgnoredCompatibilityFields(raw); apiErr != nil {
		return messagesRequest{}, apiErr
	}

	var request messagesRequest
	model, apiErr := decodeRequiredString(raw, "model")
	if apiErr != nil {
		return messagesRequest{}, apiErr
	}
	request.Model = model

	maxTokens, apiErr := decodeRequiredInt(raw, "max_tokens")
	if apiErr != nil {
		return messagesRequest{}, apiErr
	}
	if maxTokens <= 0 {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_request",
			"max_tokens must be a positive integer",
		)
	}
	request.MaxTokens = maxTokens

	if value, ok, err := decodeOptionalSystemPrompt(raw); err != nil {
		return messagesRequest{}, err
	} else if ok {
		request.System = value
	}

	if value, ok, err := decodeOptionalBool(raw, "stream"); err != nil {
		return messagesRequest{}, err
	} else if ok {
		request.Stream = value
	}
	if request.Stream && !options.AllowStream {
		return messagesRequest{}, newInvalidRequestError(
			"unsupported_stream",
			"stream=true is not supported on this endpoint; use stream=false",
		)
	}

	if values, ok, err := decodeOptionalStringArray(raw, "stop_sequences"); err != nil {
		return messagesRequest{}, err
	} else if ok {
		for index, item := range values {
			if strings.TrimSpace(item) == "" {
				return messagesRequest{}, newInvalidRequestError(
					"invalid_request",
					fmt.Sprintf("stop_sequences[%d] must be a non-empty string", index),
				)
			}
		}
		request.StopSequences = values
	}

	rawMessages, apiErr := decodeRequiredRawArray(raw, "messages")
	if apiErr != nil {
		return messagesRequest{}, apiErr
	}
	if len(rawMessages) == 0 {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_request",
			"messages must contain at least one message",
		)
	}
	request.Messages = make([]messagesMessage, 0, len(rawMessages))
	for index, rawMessage := range rawMessages {
		parsed, err := parseSingleMessage(rawMessage, index)
		if err != nil {
			return messagesRequest{}, err
		}
		if parsed.Role == "system" {
			systemText, systemErr := systemPromptFromMessageContent(parsed.Content, index)
			if systemErr != nil {
				return messagesRequest{}, systemErr
			}
			request.System = joinSystemPrompts(request.System, systemText)
			continue
		}
		if len(parsed.Content) == 0 {
			continue
		}
		request.Messages = append(request.Messages, parsed)
	}
	if len(request.Messages) == 0 {
		return messagesRequest{}, newInvalidRequestError(
			"invalid_request",
			"messages must contain at least one user or assistant message",
		)
	}

	if rawTools, ok, err := decodeOptionalRawArray(raw, "tools"); err != nil {
		return messagesRequest{}, err
	} else if ok {
		tools := make([]messagesToolDefinition, 0, len(rawTools))
		for index, rawTool := range rawTools {
			tool, parseErr := parseToolDefinition(rawTool, index)
			if parseErr != nil {
				return messagesRequest{}, parseErr
			}
			tools = append(tools, tool)
		}
		request.Tools = tools
	}

	if rawChoice, ok := raw["tool_choice"]; ok {
		choice, parseErr := parseToolChoice(rawChoice)
		if parseErr != nil {
			return messagesRequest{}, parseErr
		}
		request.ToolChoice = &choice
	}

	return request, nil
}

func validateIgnoredCompatibilityFields(raw map[string]json.RawMessage) *messagesAPIError {
	for _, field := range []string{"context_management", "metadata", "output_config", "thinking", "reasoning"} {
		value, ok := raw[field]
		if !ok {
			continue
		}
		if len(value) == 0 || string(value) == "null" {
			continue
		}
		var object map[string]any
		if err := json.Unmarshal(value, &object); err != nil {
			return newInvalidRequestError(
				"unsupported_field",
				fmt.Sprintf("%s must be an object when provided", field),
			)
		}
	}
	return nil
}

func decodeOptionalSystemPrompt(
	fields map[string]json.RawMessage,
) (string, bool, *messagesAPIError) {
	raw, ok := fields["system"]
	if !ok {
		return "", false, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, true, nil
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false, newInvalidRequestError(
			"invalid_request",
			"system must be a string or an array of text blocks",
		)
	}

	parts := make([]string, 0, len(blocks))
	for index, blockRaw := range blocks {
		var blockFields map[string]json.RawMessage
		if err := json.Unmarshal(blockRaw, &blockFields); err != nil {
			return "", false, newInvalidRequestError(
				"invalid_request",
				fmt.Sprintf("system[%d] must be an object", index),
			)
		}
		if unsupported := unsupportedFields(blockFields, supportedSystemTextBlockFields); len(unsupported) > 0 {
			return "", false, newInvalidRequestError(
				"unsupported_field",
				fmt.Sprintf(
					"system[%d] has unsupported field(s): %s",
					index,
					strings.Join(unsupported, ", "),
				),
			)
		}
		blockType, apiErr := decodeRequiredString(blockFields, "type")
		if apiErr != nil {
			return "", false, apiErr
		}
		if blockType != "text" {
			return "", false, newInvalidRequestError(
				"unsupported_content_type",
				fmt.Sprintf("system[%d].type=%q is unsupported; supported type: text", index, blockType),
			)
		}
		if err := validateOptionalIgnoredObjectField(
			blockFields,
			"cache_control",
			fmt.Sprintf("system[%d].cache_control", index),
		); err != nil {
			return "", false, err
		}
		blockText, apiErr := decodeRequiredString(blockFields, "text")
		if apiErr != nil {
			return "", false, apiErr
		}
		parts = append(parts, blockText)
	}

	return strings.Join(parts, "\n\n"), true, nil
}

func validateOptionalIgnoredObjectField(
	fields map[string]json.RawMessage,
	field string,
	label string,
) *messagesAPIError {
	raw, ok := fields[field]
	if !ok {
		return nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf("%s must be an object when provided", label),
		)
	}
	return nil
}

func parseSingleMessage(raw json.RawMessage, messageIndex int) (messagesMessage, *messagesAPIError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return messagesMessage{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("messages[%d] must be an object", messageIndex),
		)
	}
	if unsupported := unsupportedFields(fields, supportedMessageObjectFields); len(unsupported) > 0 {
		return messagesMessage{}, newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf(
				"messages[%d] has unsupported field(s): %s",
				messageIndex,
				strings.Join(unsupported, ", "),
			),
		)
	}

	role, apiErr := decodeRequiredString(fields, "role")
	if apiErr != nil {
		return messagesMessage{}, apiErr
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "user" && role != "assistant" && role != "system" {
		return messagesMessage{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("messages[%d].role must be one of: user, assistant, system", messageIndex),
		)
	}

	content, apiErr := parseMessageContent(fields, messageIndex)
	if apiErr != nil {
		return messagesMessage{}, apiErr
	}

	return messagesMessage{
		Role:    role,
		Content: content,
	}, nil
}

func parseMessageContent(
	fields map[string]json.RawMessage,
	messageIndex int,
) ([]messagesContentBlock, *messagesAPIError) {
	raw, ok := fields["content"]
	if !ok {
		return nil, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("messages[%d].content is required", messageIndex),
		)
	}

	var shorthand string
	if err := json.Unmarshal(raw, &shorthand); err == nil {
		if strings.TrimSpace(shorthand) == "" {
			return []messagesContentBlock{}, nil
		}
		return []messagesContentBlock{{Type: "text", Text: shorthand}}, nil
	}

	var rawContent []json.RawMessage
	if err := json.Unmarshal(raw, &rawContent); err != nil {
		return nil, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("messages[%d].content must be a string or an array", messageIndex),
		)
	}
	if len(rawContent) == 0 {
		return nil, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("messages[%d].content must include at least one item", messageIndex),
		)
	}
	content := make([]messagesContentBlock, 0, len(rawContent))
	for contentIndex, blockRaw := range rawContent {
		block, parseErr := parseContentBlock(blockRaw, messageIndex, contentIndex)
		if parseErr != nil {
			return nil, parseErr
		}
		if block.Type == "text" && strings.TrimSpace(block.Text) == "" {
			continue
		}
		content = append(content, block)
	}

	return content, nil
}

func systemPromptFromMessageContent(
	content []messagesContentBlock,
	messageIndex int,
) (string, *messagesAPIError) {
	parts := make([]string, 0, len(content))
	for index, block := range content {
		if block.Type != "text" {
			return "", newInvalidRequestError(
				"unsupported_content_type",
				fmt.Sprintf(
					"messages[%d].content[%d].type=%q is unsupported for system role; supported type: text",
					messageIndex,
					index,
					block.Type,
				),
			)
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

func joinSystemPrompts(existing string, addition string) string {
	if strings.TrimSpace(addition) == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	return existing + "\n\n" + addition
}

func parseContentBlock(
	raw json.RawMessage,
	messageIndex int,
	contentIndex int,
) (messagesContentBlock, *messagesAPIError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return messagesContentBlock{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf(
				"messages[%d].content[%d] must be an object",
				messageIndex,
				contentIndex,
			),
		)
	}

	blockType, apiErr := decodeRequiredString(fields, "type")
	if apiErr != nil {
		return messagesContentBlock{}, apiErr
	}

	switch blockType {
	case "text":
		if unsupported := unsupportedFields(fields, supportedTextBlockFields); len(unsupported) > 0 {
			return messagesContentBlock{}, unsupportedContentFieldError(
				messageIndex,
				contentIndex,
				unsupported,
			)
		}
		if err := validateOptionalIgnoredObjectField(
			fields,
			"cache_control",
			fmt.Sprintf("messages[%d].content[%d].cache_control", messageIndex, contentIndex),
		); err != nil {
			return messagesContentBlock{}, err
		}
		text, err := decodeRequiredString(fields, "text")
		if err != nil {
			return messagesContentBlock{}, err
		}
		return messagesContentBlock{Type: "text", Text: text}, nil
	case "image":
		if unsupported := unsupportedFields(fields, supportedImageBlockFields); len(unsupported) > 0 {
			return messagesContentBlock{}, unsupportedContentFieldError(
				messageIndex,
				contentIndex,
				unsupported,
			)
		}
		if err := validateOptionalIgnoredObjectField(
			fields,
			"cache_control",
			fmt.Sprintf("messages[%d].content[%d].cache_control", messageIndex, contentIndex),
		); err != nil {
			return messagesContentBlock{}, err
		}
		var rawSource map[string]json.RawMessage
		if err := decodeRequiredObject(fields, "source", &rawSource); err != nil {
			return messagesContentBlock{}, err
		}
		if unsupported := unsupportedFields(rawSource, supportedImageSourceFields); len(unsupported) > 0 {
			return messagesContentBlock{}, newInvalidRequestError(
				"unsupported_field",
				fmt.Sprintf(
					"messages[%d].content[%d].source has unsupported field(s): %s",
					messageIndex,
					contentIndex,
					strings.Join(unsupported, ", "),
				),
			)
		}
		sourceType, err := decodeRequiredString(rawSource, "type")
		if err != nil {
			return messagesContentBlock{}, err
		}
		mediaType, err := decodeRequiredString(rawSource, "media_type")
		if err != nil {
			return messagesContentBlock{}, err
		}
		data, err := decodeRequiredString(rawSource, "data")
		if err != nil {
			return messagesContentBlock{}, err
		}
		return messagesContentBlock{
			Type: "image",
			Source: &messagesImageSource{
				Type:      sourceType,
				MediaType: mediaType,
				Data:      data,
			},
		}, nil
	case "tool_use":
		if unsupported := unsupportedFields(fields, supportedToolUseBlockFields); len(unsupported) > 0 {
			return messagesContentBlock{}, unsupportedContentFieldError(
				messageIndex,
				contentIndex,
				unsupported,
			)
		}
		if err := validateOptionalIgnoredObjectField(
			fields,
			"cache_control",
			fmt.Sprintf("messages[%d].content[%d].cache_control", messageIndex, contentIndex),
		); err != nil {
			return messagesContentBlock{}, err
		}
		id, err := decodeRequiredString(fields, "id")
		if err != nil {
			return messagesContentBlock{}, err
		}
		name, err := decodeRequiredString(fields, "name")
		if err != nil {
			return messagesContentBlock{}, err
		}
		input, err := decodeRequiredAnyObject(fields, "input")
		if err != nil {
			return messagesContentBlock{}, err
		}
		return messagesContentBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: input,
		}, nil
	case "tool_result":
		if unsupported := unsupportedFields(fields, supportedToolResultBlockFields); len(unsupported) > 0 {
			return messagesContentBlock{}, unsupportedContentFieldError(
				messageIndex,
				contentIndex,
				unsupported,
			)
		}
		if err := validateOptionalIgnoredObjectField(
			fields,
			"cache_control",
			fmt.Sprintf("messages[%d].content[%d].cache_control", messageIndex, contentIndex),
		); err != nil {
			return messagesContentBlock{}, err
		}
		toolUseID, err := decodeRequiredString(fields, "tool_use_id")
		if err != nil {
			return messagesContentBlock{}, err
		}
		if _, ok := fields["content"]; !ok {
			return messagesContentBlock{}, newInvalidRequestError(
				"invalid_request",
				fmt.Sprintf(
					"messages[%d].content[%d].content is required for tool_result blocks",
					messageIndex,
					contentIndex,
				),
			)
		}
		var content any
		if err := json.Unmarshal(fields["content"], &content); err != nil {
			return messagesContentBlock{}, newInvalidRequestError(
				"invalid_request",
				fmt.Sprintf(
					"messages[%d].content[%d].content must be valid JSON",
					messageIndex,
					contentIndex,
				),
			)
		}
		var isError *bool
		if value, ok, parseErr := decodeOptionalBool(fields, "is_error"); parseErr != nil {
			return messagesContentBlock{}, parseErr
		} else if ok {
			isError = &value
		}
		return messagesContentBlock{
			Type:              "tool_result",
			ToolUseID:         toolUseID,
			ToolResultContent: content,
			IsError:           isError,
		}, nil
	default:
		return messagesContentBlock{}, newInvalidRequestError(
			"unsupported_content_type",
			fmt.Sprintf(
				"messages[%d].content[%d].type=%q is unsupported; supported types: text, image, tool_use, tool_result",
				messageIndex,
				contentIndex,
				blockType,
			),
		)
	}
}

func parseToolDefinition(raw json.RawMessage, index int) (messagesToolDefinition, *messagesAPIError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return messagesToolDefinition{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("tools[%d] must be an object", index),
		)
	}
	if _, ok := fields["function"]; ok {
		return parseOpenAIFunctionToolDefinition(fields, index)
	}
	if _, ok := fields["type"]; ok {
		return messagesToolDefinition{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("tools[%d].function is required when tools[%d].type is provided", index, index),
		)
	}

	if unsupported := unsupportedFields(fields, supportedToolDefinitionFields); len(unsupported) > 0 {
		return messagesToolDefinition{}, newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf(
				"tools[%d] has unsupported field(s): %s",
				index,
				strings.Join(unsupported, ", "),
			),
		)
	}
	if err := validateOptionalIgnoredObjectField(
		fields,
		"cache_control",
		fmt.Sprintf("tools[%d].cache_control", index),
	); err != nil {
		return messagesToolDefinition{}, err
	}

	name, apiErr := decodeRequiredString(fields, "name")
	if apiErr != nil {
		return messagesToolDefinition{}, apiErr
	}
	if strings.TrimSpace(name) == "" {
		return messagesToolDefinition{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("tools[%d].name must be non-empty", index),
		)
	}

	var description string
	if value, ok, err := decodeOptionalString(fields, "description"); err != nil {
		return messagesToolDefinition{}, err
	} else if ok {
		description = value
	}

	inputSchema, apiErr := decodeRequiredAnyObject(fields, "input_schema")
	if apiErr != nil {
		return messagesToolDefinition{}, apiErr
	}
	if schemaErr := validateToolInputSchema(inputSchema, fmt.Sprintf("tools[%d].input_schema", index)); schemaErr != nil {
		return messagesToolDefinition{}, schemaErr
	}
	return messagesToolDefinition{
		Name:        name,
		Description: description,
		InputSchema: stripIgnoredJSONSchemaMetadata(inputSchema),
	}, nil
}

func parseOpenAIFunctionToolDefinition(
	fields map[string]json.RawMessage,
	index int,
) (messagesToolDefinition, *messagesAPIError) {
	if unsupported := unsupportedFields(fields, supportedToolDefinitionFields); len(unsupported) > 0 {
		return messagesToolDefinition{}, newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf(
				"tools[%d] has unsupported field(s): %s",
				index,
				strings.Join(unsupported, ", "),
			),
		)
	}
	if err := validateOptionalIgnoredObjectField(
		fields,
		"cache_control",
		fmt.Sprintf("tools[%d].cache_control", index),
	); err != nil {
		return messagesToolDefinition{}, err
	}
	toolType, apiErr := decodeRequiredString(fields, "type")
	if apiErr != nil {
		return messagesToolDefinition{}, apiErr
	}
	if toolType != "function" {
		return messagesToolDefinition{}, newInvalidRequestError(
			"unsupported_tool_type",
			fmt.Sprintf("tools[%d].type=%q is unsupported; supported type: function", index, toolType),
		)
	}

	var functionFields map[string]json.RawMessage
	if err := decodeRequiredObject(fields, "function", &functionFields); err != nil {
		return messagesToolDefinition{}, err
	}
	if unsupported := unsupportedFields(functionFields, supportedOpenAIFunctionFields); len(unsupported) > 0 {
		return messagesToolDefinition{}, newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf(
				"tools[%d].function has unsupported field(s): %s",
				index,
				strings.Join(unsupported, ", "),
			),
		)
	}

	name, apiErr := decodeRequiredString(functionFields, "name")
	if apiErr != nil {
		return messagesToolDefinition{}, apiErr
	}
	if strings.TrimSpace(name) == "" {
		return messagesToolDefinition{}, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("tools[%d].function.name must be non-empty", index),
		)
	}

	var description string
	if value, ok, err := decodeOptionalString(functionFields, "description"); err != nil {
		return messagesToolDefinition{}, err
	} else if ok {
		description = value
	}
	if _, ok, err := decodeOptionalBool(functionFields, "strict"); err != nil {
		return messagesToolDefinition{}, err
	} else if ok {
		// strict is a client-side schema directive for OpenAI-style tool declarations.
		// Codex receives the normalized schema only.
	}

	parameters, apiErr := decodeRequiredAnyObject(functionFields, "parameters")
	if apiErr != nil {
		return messagesToolDefinition{}, apiErr
	}
	if schemaErr := validateToolInputSchema(parameters, fmt.Sprintf("tools[%d].function.parameters", index)); schemaErr != nil {
		return messagesToolDefinition{}, schemaErr
	}
	return messagesToolDefinition{
		Name:        name,
		Description: description,
		InputSchema: stripIgnoredJSONSchemaMetadata(parameters),
	}, nil
}

func parseToolChoice(raw json.RawMessage) (messagesToolChoice, *messagesAPIError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return messagesToolChoice{}, newInvalidRequestError(
			"invalid_request",
			"tool_choice must be an object",
		)
	}
	if unsupported := unsupportedFields(fields, supportedToolChoiceFields); len(unsupported) > 0 {
		return messagesToolChoice{}, newInvalidRequestError(
			"unsupported_field",
			fmt.Sprintf(
				"tool_choice has unsupported field(s): %s",
				strings.Join(unsupported, ", "),
			),
		)
	}

	choiceType, apiErr := decodeRequiredString(fields, "type")
	if apiErr != nil {
		return messagesToolChoice{}, apiErr
	}
	choiceType = strings.ToLower(strings.TrimSpace(choiceType))
	switch choiceType {
	case "auto", "any", "none":
		if _, ok := fields["name"]; ok {
			return messagesToolChoice{}, newInvalidRequestError(
				"invalid_request",
				"tool_choice.name is only allowed when tool_choice.type is \"tool\"",
			)
		}
		return messagesToolChoice{Type: choiceType}, nil
	case "tool":
		name, err := decodeRequiredString(fields, "name")
		if err != nil {
			return messagesToolChoice{}, err
		}
		if strings.TrimSpace(name) == "" {
			return messagesToolChoice{}, newInvalidRequestError(
				"invalid_request",
				"tool_choice.name must be non-empty when tool_choice.type is \"tool\"",
			)
		}
		return messagesToolChoice{Type: choiceType, Name: name}, nil
	default:
		return messagesToolChoice{}, newInvalidRequestError(
			"unsupported_field",
			"tool_choice.type must be one of: auto, any, none, tool",
		)
	}
}

func convertMessagesRequestToCodexPayload(request messagesRequest) map[string]any {
	input := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		convertedContent := make([]any, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				convertedContent = append(
					convertedContent,
					map[string]any{
						"type": codexTextContentTypeForRole(message.Role),
						"text": block.Text,
					},
				)
			case "image":
				convertedContent = append(
					convertedContent,
					map[string]any{
						"type": "input_image",
						"source": map[string]any{
							"type":       block.Source.Type,
							"media_type": block.Source.MediaType,
							"data":       block.Source.Data,
						},
					},
				)
			case "tool_use":
				convertedContent = append(
					convertedContent,
					map[string]any{
						"type":      "tool_call",
						"id":        block.ID,
						"name":      block.Name,
						"arguments": block.Input,
					},
				)
			case "tool_result":
				item := map[string]any{
					"type":         "tool_result",
					"tool_call_id": block.ToolUseID,
					"content":      block.ToolResultContent,
				}
				if block.IsError != nil {
					item["is_error"] = *block.IsError
				}
				convertedContent = append(convertedContent, item)
			}
		}
		input = append(
			input,
			map[string]any{
				"role":    message.Role,
				"content": convertedContent,
			},
		)
	}

	payload := map[string]any{
		"model":             codexUpstreamModelName(request.Model),
		"max_output_tokens": request.MaxTokens,
		"input":             input,
	}
	if request.System != "" {
		payload["instructions"] = request.System
	}
	if len(request.StopSequences) > 0 {
		payload["stop"] = request.StopSequences
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(
				tools,
				map[string]any{
					"type":        "function",
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  tool.InputSchema,
				},
			)
		}
		payload["tools"] = tools
	}
	if request.ToolChoice != nil {
		payload["tool_choice"] = codexToolChoice(*request.ToolChoice)
	}
	return payload
}

func codexToolChoice(choice messagesToolChoice) any {
	switch strings.ToLower(strings.TrimSpace(choice.Type)) {
	case "auto", "none":
		return choice.Type
	case "any":
		return "required"
	case "tool":
		return map[string]any{
			"type": "function",
			"name": choice.Name,
		}
	default:
		return choice.Type
	}
}

func codexTextContentTypeForRole(role string) string {
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
}

func (s *Server) convertMessagesRequestToCodexPayload(request messagesRequest) map[string]any {
	payload := convertMessagesRequestToCodexPayload(request)
	if upstreamModel := strings.TrimSpace(s.config.UpstreamModel); upstreamModel != "" {
		payload["model"] = upstreamModel
	}
	return payload
}

func codexUpstreamModelName(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return trimmed
	}

	for _, prefix := range []string{"anthropic/", "openai/", "codex/"} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	for strings.HasSuffix(trimmed, "[1m]") {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "[1m]"))
	}

	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "haiku"):
		return modelMapEnv("CODEX_MODEL_MAP_HAIKU", "gpt-5.4-mini")
	case strings.Contains(lower, "sonnet"):
		return modelMapEnv("CODEX_MODEL_MAP_SONNET", "gpt-5.5")
	case strings.Contains(lower, "opus"):
		return modelMapEnv("CODEX_MODEL_MAP_OPUS", "gpt-5.5")
	default:
		return trimmed
	}
}

func modelMapEnv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		return value
	}
	return fallback
}

func convertCodexResponseToMessagesResponse(
	request messagesRequest,
	completion *codexclient.CompletionResult,
) (map[string]any, *messagesAPIError) {
	if completion == nil || completion.Response == nil {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: "codex response payload is empty",
		}
	}

	status, _ := completion.Response["status"].(string)
	toolUseSeen := false
	refusalSeen := false

	content := make([]map[string]any, 0)
	rawOutput, ok := completion.Response["output"]
	if !ok {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: "codex response.output is required",
		}
	}
	items, ok := rawOutput.([]any)
	if !ok {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: "codex response.output must be an array",
		}
	}
	if len(items) == 0 {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: "codex response.output must not be empty",
		}
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, &messagesAPIError{
				Status:  http.StatusBadGateway,
				ErrType: "api_error",
				Code:    "bad_upstream_response",
				Message: fmt.Sprintf("codex output[%d] must be an object", index),
			}
		}

		itemType, _ := item["type"].(string)
		switch itemType {
		case "message":
			messageBlocks, sawToolUse, sawRefusal, err := convertCodexMessageContent(item, index)
			if err != nil {
				return nil, err
			}
			if sawToolUse {
				toolUseSeen = true
			}
			if sawRefusal {
				refusalSeen = true
			}
			content = append(content, messageBlocks...)
		case "tool_call", "function_call":
			converted, err := convertCodexToolCall(item, index)
			if err != nil {
				return nil, err
			}
			toolUseSeen = true
			content = append(content, converted)
		case "tool_result":
			converted, err := convertCodexToolResult(item, index)
			if err != nil {
				return nil, err
			}
			content = append(content, converted)
		case "refusal":
			message, _ := item["message"].(string)
			if strings.TrimSpace(message) == "" {
				message = "request refused by upstream policy"
			}
			refusalSeen = true
			content = append(content, map[string]any{"type": "text", "text": message})
		default:
			return nil, &messagesAPIError{
				Status:  http.StatusBadGateway,
				ErrType: "api_error",
				Code:    "unsupported_upstream_output",
				Message: fmt.Sprintf("codex output[%d].type=%q is not supported", index, itemType),
			}
		}
	}

	stopReason := "end_turn"
	if refusalSeen {
		stopReason = "refusal"
	} else if toolUseSeen || strings.EqualFold(status, "requires_action") {
		stopReason = "tool_use"
	}

	responseID := responseIDFromCodex(completion)
	return map[string]any{
		"id":            responseID,
		"type":          "message",
		"role":          "assistant",
		"model":         request.Model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  estimateInputTokens(request),
			"output_tokens": estimateOutputTokens(content),
		},
	}, nil
}

func convertCodexMessageContent(
	item map[string]any,
	outputIndex int,
) ([]map[string]any, bool, bool, *messagesAPIError) {
	rawContent, ok := item["content"]
	if !ok {
		return nil, false, false, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: fmt.Sprintf("codex output[%d].content is required", outputIndex),
		}
	}

	entries, ok := rawContent.([]any)
	if !ok {
		return nil, false, false, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: fmt.Sprintf("codex output[%d].content must be an array", outputIndex),
		}
	}

	converted := make([]map[string]any, 0, len(entries))
	toolUseSeen := false
	refusalSeen := false
	for contentIndex, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, false, false, &messagesAPIError{
				Status:  http.StatusBadGateway,
				ErrType: "api_error",
				Code:    "bad_upstream_response",
				Message: fmt.Sprintf(
					"codex output[%d].content[%d] must be an object",
					outputIndex,
					contentIndex,
				),
			}
		}
		entryType, _ := entry["type"].(string)
		switch entryType {
		case "output_text", "text":
			text, _ := entry["text"].(string)
			converted = append(converted, map[string]any{"type": "text", "text": text})
		case "tool_call", "function_call":
			toolBlock, err := convertCodexToolCall(entry, contentIndex)
			if err != nil {
				return nil, false, false, err
			}
			toolUseSeen = true
			converted = append(converted, toolBlock)
		case "tool_result":
			toolResult, err := convertCodexToolResult(entry, contentIndex)
			if err != nil {
				return nil, false, false, err
			}
			converted = append(converted, toolResult)
		case "refusal":
			message, _ := entry["message"].(string)
			if strings.TrimSpace(message) == "" {
				message = "request refused by upstream policy"
			}
			refusalSeen = true
			converted = append(converted, map[string]any{"type": "text", "text": message})
		default:
			return nil, false, false, &messagesAPIError{
				Status:  http.StatusBadGateway,
				ErrType: "api_error",
				Code:    "unsupported_upstream_output",
				Message: fmt.Sprintf(
					"codex output[%d].content[%d].type=%q is unsupported",
					outputIndex,
					contentIndex,
					entryType,
				),
			}
		}
	}
	return converted, toolUseSeen, refusalSeen, nil
}

func convertCodexToolCall(item map[string]any, index int) (map[string]any, *messagesAPIError) {
	id, _ := item["id"].(string)
	if strings.TrimSpace(id) == "" {
		id, _ = item["call_id"].(string)
	}
	name, _ := item["name"].(string)
	arguments, ok := item["arguments"].(map[string]any)
	if !ok {
		if rawArgs, isString := item["arguments"].(string); isString && strings.TrimSpace(rawArgs) != "" {
			if err := json.Unmarshal([]byte(rawArgs), &arguments); err != nil {
				return nil, &messagesAPIError{
					Status:  http.StatusBadGateway,
					ErrType: "api_error",
					Code:    "bad_upstream_response",
					Message: fmt.Sprintf("codex function_call output[%d].arguments must be a JSON object", index),
				}
			}
		} else {
			arguments = map[string]any{}
		}
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: fmt.Sprintf("codex tool_call/function_call output[%d] must include non-empty id/call_id and name", index),
		}
	}
	return map[string]any{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": arguments,
	}, nil
}

func convertCodexToolResult(item map[string]any, index int) (map[string]any, *messagesAPIError) {
	toolCallID, _ := item["tool_call_id"].(string)
	if strings.TrimSpace(toolCallID) == "" {
		return nil, &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: fmt.Sprintf("codex tool_result output[%d] must include tool_call_id", index),
		}
	}

	content, ok := item["content"]
	if !ok {
		content = ""
	}
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": toolCallID,
		"content":     content,
	}, nil
}

func responseIDFromCodex(completion *codexclient.CompletionResult) string {
	if completion == nil {
		return "msg_compat"
	}
	if responseID, ok := completion.Response["id"].(string); ok && strings.TrimSpace(responseID) != "" {
		return responseID
	}
	if strings.TrimSpace(completion.RequestID) != "" {
		return "msg_" + completion.RequestID
	}
	return "msg_compat"
}

func mapCodexClientError(err error) *messagesAPIError {
	var clientErr *codexclient.ClientError
	if !errors.As(err, &clientErr) {
		return &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "upstream_error",
			Message: "codex upstream request failed",
		}
	}

	message := strings.TrimSpace(clientErr.Message)
	if message == "" {
		message = "codex upstream request failed"
	}

	switch clientErr.Kind {
	case codexclient.KindUnauthorized:
		return &messagesAPIError{
			Status:  http.StatusUnauthorized,
			ErrType: "authentication_error",
			Code:    "unauthorized",
			Message: message,
		}
	case codexclient.KindForbidden:
		return &messagesAPIError{
			Status:  http.StatusForbidden,
			ErrType: "permission_error",
			Code:    "forbidden",
			Message: message,
		}
	case codexclient.KindRateLimited:
		return &messagesAPIError{
			Status:  http.StatusTooManyRequests,
			ErrType: "rate_limit_error",
			Code:    "rate_limited",
			Message: message,
		}
	case codexclient.KindTimeout, codexclient.KindNetworkTimeout:
		return &messagesAPIError{
			Status:  http.StatusGatewayTimeout,
			ErrType: "api_error",
			Code:    "upstream_timeout",
			Message: message,
		}
	case codexclient.KindMalformedResponse:
		return &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "bad_upstream_response",
			Message: message,
		}
	default:
		return &messagesAPIError{
			Status:  http.StatusBadGateway,
			ErrType: "api_error",
			Code:    "upstream_error",
			Message: message,
		}
	}
}

func estimateInputTokens(request messagesRequest) int {
	total := 0
	total += estimateTextTokens(request.Model)
	total += estimateTextTokens(request.System)
	total += 8

	for _, message := range request.Messages {
		total += estimateTextTokens(message.Role) + 4
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				total += estimateTextTokens(block.Text)
			case "image":
				total += conservativeTokens(block.Source)
			case "tool_use":
				total += estimateTextTokens(block.Name)
				total += conservativeTokens(block.Input)
			case "tool_result":
				total += conservativeTokens(block.ToolResultContent)
				total += estimateTextTokens(block.ToolUseID)
			}
		}
	}
	for _, stop := range request.StopSequences {
		total += estimateTextTokens(stop)
	}
	for _, tool := range request.Tools {
		total += estimateTextTokens(tool.Name)
		total += estimateTextTokens(tool.Description)
		total += conservativeTokens(tool.InputSchema)
	}
	if request.ToolChoice != nil {
		total += estimateTextTokens(request.ToolChoice.Type)
		total += estimateTextTokens(request.ToolChoice.Name)
	}
	if total < 1 {
		return 1
	}
	return total
}

func estimateOutputTokens(content []map[string]any) int {
	total := 0
	for _, block := range content {
		total += conservativeTokens(block)
	}
	if total < 1 {
		return 1
	}
	return total
}

func conservativeTokens(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 1
	}
	return estimateTextTokens(string(encoded))
}

func estimateTextTokens(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	ascii := 0
	cjk := 0
	otherNonASCII := 0
	for _, r := range trimmed {
		switch {
		case isCJKRune(r):
			cjk++
		case r <= utf8.RuneSelf:
			ascii++
		default:
			otherNonASCII++
		}
	}
	// Count text conservatively because Claude Code uses /count_tokens to decide
	// when to compact. Undercounting is worse than early compaction here.
	tokens := ((ascii + 2) / 3) + cjk + ((otherNonASCII + 1) / 2)
	tokens = (tokens*6 + 4) / 5
	if tokens < 1 {
		return 1
	}
	return tokens
}

func isCJKRune(r rune) bool {
	return (r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0x2CEB0 && r <= 0x2EBEF) ||
		(r >= 0x30000 && r <= 0x3134F)
}

func unsupportedContentFieldError(
	messageIndex int,
	contentIndex int,
	unsupported []string,
) *messagesAPIError {
	return newInvalidRequestError(
		"unsupported_field",
		fmt.Sprintf(
			"messages[%d].content[%d] has unsupported field(s): %s",
			messageIndex,
			contentIndex,
			strings.Join(unsupported, ", "),
		),
	)
}

func decodeRequiredRawArray(
	fields map[string]json.RawMessage,
	field string,
) ([]json.RawMessage, *messagesAPIError) {
	if raw, ok := fields[field]; ok {
		var value []json.RawMessage
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, newInvalidRequestError(
				"invalid_request",
				fmt.Sprintf("%s must be an array", field),
			)
		}
		return value, nil
	}
	return nil, newInvalidRequestError(
		"invalid_request",
		fmt.Sprintf("%s is required", field),
	)
}

func decodeOptionalRawArray(
	fields map[string]json.RawMessage,
	field string,
) ([]json.RawMessage, bool, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return nil, false, nil
	}
	var value []json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be an array", field),
		)
	}
	return value, true, nil
}

func decodeRequiredString(
	fields map[string]json.RawMessage,
	field string,
) (string, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return "", newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s is required", field),
		)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be a string", field),
		)
	}
	return value, nil
}

func decodeOptionalString(
	fields map[string]json.RawMessage,
	field string,
) (string, bool, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be a string", field),
		)
	}
	return value, true, nil
}

func decodeRequiredInt(fields map[string]json.RawMessage, field string) (int, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return 0, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s is required", field),
		)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be an integer", field),
		)
	}
	return value, nil
}

func decodeOptionalBool(
	fields map[string]json.RawMessage,
	field string,
) (bool, bool, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be boolean", field),
		)
	}
	return value, true, nil
}

func decodeOptionalStringArray(
	fields map[string]json.RawMessage,
	field string,
) ([]string, bool, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return nil, false, nil
	}
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be an array of strings", field),
		)
	}
	return value, true, nil
}

func decodeRequiredObject(
	fields map[string]json.RawMessage,
	field string,
	target *map[string]json.RawMessage,
) *messagesAPIError {
	raw, ok := fields[field]
	if !ok {
		return newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s is required", field),
		)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be an object", field),
		)
	}
	return nil
}

func decodeRequiredAnyObject(
	fields map[string]json.RawMessage,
	field string,
) (map[string]any, *messagesAPIError) {
	raw, ok := fields[field]
	if !ok {
		return nil, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s is required", field),
		)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, newInvalidRequestError(
			"invalid_request",
			fmt.Sprintf("%s must be an object", field),
		)
	}
	return value, nil
}

func unsupportedFields(
	fields map[string]json.RawMessage,
	allowed map[string]struct{},
) []string {
	result := make([]string, 0)
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func fieldList(allowed map[string]struct{}) string {
	fields := make([]string, 0, len(allowed))
	for key := range allowed {
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return strings.Join(fields, ", ")
}

func validateToolInputSchema(schema map[string]any, path string) *messagesAPIError {
	if len(schema) == 0 {
		return newInvalidRequestError(
			"unsupported_tool_schema",
			path+" must define a JSON schema object",
		)
	}
	typeValue, ok := schema["type"].(string)
	if !ok || strings.TrimSpace(typeValue) == "" {
		return newInvalidRequestError(
			"unsupported_tool_schema",
			path+".type must be a non-empty string",
		)
	}
	if typeValue != "object" {
		return newInvalidRequestError(
			"unsupported_tool_schema",
			path+".type must be \"object\"",
		)
	}
	if err := validateJSONSchemaNode(path, schema); err != nil {
		return newInvalidRequestError("unsupported_tool_schema", err.Error())
	}
	return nil
}

func validateJSONSchemaNode(path string, node map[string]any) error {
	unsupported := make([]string, 0)
	for key := range node {
		if _, ok := supportedJSONSchemaFields[key]; !ok {
			unsupported = append(unsupported, key)
		}
	}
	sort.Strings(unsupported)
	if len(unsupported) > 0 {
		return fmt.Errorf("%s has unsupported schema field(s): %s", path, strings.Join(unsupported, ", "))
	}

	if schemaURI, ok := node["$schema"]; ok {
		if _, isString := schemaURI.(string); !isString {
			return fmt.Errorf("%s.$schema must be a string", path)
		}
	}
	for _, stringKeyword := range []string{"$id", "$ref", "title", "description", "pattern", "format"} {
		if value, ok := node[stringKeyword]; ok {
			if _, isString := value.(string); !isString {
				return fmt.Errorf("%s.%s must be a string", path, stringKeyword)
			}
		}
	}

	if schemaType, ok := node["type"]; ok {
		if err := validateJSONSchemaType(path+".type", schemaType); err != nil {
			return err
		}
	}

	if requiredValue, ok := node["required"]; ok {
		requiredList, isList := requiredValue.([]any)
		if !isList {
			return fmt.Errorf("%s.required must be an array", path)
		}
		for index, item := range requiredList {
			if _, isString := item.(string); !isString {
				return fmt.Errorf("%s.required[%d] must be a string", path, index)
			}
		}
	}

	if enumValue, ok := node["enum"]; ok {
		if _, isList := enumValue.([]any); !isList {
			return fmt.Errorf("%s.enum must be an array", path)
		}
	}

	for _, boolKeyword := range []string{"uniqueItems", "deprecated", "readOnly", "writeOnly"} {
		if boolValue, ok := node[boolKeyword]; ok {
			if _, isBool := boolValue.(bool); !isBool {
				return fmt.Errorf("%s.%s must be a boolean", path, boolKeyword)
			}
		}
	}

	for _, numericKeyword := range []string{"minimum", "maximum", "multipleOf"} {
		if numericValue, ok := node[numericKeyword]; ok {
			number, isNumber := numericValue.(float64)
			if !isNumber {
				return fmt.Errorf("%s.%s must be a number", path, numericKeyword)
			}
			if numericKeyword == "multipleOf" && number <= 0 {
				return fmt.Errorf("%s.%s must be positive", path, numericKeyword)
			}
		}
	}
	for _, exclusiveKeyword := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
		if exclusiveValue, ok := node[exclusiveKeyword]; ok {
			switch exclusiveValue.(type) {
			case bool, float64:
			default:
				return fmt.Errorf("%s.%s must be a boolean or number", path, exclusiveKeyword)
			}
		}
	}
	for _, integerKeyword := range []string{
		"minItems",
		"maxItems",
		"minContains",
		"maxContains",
		"minProperties",
		"maxProperties",
	} {
		if integerValue, ok := node[integerKeyword]; ok {
			number, isNumber := integerValue.(float64)
			if !isNumber || number < 0 || number != float64(int64(number)) {
				return fmt.Errorf("%s.%s must be a non-negative integer", path, integerKeyword)
			}
		}
	}
	for _, stringLengthKeyword := range []string{"minLength", "maxLength"} {
		if lengthValue, ok := node[stringLengthKeyword]; ok {
			number, isNumber := lengthValue.(float64)
			if !isNumber || number < 0 || number != float64(int64(number)) {
				return fmt.Errorf("%s.%s must be a non-negative integer", path, stringLengthKeyword)
			}
		}
	}
	if additionalValue, ok := node["additionalProperties"]; ok {
		switch typed := additionalValue.(type) {
		case bool:
		case map[string]any:
			if err := validateJSONSchemaNode(path+".additionalProperties", typed); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.additionalProperties must be boolean or object", path)
		}
	}
	if unevaluatedValue, ok := node["unevaluatedProperties"]; ok {
		switch typed := unevaluatedValue.(type) {
		case bool:
		case map[string]any:
			if err := validateJSONSchemaNode(path+".unevaluatedProperties", typed); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.unevaluatedProperties must be boolean or object", path)
		}
	}

	if propertiesValue, ok := node["properties"]; ok {
		if err := validateJSONSchemaSchemaMap(path+".properties", propertiesValue); err != nil {
			return err
		}
	}
	for _, schemaMapKeyword := range []string{
		"patternProperties",
		"$defs",
		"definitions",
		"dependentSchemas",
	} {
		if value, ok := node[schemaMapKeyword]; ok {
			if err := validateJSONSchemaSchemaMap(path+"."+schemaMapKeyword, value); err != nil {
				return err
			}
		}
	}

	if itemsValue, ok := node["items"]; ok {
		if err := validateJSONSchemaObjectOrList(path+".items", itemsValue); err != nil {
			return err
		}
	}
	if prefixItemsValue, ok := node["prefixItems"]; ok {
		if err := validateJSONSchemaVariantList(path+".prefixItems", prefixItemsValue); err != nil {
			return err
		}
	}
	if propertyNamesValue, ok := node["propertyNames"]; ok {
		propertyNamesSchema, isSchema := propertyNamesValue.(map[string]any)
		if !isSchema {
			return fmt.Errorf("%s.propertyNames must be an object schema", path)
		}
		if err := validateJSONSchemaNode(path+".propertyNames", propertyNamesSchema); err != nil {
			return err
		}
	}
	if containsValue, ok := node["contains"]; ok {
		containsSchema, isSchema := containsValue.(map[string]any)
		if !isSchema {
			return fmt.Errorf("%s.contains must be an object schema", path)
		}
		if err := validateJSONSchemaNode(path+".contains", containsSchema); err != nil {
			return err
		}
	}
	for _, schemaKeyword := range []string{"not", "if", "then", "else"} {
		if value, ok := node[schemaKeyword]; ok {
			schema, isSchema := value.(map[string]any)
			if !isSchema {
				return fmt.Errorf("%s.%s must be an object schema", path, schemaKeyword)
			}
			if err := validateJSONSchemaNode(path+"."+schemaKeyword, schema); err != nil {
				return err
			}
		}
	}
	for _, variantKeyword := range []string{"anyOf", "oneOf", "allOf"} {
		if value, ok := node[variantKeyword]; ok {
			if err := validateJSONSchemaVariantList(path+"."+variantKeyword, value); err != nil {
				return err
			}
		}
	}
	if dependentRequiredValue, ok := node["dependentRequired"]; ok {
		dependentRequired, isObject := dependentRequiredValue.(map[string]any)
		if !isObject {
			return fmt.Errorf("%s.dependentRequired must be an object", path)
		}
		for propertyName, rawList := range dependentRequired {
			list, isList := rawList.([]any)
			if !isList {
				return fmt.Errorf("%s.dependentRequired.%s must be an array", path, propertyName)
			}
			for index, item := range list {
				if _, isString := item.(string); !isString {
					return fmt.Errorf("%s.dependentRequired.%s[%d] must be a string", path, propertyName, index)
				}
			}
		}
	}
	if examplesValue, ok := node["examples"]; ok {
		if _, isList := examplesValue.([]any); !isList {
			return fmt.Errorf("%s.examples must be an array", path)
		}
	}
	return nil
}

func validateJSONSchemaType(path string, value any) error {
	switch typed := value.(type) {
	case string:
		return validateJSONSchemaTypeName(path, typed)
	case []any:
		if len(typed) == 0 {
			return fmt.Errorf("%s must be a non-empty string or array of strings", path)
		}
		for index, item := range typed {
			typeName, isString := item.(string)
			if !isString {
				return fmt.Errorf("%s[%d] must be a string", path, index)
			}
			if err := validateJSONSchemaTypeName(fmt.Sprintf("%s[%d]", path, index), typeName); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s must be a string or array of strings", path)
	}
}

func validateJSONSchemaTypeName(path string, value string) error {
	typeName := strings.TrimSpace(value)
	if typeName == "" {
		return fmt.Errorf("%s must be non-empty", path)
	}
	if _, supported := supportedJSONSchemaTypes[typeName]; !supported {
		return fmt.Errorf("%s=%q is unsupported", path, typeName)
	}
	return nil
}

func validateJSONSchemaSchemaMap(path string, value any) error {
	schemas, isObject := value.(map[string]any)
	if !isObject {
		return fmt.Errorf("%s must be an object", path)
	}
	for propertyName, propertyRaw := range schemas {
		propertySchema, isSchema := propertyRaw.(map[string]any)
		if !isSchema {
			return fmt.Errorf("%s.%s must be an object schema", path, propertyName)
		}
		if err := validateJSONSchemaNode(path+"."+propertyName, propertySchema); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONSchemaObjectOrList(path string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		return validateJSONSchemaNode(path, typed)
	case []any:
		return validateJSONSchemaVariantList(path, typed)
	default:
		return fmt.Errorf("%s must be an object schema or array of object schemas", path)
	}
}

func validateJSONSchemaVariantList(path string, value any) error {
	variants, isList := value.([]any)
	if !isList || len(variants) == 0 {
		return fmt.Errorf("%s must be a non-empty array", path)
	}
	for index, item := range variants {
		schema, isSchema := item.(map[string]any)
		if !isSchema {
			return fmt.Errorf("%s[%d] must be an object schema", path, index)
		}
		if err := validateJSONSchemaNode(fmt.Sprintf("%s[%d]", path, index), schema); err != nil {
			return err
		}
	}
	return nil
}

func stripIgnoredJSONSchemaMetadata(schema map[string]any) map[string]any {
	stripped, _ := stripIgnoredJSONSchemaMetadataValue(schema).(map[string]any)
	return stripped
}

func stripIgnoredJSONSchemaMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "$schema" || key == "default" {
				continue
			}
			copied[key] = stripIgnoredJSONSchemaMetadataValue(item)
		}
		return copied
	case []any:
		copied := make([]any, 0, len(typed))
		for _, item := range typed {
			copied = append(copied, stripIgnoredJSONSchemaMetadataValue(item))
		}
		return copied
	default:
		return value
	}
}

func newInvalidRequestError(code string, message string) *messagesAPIError {
	return &messagesAPIError{
		Status:  http.StatusBadRequest,
		ErrType: "invalid_request_error",
		Code:    code,
		Message: message,
	}
}
