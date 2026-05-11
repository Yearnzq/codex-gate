package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"codex-gate/internal/codexclient"
)

type fakeGatewayCodexClient struct {
	createFunc func(
		ctx context.Context,
		payload map[string]any,
		options codexclient.CallOptions,
	) (*codexclient.CompletionResult, error)
	streamFunc func(
		ctx context.Context,
		payload map[string]any,
		options codexclient.CallOptions,
	) (*codexclient.StreamResult, error)
}

func (f *fakeGatewayCodexClient) Create(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
) (*codexclient.CompletionResult, error) {
	if f.createFunc == nil {
		return nil, nil
	}
	return f.createFunc(ctx, payload, options)
}

func (f *fakeGatewayCodexClient) Stream(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
) (*codexclient.StreamResult, error) {
	if f.streamFunc == nil {
		return nil, nil
	}
	return f.streamFunc(ctx, payload, options)
}

type realtimeGatewayCodexClient struct {
	streamEventsFunc func(
		ctx context.Context,
		payload map[string]any,
		options codexclient.CallOptions,
		handler codexclient.StreamEventHandler,
	) (*codexclient.StreamResult, error)
}

func (f *realtimeGatewayCodexClient) Create(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
) (*codexclient.CompletionResult, error) {
	return nil, nil
}

func (f *realtimeGatewayCodexClient) Stream(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
) (*codexclient.StreamResult, error) {
	return nil, nil
}

func (f *realtimeGatewayCodexClient) StreamEvents(
	ctx context.Context,
	payload map[string]any,
	options codexclient.CallOptions,
	handler codexclient.StreamEventHandler,
) (*codexclient.StreamResult, error) {
	return f.streamEventsFunc(ctx, payload, options, handler)
}

func TestParseMessagesRequestFromFixtures(t *testing.T) {
	testCases := []struct {
		name    string
		fixture string
		assert  func(t *testing.T, payload map[string]any)
	}{
		{
			name:    "plain text fixture",
			fixture: "anthropic_plain_text_basic.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				input := asSlice(t, payload["input"], "input")
				if len(input) != 1 {
					t.Fatalf("expected one input message, got %d", len(input))
				}
				if !inputContainsBlockType(t, input, "input_text") {
					t.Fatalf("expected converted input_text block")
				}
			},
		},
		{
			name:    "system prompt fixture",
			fixture: "anthropic_with_system_prompt.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				instructions, _ := payload["instructions"].(string)
				if strings.TrimSpace(instructions) == "" {
					t.Fatalf("expected non-empty instructions mapping")
				}
			},
		},
		{
			name:    "max tokens fixture",
			fixture: "anthropic_max_tokens_limit.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["max_output_tokens"] != float64(4096) && payload["max_output_tokens"] != 4096 {
					t.Fatalf("expected max_output_tokens to carry max_tokens, got %#v", payload["max_output_tokens"])
				}
			},
		},
		{
			name:    "stop sequences fixture",
			fixture: "anthropic_with_stop_sequences.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				stop := asStringSlice(t, payload["stop"], "stop")
				if len(stop) != 2 {
					t.Fatalf("expected 2 stop sequences, got %d", len(stop))
				}
			},
		},
		{
			name:    "tool definitions fixture",
			fixture: "anthropic_with_tool_definitions.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				tools := asSlice(t, payload["tools"], "tools")
				if len(tools) != 1 {
					t.Fatalf("expected one converted tool, got %d", len(tools))
				}
				tool := asMap(t, tools[0], "tools[0]")
				if tool["type"] != "function" {
					t.Fatalf("expected converted tool type=function, got %#v", tool["type"])
				}
				choice := asMap(t, payload["tool_choice"], "tool_choice")
				if choice["type"] != "function" || choice["name"] != "get_build_status" {
					t.Fatalf("expected Responses function tool_choice, got %#v", choice)
				}
			},
		},
		{
			name:    "tool result fixture",
			fixture: "anthropic_with_tool_result.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				input := asSlice(t, payload["input"], "input")
				if !inputContainsBlockType(t, input, "tool_call") {
					t.Fatalf("expected converted tool_call block from tool_use")
				}
				if !inputContainsBlockType(t, input, "tool_result") {
					t.Fatalf("expected converted tool_result block")
				}
			},
		},
		{
			name:    "multimodal fixture",
			fixture: "anthropic_multimodal_placeholder.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				input := asSlice(t, payload["input"], "input")
				if !inputContainsBlockType(t, input, "input_image") {
					t.Fatalf("expected converted input_image block")
				}
			},
		},
		{
			name:    "long context fixture",
			fixture: "anthropic_long_context.json",
			assert: func(t *testing.T, payload map[string]any) {
				t.Helper()
				input := asSlice(t, payload["input"], "input")
				if len(input) == 0 {
					t.Fatalf("expected input entries")
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := loadAnthropicRequestBodyFixture(t, tc.fixture)
			request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: false})
			if apiErr != nil {
				t.Fatalf("parse fixture failed: %+v", *apiErr)
			}
			payload := convertMessagesRequestToCodexPayload(request)
			expectedModel := codexUpstreamModelName(request.Model)
			if payload["model"] != expectedModel {
				t.Fatalf("converted payload model mismatch: %#v vs %q", payload["model"], expectedModel)
			}
			if payload["max_output_tokens"] != request.MaxTokens {
				t.Fatalf("converted payload max_output_tokens mismatch: %#v vs %d", payload["max_output_tokens"], request.MaxTokens)
			}
			tc.assert(t, payload)
		})
	}
}

func TestParseMessagesRequestFailClosed(t *testing.T) {
	invalidFieldBody := loadAnthropicRequestBodyFixture(t, "anthropic_invalid_field.json")
	_, invalidFieldErr := parseMessagesRequest(invalidFieldBody, messagesParseOptions{AllowStream: false})
	if invalidFieldErr == nil {
		t.Fatal("expected unsupported field error")
	}
	if invalidFieldErr.Code != "unsupported_field" {
		t.Fatalf("expected unsupported_field code, got %q", invalidFieldErr.Code)
	}
	if !strings.Contains(invalidFieldErr.Message, "unsupported_field") {
		t.Fatalf("expected actionable unsupported field message, got %q", invalidFieldErr.Message)
	}

	streamBody := loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json")
	_, streamErr := parseMessagesRequest(streamBody, messagesParseOptions{AllowStream: false})
	if streamErr == nil {
		t.Fatal("expected unsupported stream error")
	}
	if streamErr.Code != "unsupported_stream" {
		t.Fatalf("expected unsupported_stream code, got %q", streamErr.Code)
	}

	_, streamAllowedErr := parseMessagesRequest(streamBody, messagesParseOptions{AllowStream: true})
	if streamAllowedErr != nil {
		t.Fatalf("stream should be accepted for count_tokens parsing: %+v", *streamAllowedErr)
	}

	unsupportedToolSchemaBody := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":[{"type":"text","text":"run"}]}],
	  "max_tokens":64,
	  "stream":false,
	  "tools":[{"name":"bad_tool","input_schema":{"type":"array"}}]
	}`)
	_, toolSchemaErr := parseMessagesRequest(unsupportedToolSchemaBody, messagesParseOptions{AllowStream: true})
	if toolSchemaErr == nil {
		t.Fatal("expected unsupported tool schema error")
	}
	if toolSchemaErr.Code != "unsupported_tool_schema" {
		t.Fatalf("expected unsupported_tool_schema code, got %q", toolSchemaErr.Code)
	}
}

func TestParseMessagesRequestAcceptsClaudeCodeCompatibilityHints(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "max_tokens":64,
	  "metadata":{"user_id":"local-client"},
	  "context_management":{"edits":{}},
	  "output_config":{"format":"text"},
	  "thinking":{"type":"disabled"},
	  "reasoning":{"effort":"medium"}
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected Claude Code compatibility hints to parse: %+v", *apiErr)
	}
	payload := convertMessagesRequestToCodexPayload(request)
	for _, field := range []string{"metadata", "context_management", "output_config", "thinking", "reasoning"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("compatibility field %q must not be forwarded to Codex payload: %#v", field, payload)
		}
	}
}

func TestConvertMessagesRequestStripsClaudeCodeLongContextModelSuffix(t *testing.T) {
	body := []byte(`{
	  "model":"gpt-5.5[1m]",
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("parse request failed: %+v", *apiErr)
	}
	payload := convertMessagesRequestToCodexPayload(request)
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("expected upstream model suffix stripped, got %#v", payload["model"])
	}
	if request.Model != "gpt-5.5[1m]" {
		t.Fatalf("client-facing request model should be preserved, got %q", request.Model)
	}
}

func TestConvertMessagesRequestMapsClaudeModelNamesToCodexModels(t *testing.T) {
	t.Setenv("CODEX_MODEL_MAP_SONNET", "gpt-5.3-codex")
	body := []byte(`{
	  "model":"claude-sonnet-4-5[1m]",
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("parse request failed: %+v", *apiErr)
	}
	payload := convertMessagesRequestToCodexPayload(request)
	if payload["model"] != "gpt-5.3-codex" {
		t.Fatalf("expected Claude model mapped to configured Codex model, got %#v", payload["model"])
	}
}

func TestParseMessagesRequestAcceptsSystemTextBlocks(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "system":[
	    {"type":"text","text":"Follow the local harness contract.","cache_control":{"type":"ephemeral"}},
	    {"type":"text","text":"Return concise answers."}
	  ],
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping","cache_control":{"type":"ephemeral"}}]}],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected system text blocks to parse: %+v", *apiErr)
	}
	if request.System != "Follow the local harness contract.\n\nReturn concise answers." {
		t.Fatalf("unexpected system prompt conversion: %q", request.System)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	instructions, _ := payload["instructions"].(string)
	if instructions != request.System {
		t.Fatalf("expected converted instructions, got %#v", payload["instructions"])
	}
	input := asSlice(t, payload["input"], "input")
	firstMessage := input[0].(map[string]any)
	content := asSlice(t, firstMessage["content"], "content")
	firstBlock := content[0].(map[string]any)
	if _, ok := firstBlock["cache_control"]; ok {
		t.Fatalf("cache_control must not be forwarded to Codex payload: %#v", firstBlock)
	}
}

func TestParseMessagesRequestAcceptsCompatibilityCacheControlOnAllContentBlocks(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[
	    {"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="},"cache_control":{"type":"ephemeral"}}]},
	    {"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"inspect","input":{"path":"README.md"},"cache_control":{"type":"ephemeral"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok","cache_control":{"type":"ephemeral"}}]}
	  ],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected cache_control on non-text content blocks to parse: %+v", *apiErr)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	input := asSlice(t, payload["input"], "input")
	for messageIndex, rawMessage := range input {
		message := asMap(t, rawMessage, "input message")
		content := asSlice(t, message["content"], "content")
		firstBlock := asMap(t, content[0], "content block")
		if _, ok := firstBlock["cache_control"]; ok {
			t.Fatalf("cache_control must not be forwarded for input[%d]: %#v", messageIndex, firstBlock)
		}
	}
}

func TestParseMessagesRequestDropsEmptyClaudeCodeTextHistoryBlocks(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"start"}]},
	    {"role":"assistant","content":[
	      {"type":"text","text":" "},
	      {"type":"tool_use","id":"toolu_01","name":"inspect","input":{"path":"README.md"}}
	    ]},
	    {"role":"user","content":[{"type":"text","text":""}]},
	    {"role":"user","content":[{"type":"text","text":"continue"}]}
	  ],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected empty text compatibility blocks to parse: %+v", *apiErr)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	input := asSlice(t, payload["input"], "input")
	if len(input) != 3 {
		t.Fatalf("expected empty-only message to be dropped, got %#v", input)
	}
	for messageIndex, rawMessage := range input {
		message := asMap(t, rawMessage, "input message")
		content := asSlice(t, message["content"], "content")
		if len(content) == 0 {
			t.Fatalf("input[%d] must not contain an empty content array: %#v", messageIndex, message)
		}
		for blockIndex, rawBlock := range content {
			block := asMap(t, rawBlock, "content block")
			if block["type"] == "input_text" && strings.TrimSpace(block["text"].(string)) == "" {
				t.Fatalf("input[%d].content[%d] retained empty text: %#v", messageIndex, blockIndex, block)
			}
		}
	}
}

func TestConvertMessagesRequestMapsAssistantHistoryTextToOutputText(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"start"}]},
	    {"role":"assistant","content":[{"type":"text","text":"I will inspect the project."}]},
	    {"role":"user","content":[{"type":"text","text":"continue"}]}
	  ],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected assistant history to parse: %+v", *apiErr)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	input := asSlice(t, payload["input"], "input")
	userMessage := asMap(t, input[0], "input[0]")
	userContent := asSlice(t, userMessage["content"], "input[0].content")
	userText := asMap(t, userContent[0], "input[0].content[0]")
	if userText["type"] != "input_text" {
		t.Fatalf("expected user text to map to input_text, got %#v", userText)
	}

	assistantMessage := asMap(t, input[1], "input[1]")
	assistantContent := asSlice(t, assistantMessage["content"], "input[1].content")
	assistantText := asMap(t, assistantContent[0], "input[1].content[0]")
	if assistantText["type"] != "output_text" {
		t.Fatalf("expected assistant history text to map to output_text, got %#v", assistantText)
	}
}

func TestParseMessagesRequestFoldsSystemRoleMessages(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "system":"Top-level instruction.",
	  "messages":[
	    {"role":"system","content":"System role instruction."},
	    {"role":"user","content":"ping"}
	  ],
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected system role message to parse: %+v", *apiErr)
	}
	if request.System != "Top-level instruction.\n\nSystem role instruction." {
		t.Fatalf("unexpected folded system prompt: %q", request.System)
	}
	if len(request.Messages) != 1 || request.Messages[0].Role != "user" {
		t.Fatalf("expected only user/assistant messages to remain, got %#v", request.Messages)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	input := asSlice(t, payload["input"], "input")
	if len(input) != 1 {
		t.Fatalf("expected system message to be folded out of input, got %#v", input)
	}
}

func TestParseMessagesRequestRejectsUnsupportedSystemBlocks(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "system":[{"type":"image","text":"not accepted"}],
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "max_tokens":64
	}`)
	_, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr == nil {
		t.Fatal("expected unsupported system block to fail closed")
	}
	if apiErr.Code != "unsupported_content_type" {
		t.Fatalf("expected unsupported_content_type code, got %q", apiErr.Code)
	}
}

func TestParseMessagesRequestAcceptsIgnoredToolMetadata(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "max_tokens":64,
	  "tools":[{
	    "name":"local_tool",
	    "description":"Local test tool",
	    "cache_control":{"type":"ephemeral"},
	    "input_schema":{
	      "$schema":"http://json-schema.org/draft-07/schema#",
	      "type":"object",
	      "properties":{
	        "query":{"type":"string","description":"Search query","default":"status"},
	        "limit":{"type":"integer","minimum":1,"maximum":100,"exclusiveMinimum":0},
	        "questions":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":5},
	        "answers":{"type":"object","propertyNames":{"pattern":"^[a-z]+$","minLength":1,"maxLength":16},"additionalProperties":{"type":"string"}},
	        "url":{"type":"string","format":"uri"}
	      },
	      "required":["query"],
	      "additionalProperties":false
	    }
	  }]
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected ignored tool metadata to parse: %+v", *apiErr)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	tools := asSlice(t, payload["tools"], "tools")
	tool := tools[0].(map[string]any)
	if _, ok := tool["cache_control"]; ok {
		t.Fatalf("cache_control must not be forwarded to Codex payload: %#v", tool)
	}
	parameters := tool["parameters"].(map[string]any)
	if _, ok := parameters["$schema"]; ok {
		t.Fatalf("$schema must not be forwarded to Codex tool parameters: %#v", parameters)
	}
	properties := parameters["properties"].(map[string]any)
	query := properties["query"].(map[string]any)
	if _, ok := query["default"]; ok {
		t.Fatalf("default must not be forwarded to Codex tool parameters: %#v", query)
	}
	limit := properties["limit"].(map[string]any)
	if limit["minimum"] != float64(1) || limit["maximum"] != float64(100) ||
		limit["exclusiveMinimum"] != float64(0) {
		t.Fatalf("expected numeric schema bounds to be preserved, got %#v", limit)
	}
	questions := properties["questions"].(map[string]any)
	if questions["minItems"] != float64(1) || questions["maxItems"] != float64(5) {
		t.Fatalf("expected array item bounds to be preserved, got %#v", questions)
	}
	answers := properties["answers"].(map[string]any)
	propertyNames := answers["propertyNames"].(map[string]any)
	if propertyNames["pattern"] != "^[a-z]+$" ||
		propertyNames["minLength"] != float64(1) ||
		propertyNames["maxLength"] != float64(16) {
		t.Fatalf("expected propertyNames schema to be preserved, got %#v", propertyNames)
	}
	url := properties["url"].(map[string]any)
	if url["format"] != "uri" {
		t.Fatalf("expected format to be preserved, got %#v", url)
	}
}

func TestParseMessagesRequestAcceptsOpenAIFunctionTools(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":"ping"}],
	  "max_tokens":64,
	  "tools":[{
	    "type":"function",
	    "function":{
	      "name":"local_tool",
	      "description":"Local test tool",
	      "strict":true,
	      "parameters":{
	        "$schema":"http://json-schema.org/draft-07/schema#",
	        "type":"object",
	        "$defs":{
	          "mode":{"type":"string","const":"read"}
	        },
	        "properties":{
	          "query":{"type":["string","null"],"default":"status"},
	          "status":{
	            "anyOf":[
	              {"type":"string","enum":["pending","completed"]},
	              {"const":"completed"}
	            ]
	          },
	          "mode":{"$ref":"#/$defs/mode"},
	          "filters":{
	            "type":"object",
	            "patternProperties":{"^x-":{"type":"string"}},
	            "additionalProperties":{"type":"string"},
	            "dependentRequired":{"start":["end"]},
	            "dependentSchemas":{"advanced":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}
	          },
	          "items":{
	            "type":"array",
	            "prefixItems":[{"type":"string"},{"type":"integer"}],
	            "items":{"oneOf":[{"type":"string"},{"type":"number"}]},
	            "contains":{"type":"string","const":"needle"},
	            "uniqueItems":true
	          }
	        },
	        "allOf":[{"type":"object"}],
	        "required":["query"],
	        "additionalProperties":false
	      }
	    }
	  }]
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected OpenAI-style function tool to parse: %+v", *apiErr)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	tools := asSlice(t, payload["tools"], "tools")
	tool := tools[0].(map[string]any)
	if tool["name"] != "local_tool" {
		t.Fatalf("expected normalized tool name, got %#v", tool)
	}
	if _, ok := tool["function"]; ok {
		t.Fatalf("OpenAI-style wrapper must not be forwarded to Codex payload: %#v", tool)
	}
	parameters := tool["parameters"].(map[string]any)
	if _, ok := parameters["$schema"]; ok {
		t.Fatalf("$schema must not be forwarded to Codex tool parameters: %#v", parameters)
	}
	properties := parameters["properties"].(map[string]any)
	query := properties["query"].(map[string]any)
	if _, ok := query["default"]; ok {
		t.Fatalf("default must not be forwarded to Codex tool parameters: %#v", query)
	}
}

func TestParseMessagesRequestAcceptsOpenAIFunctionToolAnyOfSchema(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":"ping"}],
	  "max_tokens":64,
	  "tools":[{
	    "type":"function",
	    "function":{
	      "name":"local_tool",
	      "parameters":{
	        "type":"object",
	        "properties":{
	          "status":{
	            "anyOf":[
	              {"type":"string","enum":["pending","completed"]},
	              {"const":"completed"}
	            ],
	            "description":"Optional status filter"
	          }
	        },
	        "additionalProperties":false
	      }
	    }
	  }]
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected anyOf tool schema to parse: %+v", *apiErr)
	}

	payload := convertMessagesRequestToCodexPayload(request)
	tools := asSlice(t, payload["tools"], "tools")
	tool := tools[0].(map[string]any)
	parameters := tool["parameters"].(map[string]any)
	properties := parameters["properties"].(map[string]any)
	status := properties["status"].(map[string]any)
	anyOf := asSlice(t, status["anyOf"], "status.anyOf")
	if len(anyOf) != 2 {
		t.Fatalf("expected two anyOf variants, got %#v", anyOf)
	}
}

func TestParseMessagesRequestRejectsMalformedCompatibilityHints(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "max_tokens":64,
	  "reasoning":"medium"
	}`)
	_, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr == nil {
		t.Fatal("expected malformed compatibility hint to fail closed")
	}
	if apiErr.Code != "unsupported_field" {
		t.Fatalf("expected unsupported_field code, got %q", apiErr.Code)
	}
}

func TestParseMessagesToolChoiceAnyMapsToResponsesRequired(t *testing.T) {
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],
	  "tools":[{"name":"inspect","description":"Inspect a file.","input_schema":{"type":"object"}}],
	  "tool_choice":{"type":"any"},
	  "max_tokens":64
	}`)
	request, apiErr := parseMessagesRequest(body, messagesParseOptions{AllowStream: true})
	if apiErr != nil {
		t.Fatalf("expected tool_choice any to parse: %+v", *apiErr)
	}
	payload := convertMessagesRequestToCodexPayload(request)
	if payload["tool_choice"] != "required" {
		t.Fatalf("expected Responses required tool_choice, got %#v", payload["tool_choice"])
	}
}

func TestMessagesHandlerReturnsConvertedResponses(t *testing.T) {
	testCases := []struct {
		name                string
		codexFixture        string
		expectedStopReason  string
		expectedContentType string
	}{
		{
			name:                "normal completion",
			codexFixture:        "codex_normal_completion.json",
			expectedStopReason:  "end_turn",
			expectedContentType: "text",
		},
		{
			name:                "tool call completion",
			codexFixture:        "codex_tool_call.json",
			expectedStopReason:  "tool_use",
			expectedContentType: "tool_use",
		},
		{
			name:                "tool result completion",
			codexFixture:        "codex_tool_result.json",
			expectedStopReason:  "end_turn",
			expectedContentType: "tool_result",
		},
		{
			name:                "refusal completion",
			codexFixture:        "codex_refusal.json",
			expectedStopReason:  "refusal",
			expectedContentType: "text",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			codexResponse := loadCodexResponseFixture(t, tc.codexFixture)
			client := &fakeGatewayCodexClient{
				createFunc: func(
					_ context.Context,
					_ map[string]any,
					_ codexclient.CallOptions,
				) (*codexclient.CompletionResult, error) {
					return &codexclient.CompletionResult{
						RequestID:  "req-phase6-01",
						HTTPStatus: http.StatusOK,
						Response:   codexResponse,
					}, nil
				},
			}
			server := newMessagesTestServer(t, client)
			status, payload := performJSONRequest(
				t,
				server,
				http.MethodPost,
				"/v1/messages",
				loadAnthropicRequestBodyFixture(t, "anthropic_plain_text_basic.json"),
			)
			if status != http.StatusOK {
				t.Fatalf("expected 200, got %d payload=%#v", status, payload)
			}
			if payload["type"] != "message" {
				t.Fatalf("expected response type message, got %#v", payload["type"])
			}
			if payload["stop_reason"] != tc.expectedStopReason {
				t.Fatalf("expected stop_reason %q, got %#v", tc.expectedStopReason, payload["stop_reason"])
			}

			content := asSlice(t, payload["content"], "content")
			if len(content) == 0 {
				t.Fatalf("expected non-empty content blocks")
			}
			first := asMap(t, content[0], "content[0]")
			if first["type"] != tc.expectedContentType {
				t.Fatalf("expected first content type %q, got %#v", tc.expectedContentType, first["type"])
			}
		})
	}
}

func TestMessagesHandlerConvertsResponsesFunctionCallOutput(t *testing.T) {
	client := &fakeGatewayCodexClient{
		createFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.CompletionResult, error) {
			return &codexclient.CompletionResult{
				RequestID:  "req-function-call",
				HTTPStatus: http.StatusOK,
				Response: map[string]any{
					"id":     "resp_function_call",
					"status": "completed",
					"output": []any{
						map[string]any{
							"type":      "function_call",
							"call_id":   "call_1",
							"name":      "inspect",
							"arguments": `{"path":"README.md"}`,
						},
					},
				},
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	status, payload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_plain_text_basic.json"),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d payload=%#v", status, payload)
	}
	if payload["stop_reason"] != "tool_use" {
		t.Fatalf("expected tool_use stop reason, got %#v", payload["stop_reason"])
	}
	content := asSlice(t, payload["content"], "content")
	toolUse := asMap(t, content[0], "content[0]")
	if toolUse["type"] != "tool_use" || toolUse["id"] != "call_1" || toolUse["name"] != "inspect" {
		t.Fatalf("expected Anthropic tool_use, got %#v", toolUse)
	}
	input := asMap(t, toolUse["input"], "tool input")
	if input["path"] != "README.md" {
		t.Fatalf("expected parsed function_call arguments, got %#v", input)
	}
}

func TestMessagesHandlerFailClosedOnUnsupportedField(t *testing.T) {
	called := false
	client := &fakeGatewayCodexClient{
		createFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.CompletionResult, error) {
			called = true
			return nil, nil
		},
	}
	server := newMessagesTestServer(t, client)
	status, payload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_invalid_field.json"),
	)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d payload=%#v", status, payload)
	}
	if called {
		t.Fatal("codex client should not be called on invalid request")
	}
	if payload["type"] != "error" {
		t.Fatalf("expected error response type, got %#v", payload["type"])
	}
}

func TestMessagesHandlerMapsCodexErrors(t *testing.T) {
	client := &fakeGatewayCodexClient{
		createFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.CompletionResult, error) {
			return nil, &codexclient.ClientError{
				Kind:       codexclient.KindRateLimited,
				StatusCode: http.StatusTooManyRequests,
				Retryable:  true,
				Message:    "Too many requests.",
			}
		},
	}
	server := newMessagesTestServer(t, client)
	status, payload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_plain_text_basic.json"),
	)
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d payload=%#v", status, payload)
	}
	errorObject := asMap(t, payload["error"], "error")
	if errorObject["code"] != "rate_limited" {
		t.Fatalf("expected rate_limited error code, got %#v", errorObject["code"])
	}
}

func TestCountTokensEndpointConservativeEstimate(t *testing.T) {
	server := newMessagesTestServer(t, nil)
	plainStatus, plainPayload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages/count_tokens",
		loadAnthropicRequestBodyFixture(t, "anthropic_plain_text_basic.json"),
	)
	if plainStatus != http.StatusOK {
		t.Fatalf("expected 200 for plain count_tokens, got %d payload=%#v", plainStatus, plainPayload)
	}
	plainTokens := asIntField(t, plainPayload, "input_tokens")
	if plainTokens <= 0 {
		t.Fatalf("expected positive input_tokens, got %d", plainTokens)
	}

	longStatus, longPayload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages/count_tokens",
		loadAnthropicRequestBodyFixture(t, "anthropic_long_context.json"),
	)
	if longStatus != http.StatusOK {
		t.Fatalf("expected 200 for long count_tokens, got %d payload=%#v", longStatus, longPayload)
	}
	longTokens := asIntField(t, longPayload, "input_tokens")
	if longTokens <= plainTokens {
		t.Fatalf("long context should estimate more tokens (%d <= %d)", longTokens, plainTokens)
	}
}

func TestCountTokensEndpointCountsCJKTextConservatively(t *testing.T) {
	server := newMessagesTestServer(t, nil)
	chineseText := strings.Repeat("你好", 600)
	body := []byte(fmt.Sprintf(`{
	  "model":"gpt-5.5",
	  "messages":[{"role":"user","content":[{"type":"text","text":%q}]}],
	  "max_tokens":64
	}`, chineseText))

	status, payload := performJSONRequest(t, server, http.MethodPost, "/v1/messages/count_tokens", body)
	if status != http.StatusOK {
		t.Fatalf("expected 200 for CJK count_tokens, got %d payload=%#v", status, payload)
	}
	tokens := asIntField(t, payload, "input_tokens")
	if tokens < utf8.RuneCountInString(chineseText) {
		t.Fatalf("CJK token estimate should not undercount runes, got tokens=%d runes=%d", tokens, utf8.RuneCountInString(chineseText))
	}
}

func TestMessagesHandlerStreamingSSEOrderFromFixture(t *testing.T) {
	streamResult := loadCodexStreamFixture(t, "codex_streamed_deltas.json")
	client := &fakeGatewayCodexClient{
		streamFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.StreamResult, error) {
			return streamResult, nil
		},
	}
	server := newMessagesTestServer(t, client)
	status, contentType, events := performSSERequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json"),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream content-type, got %q", contentType)
	}

	names := sseEventNames(events)
	expected := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if strings.Join(names, "|") != strings.Join(expected, "|") {
		t.Fatalf("unexpected stream event order: got=%v expected=%v", names, expected)
	}

	deltas := collectTextDeltas(events)
	if strings.Join(deltas, "") != "Hello world" {
		t.Fatalf("unexpected text deltas: %v", deltas)
	}
}

func TestMessagesRealtimeStreamCompletesBeforeStreamResultDoesNotPanic(t *testing.T) {
	client := &realtimeGatewayCodexClient{
		streamEventsFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
			handler codexclient.StreamEventHandler,
		) (*codexclient.StreamResult, error) {
			for _, event := range []map[string]any{
				{"event": "response.started", "response_id": "resp_realtime_completed"},
				{"event": "response.output_text.delta", "delta": "ok"},
				{"event": "response.completed", "response_id": "resp_realtime_completed", "status": "completed"},
			} {
				if err := handler(event); err != nil {
					return nil, err
				}
			}
			return &codexclient.StreamResult{
				RequestID:   "realtime-completed",
				FinalStatus: "completed",
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	status, _, events := performSSERequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json"),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	names := sseEventNames(events)
	if !sequenceContainsOrder(names, []string{"content_block_delta", "content_block_stop", "message_delta", "message_stop"}) {
		t.Fatalf("unexpected realtime completion ordering: %v", names)
	}
	messageDelta := findSSEEvent(events, "message_delta")
	if messageDelta == nil {
		t.Fatal("missing message_delta")
	}
	delta := asMap(t, (*messageDelta)["delta"], "message_delta.delta")
	if delta["stop_reason"] != "end_turn" {
		t.Fatalf("expected stop_reason=end_turn, got %#v", delta["stop_reason"])
	}
}

func TestMessagesHandlerStreamingTextBlockClosedBeforeToolBlock(t *testing.T) {
	streamResult := loadCodexStreamFixture(t, "codex_stream_text_then_tool_call.json")
	client := &fakeGatewayCodexClient{
		streamFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.StreamResult, error) {
			return streamResult, nil
		},
	}
	server := newMessagesTestServer(t, client)
	status, _, events := performSSERequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json"),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	names := sseEventNames(events)
	expected := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if strings.Join(names, "|") != strings.Join(expected, "|") {
		t.Fatalf("unexpected mixed text/tool event order: got=%v expected=%v", names, expected)
	}
}

func TestMessagesHandlerStreamingToolEventMappingAndRoundTrip(t *testing.T) {
	testCases := []struct {
		name            string
		fixture         string
		expectedType    string
		expectedToolID  string
		expectedDataKey string
	}{
		{
			name:            "tool use stream",
			fixture:         "codex_stream_tool_use.json",
			expectedType:    "tool_use",
			expectedToolID:  "call_stream_001",
			expectedDataKey: "id",
		},
		{
			name:            "tool result stream",
			fixture:         "codex_stream_tool_result.json",
			expectedType:    "tool_result",
			expectedToolID:  "call_stream_001",
			expectedDataKey: "tool_use_id",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			streamResult := loadCodexStreamFixture(t, tc.fixture)
			client := &fakeGatewayCodexClient{
				streamFunc: func(
					_ context.Context,
					_ map[string]any,
					_ codexclient.CallOptions,
				) (*codexclient.StreamResult, error) {
					return streamResult, nil
				},
			}
			server := newMessagesTestServer(t, client)
			status, _, events := performSSERequest(
				t,
				server,
				http.MethodPost,
				"/v1/messages",
				loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json"),
			)
			if status != http.StatusOK {
				t.Fatalf("expected 200, got %d", status)
			}

			names := sseEventNames(events)
			if !sequenceContainsOrder(
				names,
				[]string{"message_start", "content_block_start", "content_block_stop", "message_stop"},
			) {
				t.Fatalf("unexpected tool stream ordering: %v", names)
			}

			startEvent := findSSEEventByBlockType(events, "content_block_start", tc.expectedType)
			if startEvent == nil {
				t.Fatalf("missing content_block_start for type=%q", tc.expectedType)
			}
			contentBlock := asMap(t, (*startEvent)["content_block"], "content_block")
			if contentBlock["type"] != tc.expectedType {
				t.Fatalf("expected content block type %q, got %#v", tc.expectedType, contentBlock["type"])
			}
			if contentBlock[tc.expectedDataKey] != tc.expectedToolID {
				t.Fatalf(
					"expected %s=%q, got %#v",
					tc.expectedDataKey,
					tc.expectedToolID,
					contentBlock[tc.expectedDataKey],
				)
			}
			messageDelta := findSSEEvent(events, "message_delta")
			if messageDelta == nil {
				t.Fatal("missing message_delta")
			}
			delta := asMap(t, (*messageDelta)["delta"], "message_delta.delta")
			expectedStopReason := "end_turn"
			if tc.expectedType == "tool_use" {
				expectedStopReason = "tool_use"
			}
			if delta["stop_reason"] != expectedStopReason {
				t.Fatalf("expected stop_reason=%q, got %#v", expectedStopReason, delta["stop_reason"])
			}
		})
	}
}

func TestMessagesHandlerStreamingPartialFailureWithExplicitError(t *testing.T) {
	streamResult := loadCodexStreamFixture(t, "codex_partial_stream_failure.json")
	client := &fakeGatewayCodexClient{
		streamFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.StreamResult, error) {
			return streamResult, &codexclient.ClientError{
				Kind:       codexclient.KindStreamFailed,
				StatusCode: http.StatusOK,
				RequestID:  "req-stream-partial-01",
				Retryable:  false,
				Message:    "stream ended with final_status=failed",
			}
		},
	}
	server := newMessagesTestServer(t, client)
	status, _, events := performSSERequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json"),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	names := sseEventNames(events)
	if !sequenceContainsOrder(
		names,
		[]string{"content_block_delta", "error", "message_delta", "message_stop"},
	) {
		t.Fatalf("unexpected partial failure ordering: %v", names)
	}

	errorEvent := findSSEEvent(events, "error")
	if errorEvent == nil {
		t.Fatal("missing error event")
	}
	errorObj := asMap(t, (*errorEvent)["error"], "error event payload")
	if errorObj["code"] != "stream_interrupted" {
		t.Fatalf("expected stream_interrupted error code, got %#v", errorObj["code"])
	}

	messageDelta := findSSEEvent(events, "message_delta")
	if messageDelta == nil {
		t.Fatal("missing message_delta")
	}
	delta := asMap(t, (*messageDelta)["delta"], "message_delta.delta")
	if delta["stop_reason"] != "error" {
		t.Fatalf("expected stop_reason=error, got %#v", delta["stop_reason"])
	}
}

func TestMessagesHandlerStreamingPartialFailureSynthesizedErrorBeforeStop(t *testing.T) {
	streamResult := loadCodexStreamFixture(t, "codex_stream_failed_without_error.json")
	client := &fakeGatewayCodexClient{
		streamFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.StreamResult, error) {
			return streamResult, &codexclient.ClientError{
				Kind:       codexclient.KindStreamFailed,
				StatusCode: http.StatusOK,
				RequestID:  "req-stream-failed-02",
				Retryable:  false,
				Message:    "stream ended with final_status=failed",
			}
		},
	}
	server := newMessagesTestServer(t, client)
	status, _, events := performSSERequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_streaming_request.json"),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	names := sseEventNames(events)
	errorIndex := indexOfEventName(names, "error")
	stopIndex := indexOfEventName(names, "message_stop")
	if errorIndex == -1 || stopIndex == -1 {
		t.Fatalf("expected error and message_stop events, got %v", names)
	}
	if errorIndex > stopIndex {
		t.Fatalf("synthesized error must appear before message_stop, got %v", names)
	}
}

func TestMessagesHandlerFailsWhenCodexOutputMissing(t *testing.T) {
	client := &fakeGatewayCodexClient{
		createFunc: func(
			_ context.Context,
			_ map[string]any,
			_ codexclient.CallOptions,
		) (*codexclient.CompletionResult, error) {
			return &codexclient.CompletionResult{
				RequestID:  "req-missing-output-01",
				HTTPStatus: http.StatusOK,
				Response: map[string]any{
					"id":     "resp_missing_output_01",
					"status": "completed",
				},
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	status, payload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/messages",
		loadAnthropicRequestBodyFixture(t, "anthropic_plain_text_basic.json"),
	)
	if status != http.StatusBadGateway {
		t.Fatalf("expected 502 for missing output, got %d payload=%#v", status, payload)
	}
	errorObj := asMap(t, payload["error"], "error")
	if errorObj["code"] != "bad_upstream_response" {
		t.Fatalf("expected bad_upstream_response code, got %#v", errorObj["code"])
	}
}

func TestChatCompletionsHandlerReturnsOpenAIResponse(t *testing.T) {
	var capturedPayload map[string]any
	client := &fakeGatewayCodexClient{
		createFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
		) (*codexclient.CompletionResult, error) {
			capturedPayload = payload
			return &codexclient.CompletionResult{
				RequestID:  "req_chat_test",
				HTTPStatus: http.StatusOK,
				Response: map[string]any{
					"id":     "resp_chat_test",
					"status": "completed",
					"output": []any{
						map[string]any{
							"type": "message",
							"role": "assistant",
							"content": []any{
								map[string]any{"type": "output_text", "text": "client-ok"},
							},
						},
					},
				},
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "messages":[
	    {"role":"system","content":"System instruction."},
	    {"role":"user","content":"Reply with exactly: client-ok"}
	  ],
	  "max_completion_tokens":64,
	  "stop":"END",
	  "temperature":0,
	  "tools":[{
	    "type":"function",
	    "function":{
	      "name":"local_tool",
	      "parameters":{
	        "$schema":"http://json-schema.org/draft-07/schema#",
	        "type":"object",
	        "$defs":{
	          "mode":{"type":"string","const":"read"}
	        },
	        "properties":{
	          "query":{"type":["string","null"],"default":"status"},
	          "status":{
	            "anyOf":[
	              {"type":"string","enum":["pending","completed"]},
	              {"const":"completed"}
	            ]
	          },
	          "mode":{"$ref":"#/$defs/mode"},
	          "filters":{
	            "type":"object",
	            "patternProperties":{"^x-":{"type":"string"}},
	            "additionalProperties":{"type":"string"},
	            "dependentRequired":{"start":["end"]},
	            "dependentSchemas":{"advanced":{"type":"object","properties":{"enabled":{"type":"boolean"}}}}
	          },
	          "items":{
	            "type":"array",
	            "prefixItems":[{"type":"string"},{"type":"integer"}],
	            "items":{"oneOf":[{"type":"string"},{"type":"number"}]},
	            "contains":{"type":"string","const":"needle"},
	            "uniqueItems":true
	          }
	        },
	        "allOf":[{"type":"object"}],
	        "required":["query"],
	        "additionalProperties":false
	      }
	    }
	  }]
	}`)
	status, payload := performJSONRequest(t, server, http.MethodPost, "/v1/chat/completions", body)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d payload=%#v", status, payload)
	}

	choices := asSlice(t, payload["choices"], "choices")
	firstChoice := choices[0].(map[string]any)
	message := firstChoice["message"].(map[string]any)
	if message["content"] != "client-ok" {
		t.Fatalf("expected OpenAI chat content, got %#v", message)
	}
	if firstChoice["finish_reason"] != "stop" {
		t.Fatalf("expected stop finish reason, got %#v", firstChoice["finish_reason"])
	}

	if capturedPayload["instructions"] != "System instruction." {
		t.Fatalf("expected system role to become instructions, got %#v", capturedPayload)
	}
	stop, ok := capturedPayload["stop"].([]string)
	if !ok || len(stop) != 1 || stop[0] != "END" {
		t.Fatalf("expected OpenAI stop to become stop sequence, got %#v", capturedPayload["stop"])
	}
	tools := asSlice(t, capturedPayload["tools"], "tools")
	tool := tools[0].(map[string]any)
	parameters := tool["parameters"].(map[string]any)
	if _, ok := parameters["$schema"]; ok {
		t.Fatalf("$schema must not be forwarded to Codex parameters: %#v", parameters)
	}
	properties := parameters["properties"].(map[string]any)
	query := properties["query"].(map[string]any)
	if _, ok := query["default"]; ok {
		t.Fatalf("default must not be forwarded to Codex parameters: %#v", query)
	}
	statusSchema := properties["status"].(map[string]any)
	if len(asSlice(t, statusSchema["anyOf"], "status.anyOf")) != 2 {
		t.Fatalf("expected anyOf status schema to be forwarded, got %#v", statusSchema)
	}
	items := properties["items"].(map[string]any)
	if len(asSlice(t, items["prefixItems"], "items.prefixItems")) != 2 {
		t.Fatalf("expected prefixItems schema to be forwarded, got %#v", items)
	}
}

func TestChatCompletionsHandlerCanOverrideUpstreamModel(t *testing.T) {
	var capturedPayload map[string]any
	client := &fakeGatewayCodexClient{
		createFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
		) (*codexclient.CompletionResult, error) {
			capturedPayload = payload
			return &codexclient.CompletionResult{
				RequestID:  "req_chat_model_override",
				HTTPStatus: http.StatusOK,
				Response: map[string]any{
					"id":     "resp_chat_model_override",
					"status": "completed",
					"output": []any{
						map[string]any{
							"type": "message",
							"role": "assistant",
							"content": []any{
								map[string]any{"type": "output_text", "text": "client-ok"},
							},
						},
					},
				},
			}, nil
		},
	}
	cfg := DefaultConfig()
	cfg.UpstreamModel = "gpt-5.3-codex"
	server, err := NewServerWithClient(cfg, log.New(io.Discard, "", 0), client)
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}

	status, payload := performJSONRequest(
		t,
		server,
		http.MethodPost,
		"/v1/chat/completions",
		[]byte(`{
		  "model":"claude-sonnet-4-5",
		  "messages":[{"role":"user","content":"Reply with exactly: client-ok"}],
		  "max_completion_tokens":64
		}`),
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d payload=%#v", status, payload)
	}
	if capturedPayload["model"] != "gpt-5.3-codex" {
		t.Fatalf("expected upstream model override, got %#v", capturedPayload["model"])
	}
	if payload["model"] != "claude-sonnet-4-5" {
		t.Fatalf("client response should keep requested model, got %#v", payload["model"])
	}
}

func TestChatCompletionsHandlerStreamsOpenAIChunks(t *testing.T) {
	var capturedPayload map[string]any
	client := &fakeGatewayCodexClient{
		streamFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
		) (*codexclient.StreamResult, error) {
			capturedPayload = payload
			return &codexclient.StreamResult{
				RequestID:   "req_chat_stream",
				HTTPStatus:  http.StatusOK,
				FinalStatus: "completed",
				Events: []map[string]any{
					{"event": "response.started", "response_id": "resp_chat_stream"},
					{"event": "response.output_text.delta", "delta": "client-ok"},
					{"event": "response.completed", "response_id": "resp_chat_stream", "status": "completed"},
				},
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{
		  "model":"claude-sonnet-4-5",
		  "messages":[{"role":"user","content":"ping"}],
		  "stream":true
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	rawBody, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read stream body failed: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", result.StatusCode, string(rawBody))
	}
	if !strings.Contains(result.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", result.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(rawBody), `"content":"client-ok"`) {
		t.Fatalf("expected streamed OpenAI content chunk, got %s", string(rawBody))
	}
	if !strings.Contains(string(rawBody), "data: [DONE]") {
		t.Fatalf("expected OpenAI stream terminator, got %s", string(rawBody))
	}
	if capturedPayload["stream"] != true {
		t.Fatalf("expected Codex stream payload, got %#v", capturedPayload)
	}
}

func TestChatCompletionsHandlerStreamsExplicitErrorOnUpstreamFailure(t *testing.T) {
	client := &fakeGatewayCodexClient{
		streamFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
		) (*codexclient.StreamResult, error) {
			return &codexclient.StreamResult{
					RequestID:   "req_chat_stream_error",
					HTTPStatus:  http.StatusOK,
					FinalStatus: "failed",
					Events: []map[string]any{
						{"event": "response.started", "response_id": "resp_chat_stream_error"},
						{"event": "response.output_text.delta", "delta": "partial"},
					},
				}, &codexclient.ClientError{
					Kind:      codexclient.KindTransportFailed,
					Retryable: true,
					Message:   "unable to read codex web stream: stream read failed",
				}
		},
	}
	server := newMessagesTestServer(t, client)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{
		  "model":"claude-sonnet-4-5",
		  "messages":[{"role":"user","content":"ping"}],
		  "stream":true
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	rawBody, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read stream body failed: %v", err)
	}
	body := string(rawBody)
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after stream started, got %d body=%s", result.StatusCode, body)
	}
	if !strings.Contains(body, `"content":"partial"`) {
		t.Fatalf("expected partial delta before stream error, got %s", body)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"code":"upstream_error"`) {
		t.Fatalf("expected explicit stream error event, got %s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("stream failure must not be masked as finish_reason=stop: %s", body)
	}
}

func TestChatCompletionsRealtimeStreamFlushesBeforeBackendCompletes(t *testing.T) {
	releaseBackend := make(chan struct{})
	client := &realtimeGatewayCodexClient{
		streamEventsFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
			handler codexclient.StreamEventHandler,
		) (*codexclient.StreamResult, error) {
			events := []map[string]any{
				{"event": "response.started", "response_id": "resp_realtime"},
				{"event": "response.output_text.delta", "delta": "early"},
			}
			for _, event := range events {
				if err := handler(event); err != nil {
					return nil, err
				}
			}
			select {
			case <-releaseBackend:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			completed := map[string]any{
				"event":       "response.completed",
				"response_id": "resp_realtime",
				"status":      "completed",
			}
			if err := handler(completed); err != nil {
				return nil, err
			}
			return &codexclient.StreamResult{
				RequestID:   "req_realtime",
				HTTPStatus:  http.StatusOK,
				FinalStatus: "completed",
				Events:      append(events, completed),
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	httpServer := httptest.NewServer(server.httpServer.Handler)
	defer httpServer.Close()

	req, err := http.NewRequest(
		http.MethodPost,
		httpServer.URL+"/v1/chat/completions",
		strings.NewReader(`{
		  "model":"gpt-5.5",
		  "messages":[{"role":"user","content":"ping"}],
		  "stream":true
		}`),
	)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	earlyChunk := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if strings.Contains(line, `"content":"early"`) {
				earlyChunk <- line
				return
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	select {
	case <-earlyChunk:
	case err := <-readErr:
		t.Fatalf("stream ended before early chunk: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for early streamed chunk")
	}

	close(releaseBackend)
}

func TestChatCompletionsRealtimeStreamSendsHeartbeatBeforeFirstUpstreamEvent(t *testing.T) {
	t.Setenv("CODEX_GATEWAY_SSE_KEEPALIVE_SECONDS", "1")
	releaseBackend := make(chan struct{})
	client := &realtimeGatewayCodexClient{
		streamEventsFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
			handler codexclient.StreamEventHandler,
		) (*codexclient.StreamResult, error) {
			select {
			case <-releaseBackend:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			started := map[string]any{
				"event":       "response.started",
				"response_id": "resp_keepalive",
			}
			if err := handler(started); err != nil {
				return nil, err
			}
			completed := map[string]any{
				"event":       "response.completed",
				"response_id": "resp_keepalive",
				"status":      "completed",
			}
			if err := handler(completed); err != nil {
				return nil, err
			}
			return &codexclient.StreamResult{
				RequestID:   "req_keepalive",
				HTTPStatus:  http.StatusOK,
				FinalStatus: "completed",
				Events:      []map[string]any{started, completed},
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	httpServer := httptest.NewServer(server.httpServer.Handler)
	defer httpServer.Close()
	defer close(releaseBackend)

	req, err := http.NewRequest(
		http.MethodPost,
		httpServer.URL+"/v1/chat/completions",
		strings.NewReader(`{
		  "model":"gpt-5.5",
		  "messages":[{"role":"user","content":"ping"}],
		  "stream":true
		}`),
	)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	keepalive := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if strings.Contains(line, `"content":""`) {
				keepalive <- line
				return
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	select {
	case <-keepalive:
	case err := <-readErr:
		t.Fatalf("stream ended before keepalive: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for keepalive")
	}
}

func TestMessagesRealtimeStreamSendsProtocolPingBeforeFirstUpstreamEvent(t *testing.T) {
	t.Setenv("CODEX_GATEWAY_SSE_KEEPALIVE_SECONDS", "1")
	releaseBackend := make(chan struct{})
	client := &realtimeGatewayCodexClient{
		streamEventsFunc: func(
			ctx context.Context,
			payload map[string]any,
			options codexclient.CallOptions,
			handler codexclient.StreamEventHandler,
		) (*codexclient.StreamResult, error) {
			select {
			case <-releaseBackend:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &codexclient.StreamResult{
				RequestID:   "req_ping",
				HTTPStatus:  http.StatusOK,
				FinalStatus: "completed",
				Events:      []map[string]any{},
			}, nil
		},
	}
	server := newMessagesTestServer(t, client)
	httpServer := httptest.NewServer(server.httpServer.Handler)
	defer httpServer.Close()
	defer close(releaseBackend)

	req, err := http.NewRequest(
		http.MethodPost,
		httpServer.URL+"/v1/messages",
		strings.NewReader(`{
		  "model":"gpt-5.5",
		  "max_tokens":32,
		  "stream":true,
		  "messages":[{"role":"user","content":"ping"}]
		}`),
	)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ping := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if strings.Contains(line, "event: ping") {
				ping <- line
				return
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	select {
	case <-ping:
	case err := <-readErr:
		t.Fatalf("stream ended before ping: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for protocol ping")
	}
}

func newMessagesTestServer(t *testing.T, client codexclient.Client) *Server {
	t.Helper()
	cfg := DefaultConfig()
	server, err := NewServerWithClient(cfg, log.New(io.Discard, "", 0), client)
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	return server
}

func performJSONRequest(
	t *testing.T,
	server *Server,
	method string,
	path string,
	body []byte,
) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	responseBody, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read response body failed: %v", err)
	}

	payload := map[string]any{}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		t.Fatalf("response must be valid json, got %q err=%v", string(responseBody), err)
	}
	return result.StatusCode, payload
}

func performSSERequest(
	t *testing.T,
	server *Server,
	method string,
	path string,
	body []byte,
) (int, string, []map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, req)

	result := recorder.Result()
	defer result.Body.Close()
	rawBody, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read sse response body failed: %v", err)
	}
	contentType := result.Header.Get("Content-Type")
	events := parseSSEPayload(t, string(rawBody))
	return result.StatusCode, contentType, events
}

func loadAnthropicRequestBodyFixture(t *testing.T, fixtureFile string) []byte {
	t.Helper()
	path := fixturePath(t, "anthropic-messages", fixtureFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q failed: %v", path, err)
	}
	var envelope struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode fixture %q failed: %v", path, err)
	}
	if len(envelope.Request) == 0 {
		t.Fatalf("fixture %q missing request field", path)
	}
	return envelope.Request
}

func loadCodexResponseFixture(t *testing.T, fixtureFile string) map[string]any {
	t.Helper()
	path := fixturePath(t, "codex-responses", fixtureFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q failed: %v", path, err)
	}
	var envelope struct {
		Response map[string]any `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode fixture %q failed: %v", path, err)
	}
	if envelope.Response == nil {
		t.Fatalf("fixture %q missing response field", path)
	}
	return envelope.Response
}

func loadCodexStreamFixture(t *testing.T, fixtureFile string) *codexclient.StreamResult {
	t.Helper()
	path := fixturePath(t, "codex-responses", fixtureFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q failed: %v", path, err)
	}
	var envelope struct {
		Response struct {
			Stream      []map[string]any `json:"stream"`
			FinalStatus string           `json:"final_status"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode fixture %q failed: %v", path, err)
	}
	events := make([]map[string]any, 0, len(envelope.Response.Stream))
	for _, event := range envelope.Response.Stream {
		events = append(events, event)
	}
	return &codexclient.StreamResult{
		RequestID:   "req-stream-fixture",
		HTTPStatus:  http.StatusOK,
		Events:      events,
		FinalStatus: envelope.Response.FinalStatus,
	}
}

func fixturePath(t *testing.T, folder string, file string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to resolve runtime caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	return filepath.Join(root, "fixtures", folder, file)
}

func inputContainsBlockType(t *testing.T, input []any, contentType string) bool {
	t.Helper()
	for _, rawMessage := range input {
		message := asMap(t, rawMessage, "input message")
		content := asSlice(t, message["content"], "input message content")
		for _, rawBlock := range content {
			block := asMap(t, rawBlock, "input content block")
			blockType, _ := block["type"].(string)
			if blockType == contentType {
				return true
			}
		}
	}
	return false
}

func asMap(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	typed, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s must be map[string]any, got %T", name, value)
	}
	return typed
}

func asSlice(t *testing.T, value any, name string) []any {
	t.Helper()
	typed, ok := value.([]any)
	if !ok {
		t.Fatalf("%s must be []any, got %T", name, value)
	}
	return typed
}

func asIntField(t *testing.T, payload map[string]any, key string) int {
	t.Helper()
	number, ok := payload[key].(float64)
	if !ok {
		t.Fatalf("payload[%q] must be numeric, got %T", key, payload[key])
	}
	return int(number)
}

func asStringSlice(t *testing.T, value any, name string) []string {
	t.Helper()
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for index, item := range typed {
			str, ok := item.(string)
			if !ok {
				t.Fatalf("%s[%d] must be string, got %T", name, index, item)
			}
			result = append(result, str)
		}
		return result
	default:
		t.Fatalf("%s must be []string or []any, got %T", name, value)
		return nil
	}
}

func parseSSEPayload(t *testing.T, payload string) []map[string]any {
	t.Helper()
	records := strings.Split(payload, "\n\n")
	events := make([]map[string]any, 0, len(records))
	for _, record := range records {
		trimmed := strings.TrimSpace(record)
		if trimmed == "" {
			continue
		}
		lines := strings.Split(trimmed, "\n")
		eventName := ""
		dataLine := ""
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			}
			if strings.HasPrefix(line, "data: ") {
				dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
		if eventName == "" {
			t.Fatalf("malformed sse record missing event name: %q", record)
		}
		if dataLine == "" {
			t.Fatalf("malformed sse record missing data line: %q", record)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(dataLine), &decoded); err != nil {
			t.Fatalf("invalid sse json data %q: %v", dataLine, err)
		}
		decoded["_event_name"] = eventName
		events = append(events, decoded)
	}
	return events
}

func sseEventNames(events []map[string]any) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		name, _ := event["_event_name"].(string)
		result = append(result, name)
	}
	return result
}

func collectTextDeltas(events []map[string]any) []string {
	result := make([]string, 0)
	for _, event := range events {
		name, _ := event["_event_name"].(string)
		if name != "content_block_delta" {
			continue
		}
		delta, ok := event["delta"].(map[string]any)
		if !ok {
			continue
		}
		deltaType, _ := delta["type"].(string)
		if deltaType != "text_delta" {
			continue
		}
		text, _ := delta["text"].(string)
		result = append(result, text)
	}
	return result
}

func findSSEEvent(events []map[string]any, eventName string) *map[string]any {
	for index := range events {
		name, _ := events[index]["_event_name"].(string)
		if name == eventName {
			return &events[index]
		}
	}
	return nil
}

func findSSEEventByBlockType(
	events []map[string]any,
	eventName string,
	blockType string,
) *map[string]any {
	for index := range events {
		name, _ := events[index]["_event_name"].(string)
		if name != eventName {
			continue
		}
		contentBlock, ok := events[index]["content_block"].(map[string]any)
		if !ok {
			continue
		}
		if contentBlockType, _ := contentBlock["type"].(string); contentBlockType == blockType {
			return &events[index]
		}
	}
	return nil
}

func indexOfEventName(names []string, target string) int {
	for index, name := range names {
		if name == target {
			return index
		}
	}
	return -1
}
