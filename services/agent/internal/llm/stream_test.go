package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const completedFrame = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"

func deltaFrame(text string) string {
	data, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": text})
	return "data: " + string(data) + "\n\n"
}

func eventResponse(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: body}
}

// Use the production Client against a local HTTP server, never the real provider.
func streamTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	client := NewClient("test-key")
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cloned := r.Clone(r.Context())
		cloned.URL.Scheme, cloned.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(cloned)
	})
	return client
}

func TestStreamDeliversBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	client := streamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload responseRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || !payload.Stream || payload.Input != "질문" || payload.Model != Model || payload.Store || payload.MaxOutputTokens != 2048 || payload.Reasoning.Effort != "minimal" {
			t.Error("incorrect streaming request")
		}
		if r.Method != "POST" || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("incorrect request target or authentication")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "\uFEFF: heartbeat\r\n\r\nevent: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n")
		// Multiple data lines are one JSON event. UTF-8 bytes may arrive separately.
		frame := "event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"안녕\\n\"}\r\n\r\n"
		for i := range len(frame) {
			_, _ = w.Write([]byte{frame[i]})
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
			fmt.Fprint(w, deltaFrame("하세요!"), "data: {\"type\":\"response.output_text.done\",\"text\":\"안녕\\n하세요!\"}\n\n", completedFrame)
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deltas := make(chan string, 2)
	type outcome struct {
		usage Usage
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		usage, err := client.Stream(ctx, "질문", func(s string) error { deltas <- s; return nil })
		result <- outcome{usage, err}
	}()
	select {
	case first := <-deltas:
		if first != "안녕\n" {
			t.Fatalf("first delta=%q", first)
		}
	case <-ctx.Done():
		t.Fatal("first delta was buffered until completion")
	}
	close(release)
	select {
	case got := <-result:
		if got.err != nil || got.usage.TotalTokens != 5 {
			t.Fatalf("result=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal("stream did not complete")
	}
	if second := <-deltas; second != "하세요!" {
		t.Fatalf("second delta=%q", second)
	}
	if len(deltas) != 0 {
		t.Fatal("completion duplicated text")
	}
}

func TestStreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"truncated", deltaFrame("partial"), ErrInvalidResponse},
		{"unfinished event", deltaFrame("partial") + strings.TrimSpace(completedFrame), ErrInvalidResponse},
		{"invalid JSON", "data: invalid\n\n", ErrInvalidResponse},
		{"legacy done marker", "data: [DONE]\n\n", ErrInvalidResponse},
		{"missing event type", "data: {}\n\n", ErrInvalidResponse},
		{"empty completion", completedFrame, ErrInvalidResponse},
		{"blank answer", deltaFrame(" \n") + completedFrame, ErrInvalidResponse},
		{"missing response", deltaFrame("partial") + "data: {\"type\":\"response.completed\"}\n\n", ErrInvalidResponse},
		{"wrong completion status", deltaFrame("partial") + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n", ErrInvalidResponse},
		{"incomplete", deltaFrame("partial") + "data: {\"type\":\"response.incomplete\"}\n\n", ErrIncomplete},
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", ErrInvalidResponse},
		{"error", "data: {\"type\":\"error\",\"message\":\"private-account-info\"}\n\n", ErrInvalidResponse},
		{"oversized line", "data: " + strings.Repeat("x", maxEventBytes+1), nil},
		{"oversized multiline event", strings.Repeat("data: "+strings.Repeat("x", 1024)+"\n", 1024) + "\n", ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("test-key")
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return eventResponse(io.NopCloser(strings.NewReader(tc.body))), nil
			})
			_, err := client.Stream(context.Background(), "hello", func(string) error { return nil })
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || strings.Contains(err.Error(), "private-account-info") {
				t.Fatalf("error=%v, want=%v", err, tc.want)
			}
		})
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestStreamClosesOnCallbackError(t *testing.T) {
	client := NewClient("test-key")
	body := &trackedBody{Reader: strings.NewReader(deltaFrame("hi") + completedFrame)}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return eventResponse(body), nil })
	want := errors.New("reader disconnected")
	_, err := client.Stream(context.Background(), "hello", func(string) error { return want })
	if !errors.Is(err, want) || !body.closed {
		t.Fatalf("err=%v closed=%v", err, body.closed)
	}
}

func TestStreamProviderAndContentTypeErrors(t *testing.T) {
	for _, status := range []int{200, 401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := NewClient("test-key")
			calls := 0
			body := &trackedBody{Reader: strings.NewReader(`{"secret":"private-account-info"}`)}
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
			})
			_, err := client.Stream(context.Background(), "hello", func(string) error { t.Fatal("unexpected delta"); return nil })
			if err == nil || !body.closed || calls != 1 || strings.Contains(err.Error(), "private-account-info") {
				t.Fatalf("err=%v closed=%v calls=%d", err, body.closed, calls)
			}
			if status != 200 {
				var provider *ProviderError
				if !errors.As(err, &provider) || provider.StatusCode != status {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}

func TestStreamRefusalAndUnknownEvents(t *testing.T) {
	client := NewClient("test-key")
	body := "data: {\"type\":\"future.event\"}\n\ndata: {\"type\":\"response.refusal.delta\",\"delta\":\"거절 안내\"}\n\n" + completedFrame
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return eventResponse(io.NopCloser(strings.NewReader(body))), nil
	})
	var text string
	_, err := client.Stream(context.Background(), "hello", func(s string) error { text += s; return nil })
	if err != nil || text != "거절 안내" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestStreamCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			cancelled := make(chan struct{})
			client := streamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, deltaFrame("hi"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(cancelled)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			want := context.Canceled
			if timeout {
				client.httpClient.Timeout = 30 * time.Millisecond
				want = context.DeadlineExceeded
			}
			_, err := client.Stream(ctx, "hello", func(string) error {
				if !timeout {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream connection was not cancelled")
			}
		})
	}
}
