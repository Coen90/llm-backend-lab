package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
	"github.com/gin-gonic/gin"
)

func stopStream(t *testing.T, ctx context.Context, serverURL, id string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/chat/streams/"+id+"/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestStopKeepsSSEConnectionAndSendsUserCancelled(t *testing.T) {
	for _, withDelta := range []bool{false, true} {
		t.Run(fmt.Sprint(withDelta), func(t *testing.T) {
			allowFinish := make(chan struct{})
			modelCancelled := make(chan struct{})
			server := httptest.NewServer(streamRouter(streamFunc(func(ctx context.Context, _ string, emit func(string) error) (llm.Usage, error) {
				if withDelta {
					if err := emit("partial"); err != nil {
						return llm.Usage{}, err
					}
				}
				<-ctx.Done()
				close(modelCancelled)
				select {
				case <-allowFinish:
				case <-time.After(3 * time.Second):
					t.Error("test did not release model")
				}
				return llm.Usage{}, ctx.Err()
			})))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/chat/stream", strings.NewReader(`{"message":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			id := readStarted(t, reader)
			if withDelta {
				frame, err := readFrame(reader)
				if err != nil || !strings.HasPrefix(frame, "event: delta\n") {
					t.Fatalf("delta=%q err=%v", frame, err)
				}
			}
			// Stop also works before the first model token arrives.
			if status := stopStream(t, ctx, server.URL, id); status != 202 {
				t.Fatalf("stop status=%d", status)
			}
			select {
			case <-modelCancelled:
			case <-ctx.Done():
				t.Fatal("model call was not cancelled")
			}
			// Repeating a stop while cleanup is pending is harmless.
			if status := stopStream(t, ctx, server.URL, id); status != 202 {
				t.Fatalf("repeat status=%d", status)
			}
			close(allowFinish)
			tail, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("SSE connection closed before cancellation event: %v", err)
			}
			body := normalizeSSE(string(tail))
			want := "event: user_cancelled\ndata: {\"message\":\"사용자가 답변 생성을 중단했습니다.\"}\n\n"
			if body != want {
				t.Fatalf("terminal event=%q", body)
			}
			if status := stopStream(t, ctx, server.URL, id); status != 404 {
				t.Fatalf("finished stream still registered: status=%d", status)
			}
		})
	}
}

func TestStopAndFinishChooseOneOutcome(t *testing.T) {
	registry := NewStreamRegistry()
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		id := registry.register(cancel)
		start := make(chan struct{})
		stopResult, finishResult := make(chan bool, 1), make(chan bool, 1)
		go func() { <-start; stopResult <- registry.stop(id) }()
		go func() { <-start; finishResult <- registry.finish(id) }()
		close(start)
		accepted, stopped := <-stopResult, <-finishResult
		if accepted != stopped {
			t.Fatalf("accepted=%v stopped=%v", accepted, stopped)
		}
		if accepted && ctx.Err() != context.Canceled {
			t.Fatal("accepted stop did not cancel model context")
		}
		if registry.stop(id) {
			t.Fatal("finished stream still registered")
		}
		cancel()
	}
}

func TestStoppingOneStreamDoesNotCancelAnother(t *testing.T) {
	registry := NewStreamRegistry()
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	idA, idB := registry.register(cancelA), registry.register(cancelB)
	if idA == idB || !registry.stop(idA) {
		t.Fatal("invalid stream IDs")
	}
	if ctxA.Err() != context.Canceled || ctxB.Err() != nil {
		t.Fatalf("A=%v B=%v", ctxA.Err(), ctxB.Err())
	}
	if !registry.finish(idA) || registry.finish(idB) {
		t.Fatal("incorrect stop state")
	}
}

func TestAcceptedStopWinsOverConcurrentSuccessfulReturn(t *testing.T) {
	registry := NewStreamRegistry()
	var id string
	router := gin.New()
	router.POST("/chat/stream", NewStreamHandler(streamFunc(func(_ context.Context, _ string, emit func(string) error) (llm.Usage, error) {
		if err := emit("hello"); err != nil {
			return llm.Usage{}, err
		}
		registry.mu.Lock()
		for activeID := range registry.active {
			id = activeID
		}
		registry.mu.Unlock()
		if !registry.stop(id) {
			t.Fatal("stop was not accepted")
		}
		// Model completion can race with the stop signal.
		return llm.Usage{TotalTokens: 5}, nil
	}), registry))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, streamRequest(`{"message":"hello"}`))
	body := normalizeSSE(w.Body.String())
	if strings.Count(body, "event: user_cancelled\n") != 1 || strings.Contains(body, "event: done") || strings.Contains(body, "event: error") {
		t.Fatalf("body=%s", body)
	}
	if registry.stop(id) {
		t.Fatal("stream was not removed")
	}
}

func TestStopForFinishedOrUnknownStream(t *testing.T) {
	router := streamRouter(streamFunc(func(_ context.Context, _ string, emit func(string) error) (llm.Usage, error) {
		if err := emit("hello"); err != nil {
			return llm.Usage{}, err
		}
		return llm.Usage{}, nil
	}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, streamRequest(`{"message":"hello"}`))
	id := readStarted(t, bufio.NewReader(w.Body))
	for _, target := range []string{id, "unknown"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/chat/streams/"+target+"/stop", nil))
		if w.Code != 404 {
			t.Fatalf("status=%d", w.Code)
		}
		var payload map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload["error"] == "" {
			t.Fatalf("payload=%v err=%v", payload, err)
		}
	}
}
