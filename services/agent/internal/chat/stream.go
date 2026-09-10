package chat

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
)

type streamer interface {
	Stream(context.Context, string, func(string) error) (llm.Usage, error)
}

type deltaEvent struct {
	Text string `json:"text"`
}

type doneEvent struct {
	Usage llm.Usage `json:"usage"`
}

type errorEvent struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewStreamHandler(client streamer) gin.HandlerFunc {
	return func(c *gin.Context) {
		message, ok := readMessage(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		c.Header("X-Accel-Buffering", "no")
		send := func(event string, payload any) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			c.SSEvent(event, payload)
			if err := c.Errors.Last(); err != nil {
				return err
			}
			c.Writer.Flush()
			return ctx.Err()
		}
		usage, err := client.Stream(ctx, message, func(text string) error {
			return send("delta", deltaEvent{Text: text})
		})
		if ctx.Err() != nil || c.IsAborted() {
			return
		}
		if err != nil {
			status, payload := streamError(err)
			if !c.Writer.Written() {
				c.JSON(status, gin.H{"error": payload.Message, "code": payload.Code})
			} else {
				_ = send("error", payload)
			}
			return
		}
		_ = send("done", doneEvent{Usage: usage})
	}
}

func streamError(err error) (int, errorEvent) {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, errorEvent{Code: "timeout", Message: "model request timed out"}
	}
	if errors.Is(err, llm.ErrIncomplete) {
		return http.StatusBadGateway, errorEvent{Code: "incomplete_response", Message: "model response is incomplete"}
	}
	return http.StatusBadGateway, errorEvent{Code: "model_error", Message: "could not get a complete model response"}
}
