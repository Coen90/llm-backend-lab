package main

import (
	"log"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Coen90/llm-backend-lab/services/agent/internal/chat"
	"github.com/Coen90/llm-backend-lab/services/agent/internal/llm"
)

func main() {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY must be set")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	router := gin.Default()
	_ = router.SetTrustedProxies(nil)
	router.HandleMethodNotAllowed = true
	client := llm.NewClient(apiKey)
	router.POST("/chat", chat.NewHandler(client))
	router.POST("/chat/stream", chat.NewStreamHandler(client))
	if err := router.Run(addr); err != nil {
		log.Fatal(err)
	}
}
