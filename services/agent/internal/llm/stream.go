package llm

import (
	"bufio"
	"context"
	"io"
	"mime"
	"strings"
)

// Stream calls onDelta synchronously as text arrives. Returning an error from
// onDelta stops reading and closes the upstream body. Usage is final only on success.
func (c *Client) Stream(ctx context.Context, message string, onDelta func(string) error) (Usage, error) {
	resp, err := c.request(ctx, message, true)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return Usage{}, ErrInvalidResponse
	}

	return readStream(ctx, resp.Body, onDelta)
}

// readStream delivers text until a terminal model event arrives.
func readStream(ctx context.Context, body io.Reader, onDelta func(string) error) (Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxEventBytes+1)
	hasText := false
	for {
		event, err := readResponseEvent(ctx, scanner)
		if err != nil {
			return Usage{}, err
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if event.Delta == "" {
				continue
			}
			hasText = hasText || strings.TrimSpace(event.Delta) != ""
			if err := onDelta(event.Delta); err != nil {
				return Usage{}, err
			}
		case "response.completed":
			if event.Response == nil || event.Response.Status != "completed" || !hasText {
				return Usage{}, ErrInvalidResponse
			}
			return event.Response.Usage, nil
		case "response.incomplete":
			return Usage{}, ErrIncomplete
		case "response.failed", "error":
			return Usage{}, ErrInvalidResponse
		}
	}
}
