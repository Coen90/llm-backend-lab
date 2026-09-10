package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestReplyRequestAndMixedOutput(t *testing.T) {
	client := NewClient("test-key")
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.openai.com/v1/responses" {
			t.Fatalf("unexpected target: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("missing request headers")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "gpt-5-nano" || payload["input"] != "안녕" || payload["store"] != false || payload["max_output_tokens"] != float64(2048) {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		reasoning, ok := payload["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "minimal" {
			t.Fatalf("unexpected reasoning: %#v", reasoning)
		}
		return mockResponse(200, `{
			"status":"completed",
			"output":[
				{"type":"reasoning","summary":[]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"안녕"}]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"하세요!"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}
		}`), nil
	})
	result, err := client.Reply(context.Background(), "안녕")
	if err != nil || result.Answer != "안녕하세요!" || result.Usage.TotalTokens != 30 {
		t.Fatalf("result=%+v, err=%v", result, err)
	}
}

func TestReplyResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{"invalid JSON", `<html>error</html>`, ErrInvalidResponse},
		{"empty output", `{"status":"completed","output":[]}`, ErrInvalidResponse},
		{"failed response", `{"status":"failed","error":{"message":"private upstream detail"}}`, ErrInvalidResponse},
		{"incomplete with partial answer", `{"status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"total_tokens":2048}}`, ErrIncomplete},
		{"oversized response", strings.Repeat(" ", maxResponseBytes+1), ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("test-key")
			client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return mockResponse(200, tc.body), nil
			})
			result, err := client.Reply(context.Background(), "hello")
			if !errors.Is(err, tc.want) || result.Answer != "" {
				t.Fatalf("result=%+v, err=%v", result, err)
			}
			if errors.Is(tc.want, ErrIncomplete) && result.Usage.TotalTokens != 2048 {
				t.Fatal("incomplete response lost usage")
			}
		})
	}
}

func TestReplyRefusal(t *testing.T) {
	client := NewClient("test-key")
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return mockResponse(200, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that."}]}]}`), nil
	})
	result, err := client.Reply(context.Background(), "hello")
	if err != nil || result.Answer != "I cannot help with that." {
		t.Fatalf("result=%+v, err=%v", result, err)
	}
}

func TestReplyProviderErrorDoesNotExposeBodyOrRetry(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		client := NewClient("test-key")
		calls := 0
		client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return mockResponse(status, `{"error":{"message":"secret-account-info"}}`), nil
		})
		_, err := client.Reply(context.Background(), "hello")
		var providerError *ProviderError
		if !errors.As(err, &providerError) || providerError.StatusCode != status || strings.Contains(err.Error(), "secret-account-info") || calls != 1 {
			t.Fatalf("status=%d, calls=%d, err=%v", status, calls, err)
		}
	}
}

func TestReplyCancellationAndTimeout(t *testing.T) {
	for _, useTimeout := range []bool{false, true} {
		client := NewClient("test-key")
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if useTimeout {
			client.httpClient.Timeout = 10 * time.Millisecond
			want = context.DeadlineExceeded
		}
		client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if !useTimeout {
				cancel()
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		_, err := client.Reply(ctx, "hello")
		cancel()
		if !errors.Is(err, want) {
			t.Fatalf("timeout=%v, err=%v, want=%v", useTimeout, err, want)
		}
	}
}
