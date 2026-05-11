package codexcli

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"codex-gate/internal/codexclient"
)

func TestCreateUsesPromptPlaceholderArgs(t *testing.T) {
	client, err := New(Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--", promptToken},
		Env:     []string{"CODEXCLI_TEST_HELPER=1"},
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := client.Create(context.Background(), map[string]any{
		"instructions": "Return only the answer.",
		"input": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "Reply with helper-ok."},
				},
			},
		},
	}, zeroCallOptions())
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	text, err := completionText(result.Response)
	if err != nil {
		t.Fatalf("completion text failed: %v", err)
	}
	if !strings.Contains(text, "Return only the answer.") ||
		!strings.Contains(text, "Reply with helper-ok.") {
		t.Fatalf("expected helper to receive full prompt, got %q", text)
	}
}

func TestStreamSynthesizesResponseEvents(t *testing.T) {
	client, err := New(Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     []string{"CODEXCLI_TEST_HELPER=1"},
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	result, err := client.Stream(context.Background(), map[string]any{
		"input": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "stream me"}},
			},
		},
	}, zeroCallOptions())
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if result.FinalStatus != "completed" {
		t.Fatalf("unexpected final status %q", result.FinalStatus)
	}
	if len(result.Events) < 3 {
		t.Fatalf("expected synthetic start/delta/completed events, got %#v", result.Events)
	}
	deltas := make([]string, 0)
	for _, event := range result.Events {
		if event["event"] == "response.output_text.delta" {
			deltas = append(deltas, event["delta"].(string))
		}
	}
	if !strings.Contains(strings.Join(deltas, "\n"), "stream me") {
		t.Fatalf("unexpected stream delta events: %#v", result.Events)
	}
}

func TestStreamEventsEmitsDeltaBeforeProcessExit(t *testing.T) {
	client, err := New(Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     []string{"CODEXCLI_TEST_HELPER=stream_json_slow"},
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	deltaSeen := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := client.StreamEvents(context.Background(), map[string]any{
			"input": []any{
				map[string]any{
					"role":    "user",
					"content": []any{map[string]any{"type": "input_text", "text": "stream me"}},
				},
			},
		}, zeroCallOptions(), func(event map[string]any) error {
			if event["event"] == "response.output_text.delta" && event["delta"] == "first-delta" {
				deltaSeen <- struct{}{}
			}
			return nil
		})
		done <- err
	}()

	select {
	case <-deltaSeen:
	case err := <-done:
		t.Fatalf("stream returned before delta callback: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streamed delta")
	}

	select {
	case err := <-done:
		t.Fatalf("stream finished before helper delay, err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream completion")
	}
}

func TestCreateMapsExit124ToTimeout(t *testing.T) {
	client, err := New(Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     []string{"CODEXCLI_TEST_HELPER=exit124"},
		Logger:  log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new client failed: %v", err)
	}

	_, err = client.Create(context.Background(), map[string]any{
		"input": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "slow"}},
			},
		},
	}, zeroCallOptions())
	if err == nil {
		t.Fatal("expected timeout error")
	}
	clientErr, ok := err.(*codexclient.ClientError)
	if !ok {
		t.Fatalf("expected ClientError, got %T", err)
	}
	if clientErr.Kind != codexclient.KindNetworkTimeout {
		t.Fatalf("expected network timeout kind, got %q", clientErr.Kind)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("CODEXCLI_TEST_HELPER") == "exit124" {
		os.Exit(124)
	}
	if os.Getenv("CODEXCLI_TEST_HELPER") == "stream_json_slow" {
		writeJSONLine(map[string]any{"type": "thread.started", "thread_id": "thread_test"})
		writeJSONLine(map[string]any{
			"type": "item.completed",
			"item": map[string]any{
				"id":   "item_0",
				"type": "agent_message",
				"text": "first-delta",
			},
		})
		time.Sleep(400 * time.Millisecond)
		writeJSONLine(map[string]any{"type": "turn.completed"})
		os.Exit(0)
	}
	if os.Getenv("CODEXCLI_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for index, arg := range args {
		if arg == "--" && index+1 < len(args) {
			_, _ = os.Stdout.WriteString(args[index+1])
			os.Exit(0)
		}
	}
	body, _ := io.ReadAll(os.Stdin)
	_, _ = os.Stdout.Write(body)
	os.Exit(0)
}

func writeJSONLine(value map[string]any) {
	encoded, _ := json.Marshal(value)
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

func zeroCallOptions() codexclient.CallOptions {
	return codexclient.CallOptions{}
}
