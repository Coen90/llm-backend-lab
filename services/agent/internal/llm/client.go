// Package llm calls OpenAI's Responses API using the Go standard library.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	Model            = "gpt-5-nano"
	maxOutputTokens  = 2048 // Includes reasoning tokens, not just the visible answer.
	maxResponseBytes = 2 << 20
)

var (
	ErrIncomplete      = errors.New("model response is incomplete")
	ErrInvalidResponse = errors.New("invalid model response")
)

// ProviderError deliberately excludes the upstream body, which can contain
// account details. The HTTP status is enough for the handler to classify it.
type ProviderError struct {
	StatusCode int
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("OpenAI returned HTTP %d", e.StatusCode)
}

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Reply(ctx context.Context, message string) (Result, error) {
	resp, err := c.request(ctx, message, false)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return Result{}, ErrInvalidResponse
	}
	var response responseBody
	if err := json.Unmarshal(body, &response); err != nil {
		return Result{}, ErrInvalidResponse
	}
	result := Result{Usage: response.Usage}
	if response.Status == "incomplete" {
		return result, ErrIncomplete
	}
	if response.Status != "completed" {
		return result, ErrInvalidResponse
	}
	var answer strings.Builder
	// Reasoning and tool items can precede the assistant's message.
	for _, item := range response.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, content := range item.Content {
			switch content.Type {
			case "output_text":
				answer.WriteString(content.Text)
			case "refusal":
				answer.WriteString(content.Refusal)
			}
		}
	}
	result.Answer = strings.TrimSpace(answer.String())
	if result.Answer == "" {
		return result, ErrInvalidResponse
	}
	return result, nil
}

// request shares authentication and model settings between both response modes.
func (c *Client) request(ctx context.Context, message string, stream bool) (*http.Response, error) {
	payload := responseRequest{
		Model:           Model,
		Input:           message,
		Store:           false,
		MaxOutputTokens: maxOutputTokens,
		Reasoning:       reasoningConfig{Effort: "minimal"},
		Stream:          stream,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call OpenAI: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, &ProviderError{StatusCode: resp.StatusCode}
	}
	return resp, nil
}
