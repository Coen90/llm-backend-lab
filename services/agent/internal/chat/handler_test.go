package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
)

func init() { gin.SetMode(gin.TestMode) }

type replyFunc func(context.Context, string) (llm.Result, error)

func (f replyFunc) Reply(ctx context.Context, message string) (llm.Result, error) {
	return f(ctx, message)
}

func testRouter(client replier) http.Handler {
	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.POST("/chat", NewHandler(client))
	return router
}

func postChat(router http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestChatSuccess(t *testing.T) {
	router := testRouter(replyFunc(func(_ context.Context, message string) (llm.Result, error) {
		if message != "안녕" {
			t.Fatalf("message=%q", message)
		}
		return llm.Result{Answer: "안녕하세요!"}, nil
	}))
	w := postChat(router, `{"message":"  안녕  "}`)
	var response struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.Answer != "안녕하세요!" {
		t.Fatalf("status=%d, body=%s", w.Code, w.Body)
	}
}

func TestInvalidRequestsNeverCallModel(t *testing.T) {
	for _, body := range []string{
		`{"message":"  "}`, `{}`, `null`, `{"message":123}`, `{`,
		`{"message":"` + strings.Repeat("x", maxBodyBytes) + `"}`,
	} {
		router := testRouter(replyFunc(func(context.Context, string) (llm.Result, error) {
			t.Fatal("invalid input reached model")
			return llm.Result{}, nil
		}))
		w := postChat(router, body)
		if w.Code != 400 {
			t.Fatalf("status=%d, body=%s", w.Code, w.Body)
		}
	}
}

func TestRouting(t *testing.T) {
	router := testRouter(replyFunc(func(context.Context, string) (llm.Result, error) {
		t.Fatal("unexpected model call")
		return llm.Result{}, nil
	}))
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/chat", 405}, {"POST", "/other", 404},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("status=%d, want=%d", w.Code, tc.status)
		}
	}
}

func TestChatErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"timeout", context.DeadlineExceeded, 504},
		{"rate limit", &llm.ProviderError{StatusCode: 429}, 502},
		{"provider auth", &llm.ProviderError{StatusCode: 401}, 502},
		{"incomplete", llm.ErrIncomplete, 502},
		{"malformed response", llm.ErrInvalidResponse, 502},
		{"network failure", errors.New("private upstream detail"), 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := testRouter(replyFunc(func(context.Context, string) (llm.Result, error) {
				return llm.Result{}, tc.err
			}))
			w := postChat(router, `{"message":"hi"}`)
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || response.Error == "" || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("status=%d, body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestCallerCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := testRouter(replyFunc(func(modelCtx context.Context, _ string) (llm.Result, error) {
		cancel()
		if modelCtx.Err() != context.Canceled {
			t.Fatal("caller cancellation did not reach model context")
		}
		return llm.Result{}, modelCtx.Err()
	}))
	req := httptest.NewRequest("POST", "/chat", strings.NewReader(`{"message":"hi"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Body.Len() != 0 {
		t.Fatalf("wrote response after disconnect: %s", w.Body)
	}
}
