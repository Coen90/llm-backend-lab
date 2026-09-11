package chat

import (
	"context"
	"crypto/rand"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

type streamControl struct {
	cancel  context.CancelFunc
	stopped bool
}

// StreamRegistry tracks active model calls in this server process.
type StreamRegistry struct {
	mu     sync.Mutex
	active map[string]*streamControl
}

func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{active: make(map[string]*streamControl)}
}

func (r *StreamRegistry) register(cancel context.CancelFunc) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := rand.Text()
	r.active[id] = &streamControl{cancel: cancel}
	return id
}

func (r *StreamRegistry) stop(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	control, ok := r.active[id]
	if !ok {
		return false
	}
	control.stopped = true
	control.cancel()
	return true
}

// finish removes the call and reports whether stop won the race with completion.
func (r *StreamRegistry) finish(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	control, ok := r.active[id]
	delete(r.active, id)
	return ok && control.stopped
}

func NewStopHandler(streams *StreamRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !streams.stop(c.Param("id")) {
			c.JSON(http.StatusNotFound, gin.H{"error": "stream is not active"})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "stop_requested"})
	}
}
