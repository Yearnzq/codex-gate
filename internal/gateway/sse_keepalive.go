package gateway

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func sseKeepaliveInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CODEX_GATEWAY_SSE_KEEPALIVE_SECONDS"))
	if raw == "" {
		return 15 * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 15 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func startSSEKeepalive(ctx context.Context, writeHeartbeat func()) func() {
	interval := sseKeepaliveInterval()
	if interval <= 0 {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				writeHeartbeat()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
		})
	}
}

func startSSECommentKeepalive(ctx context.Context, w http.ResponseWriter, mu *sync.Mutex) func() {
	return startSSEKeepalive(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(": codex-gate keepalive\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})
}

func startMessagesPingKeepalive(ctx context.Context, w http.ResponseWriter, mu *sync.Mutex) func() {
	return startSSEKeepalive(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		writeMessagesSSEEvent(w, messagesSSEEvent{
			Name: "ping",
			Data: map[string]any{"type": "ping"},
		})
	})
}

func startOpenAIChunkKeepalive(
	ctx context.Context,
	w http.ResponseWriter,
	mu *sync.Mutex,
	id func() string,
	created int64,
	model string,
) func() {
	return startSSEKeepalive(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		writeOpenAIStreamChunk(w, id(), created, model, map[string]any{"content": ""}, nil)
	})
}
