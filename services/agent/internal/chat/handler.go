package chat

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
)

const maxBodyBytes = 16 << 10

type replier interface {
	Reply(context.Context, string) (llm.Result, error)
}

func NewHandler(client replier) gin.HandlerFunc {
	return func(c *gin.Context) {
		message, ok := readMessage(c)
		if !ok {
			return
		}

		// The LLM client owns the 30-second timeout; pass request cancellation through.
		result, err := client.Reply(c.Request.Context(), message)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			log.Printf("LLM request failed: %v", err)
			if errors.Is(err, context.DeadlineExceeded) {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "model request timed out"})
			} else {
				c.JSON(http.StatusBadGateway, gin.H{"error": "could not get a complete model response"})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"answer": result.Answer, "usage": result.Usage})
	}
}

func readMessage(c *gin.Context) (string, bool) {
	var request struct {
		Message string `json:"message" binding:"required"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a JSON body with message is required (max 16 KiB)"})
		return "", false
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message must not be empty"})
		return "", false
	}
	return request.Message, true
}
