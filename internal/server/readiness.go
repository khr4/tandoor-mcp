package server

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

const (
	readySuccessTTL = 5 * time.Second
	readyFailureTTL = 2 * time.Second
)

// TandoorReadyCheck returns the production readiness probe used by /readyz.
func TandoorReadyCheck(c *tandoor.Client) func(context.Context) error {
	return func(ctx context.Context) error {
		q := url.Values{"page_size": []string{"1"}}
		_, err := c.Do(ctx, http.MethodGet, "recipe/", q, nil)
		return err
	}
}

type readyProbe struct {
	check func(context.Context) error

	mu      sync.Mutex
	ready   bool
	expires time.Time
	running bool
	done    chan struct{}
}

func newReadyProbe(check func(context.Context) error) *readyProbe {
	return &readyProbe{check: check}
}

func (p *readyProbe) checkReady(ctx context.Context) bool {
	if p == nil || p.check == nil {
		return false
	}
	for {
		p.mu.Lock()
		now := time.Now()
		if now.Before(p.expires) {
			ready := p.ready
			p.mu.Unlock()
			return ready
		}
		if p.running {
			done := p.done
			p.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return false
			}
		}
		done := make(chan struct{})
		p.running = true
		p.done = done
		p.mu.Unlock()

		err := p.check(ctx)
		ready := err == nil
		if err != nil {
			log.Printf("readiness check failed: %s", logErrorSummary(err))
		}

		p.mu.Lock()
		p.ready = ready
		if ready {
			p.expires = time.Now().Add(readySuccessTTL)
		} else {
			p.expires = time.Now().Add(readyFailureTTL)
		}
		p.running = false
		close(done)
		p.mu.Unlock()
		return ready
	}
}
