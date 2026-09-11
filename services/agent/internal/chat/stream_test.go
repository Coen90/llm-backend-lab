package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
	"github.com/gin-gonic/gin"
)

type streamFunc func(context.Context, string, func(string) error) (llm.Usage, error)

func (f streamFunc) Stream(ctx context.Context, s string, emit func(string) error) (llm.Usage, error) {
	return f(ctx, s, emit)
}
func streamRouter(client streamer) http.Handler {
	router := gin.New()
	streams := NewStreamRegistry()
	router.POST("/chat/stream", NewStreamHandler(client, streams))
	router.POST("/chat/streams/:id/stop", NewStopHandler(streams))
	return router
}
func streamRequest(body string) *http.Request {
	r := httptest.NewRequest("POST", "/chat/stream", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestStreamInvalidInputDoesNotCallModel(t *testing.T) {
	router := streamRouter(streamFunc(func(context.Context, string, func(string) error) (llm.Usage, error) {
		t.Fatal("invalid input reached model")
		return llm.Usage{}, nil
	}))
	for _, body := range []string{`{}`, `null`, `{`, `{"message":"  "}`, `{"message":123}`, `{"message":"` + strings.Repeat("x", maxBodyBytes) + `"}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, streamRequest(body))
		if w.Code != 400 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
}

func TestStreamErrorsAfterStarted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"timeout", context.DeadlineExceeded, "timeout"},
		{"incomplete", llm.ErrIncomplete, "incomplete_response"},
		{"provider", &llm.ProviderError{StatusCode: 429}, "model_error"},
		{"unexpected EOF", llm.ErrInvalidResponse, "model_error"},
		{"private error", errors.New("private-account-info"), "model_error"},
	} {
		for _, withDelta := range []bool{false, true} {
			name := tc.name + " before delta"
			if withDelta {
				name = tc.name + " after delta"
			}
			t.Run(name, func(t *testing.T) {
				router := streamRouter(streamFunc(func(_ context.Context, _ string, emit func(string) error) (llm.Usage, error) {
					if withDelta {
						if err := emit("partial"); err != nil {
							return llm.Usage{}, err
						}
					}
					return llm.Usage{}, tc.err
				}))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, streamRequest(`{"message":"hello"}`))
				body := normalizeSSE(w.Body.String())
				if w.Code != 200 || !strings.HasPrefix(body, "event: started\n") || strings.Count(body, "event: error\n") != 1 || strings.Contains(body, "event: done") || strings.Contains(body, "event: user_cancelled") {
					t.Fatalf("status=%d body=%s", w.Code, body)
				}
				if strings.Contains(body, "event: delta\n") != withDelta || !strings.Contains(body, tc.code) || strings.Contains(body, "private-account-info") {
					t.Fatalf("body=%s", body)
				}
				if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
					t.Fatal("missing SSE content type")
				}
			})
		}
	}
}

func readFrame(reader *bufio.Reader) (string, error) {
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return frame.String(), err
		}
		frame.WriteString(line)
		if line == "\n" {
			return normalizeSSE(frame.String()), nil
		}
	}
}

func TestStreamFlushesOverHTTPBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(streamRouter(streamFunc(func(ctx context.Context, message string, emit func(string) error) (llm.Usage, error) {
		if message != "hello" {
			t.Errorf("message=%q", message)
		}
		if err := emit("안녕\n\"친구\""); err != nil {
			return llm.Usage{}, err
		}
		select {
		case <-release:
		case <-ctx.Done():
			return llm.Usage{}, ctx.Err()
		}
		if err := emit("!"); err != nil {
			return llm.Usage{}, err
		}
		return llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, nil
	})))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/chat/stream", strings.NewReader(`{"message":"  hello  "}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("headers and first delta were not flushed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("response=%+v", resp)
	}
	reader := bufio.NewReader(resp.Body)
	_ = readStarted(t, reader)
	first, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if first != "event: delta\ndata: {\"text\":\"안녕\\n\\\"친구\\\"\"}\n\n" {
		t.Fatalf("first=%q", first)
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	want := "event: delta\ndata: {\"text\":\"!\"}\n\nevent: done\ndata: {\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}\n\n"
	if normalizeSSE(string(rest)) != want {
		t.Fatalf("rest=%q", rest)
	}
}

func TestStreamDisconnectCancelsModel(t *testing.T) {
	cancelled := make(chan struct{})
	server := httptest.NewServer(streamRouter(streamFunc(func(ctx context.Context, _ string, emit func(string) error) (llm.Usage, error) {
		if err := emit("hi"); err != nil {
			return llm.Usage{}, err
		}
		<-ctx.Done()
		close(cancelled)
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
	reader := bufio.NewReader(resp.Body)
	_ = readStarted(t, reader)
	_, err = readFrame(reader)
	if err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel model")
	}
}

type failingWriter struct{ *httptest.ResponseRecorder }

func (w failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func (w failingWriter) WriteString(string) (int, error) { return 0, errors.New("broken pipe") }

func TestStreamWriteFailureStopsModel(t *testing.T) {
	streams := NewStreamRegistry()
	router := gin.New()
	called := false
	router.POST("/chat/stream", NewStreamHandler(streamFunc(func(context.Context, string, func(string) error) (llm.Usage, error) {
		called = true
		return llm.Usage{}, nil
	}), streams))
	w := failingWriter{httptest.NewRecorder()}
	router.ServeHTTP(w, streamRequest(`{"message":"hello"}`))
	if called || w.Body.Len() != 0 || len(streams.active) != 0 {
		t.Fatalf("called=%v body=%s active=%d", called, w.Body, len(streams.active))
	}
}

func readStarted(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	frame, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(frame, "event: started\n") {
		t.Fatalf("first frame=%q", frame)
	}
	var event startedEvent
	data := strings.TrimSuffix(strings.TrimPrefix(frame, "event: started\ndata: "), "\n\n")
	if err := json.Unmarshal([]byte(data), &event); err != nil || event.StreamID == "" {
		t.Fatalf("started=%q err=%v", data, err)
	}
	return event.StreamID
}

// SSE allows an optional space after the field colon.
func normalizeSSE(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		field, value, ok := strings.Cut(line, ":")
		if ok && (field == "event" || field == "data") {
			lines[i] = field + ": " + strings.TrimPrefix(value, " ")
		}
	}
	return strings.Join(lines, "\n")
}
