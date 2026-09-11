package chat

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
)

type streamer interface {
	Stream(context.Context, string, func(string) error) (llm.Usage, error)
}

type startedEvent struct {
	StreamID string `json:"stream_id"`
}

type cancelledEvent struct {
	Message string `json:"message"`
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

func NewStreamHandler(client streamer, streams *StreamRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		message, ok := readMessage(c)
		if !ok {
			return
		}
		requestCtx := c.Request.Context()
		modelCtx, cancelModel := context.WithCancel(requestCtx)
		defer cancelModel()
		id := streams.register(cancelModel)
		defer streams.finish(id)

		c.Header("X-Accel-Buffering", "no")
		if err := sendEvent(c, "started", startedEvent{StreamID: id}); err != nil {
			return
		}
		usage, err := client.Stream(modelCtx, message, func(text string) error {
			if err := modelCtx.Err(); err != nil {
				return err
			}
			return sendEvent(c, "delta", deltaEvent{Text: text})
		})
		stopped := streams.finish(id)
		if requestCtx.Err() != nil || c.IsAborted() {
			return
		}
		if stopped {
			_ = sendEvent(c, "user_cancelled", cancelledEvent{Message: "사용자가 답변 생성을 중단했습니다."})
			return
		}
		if err != nil {
			_ = sendEvent(c, "error", streamError(err))
			return
		}
		_ = sendEvent(c, "done", doneEvent{Usage: usage})
	}
}

// Sending depends on the user's connection, not the cancellable model call.
func sendEvent(c *gin.Context, event string, payload any) error {
	if err := c.Request.Context().Err(); err != nil {
		return err
	}
	c.SSEvent(event, payload)
	if err := c.Errors.Last(); err != nil {
		return err
	}
	c.Writer.Flush()
	return c.Request.Context().Err()
}

func streamError(err error) errorEvent {
	if errors.Is(err, context.DeadlineExceeded) {
		return errorEvent{Code: "timeout", Message: "model request timed out"}
	}
	if errors.Is(err, llm.ErrIncomplete) {
		return errorEvent{Code: "incomplete_response", Message: "model response is incomplete"}
	}
	return errorEvent{Code: "model_error", Message: "could not get a complete model response"}
}
