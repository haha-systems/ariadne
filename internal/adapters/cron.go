package adapters

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/haha-systems/ariadne/internal/gateway"
)

// CronAdapter is a minimal in-process cron spike.
//
// It periodically constructs a gateway.Invocation (with Source set to "cron"
// and useful Metadata for traceability) and submits it to the injected
// Gateway. This demonstrates that scheduled work needs zero duplication of
// routing, execution, or result handling logic — it just calls the same
// Submit method that the MCP adapter uses.
//
// The schedule is deliberately trivial (fixed interval via time.Ticker).
// A real implementation could accept a list of jobs or parse a simple
// schedule string; no external cron library is used here.
//
// Lifecycle:
//   - NewCronAdapter(gw, interval)
//   - Start(ctx)  — launches the ticker goroutine
//   - Stop()      — stops the ticker and waits for the goroutine to exit
//
// The adapter is safe for use alongside other adapters (e.g. the MCP server
// path) because all of them share the single Gateway instance.
type CronAdapter struct {
	gw       gateway.Gateway
	interval time.Duration

	stop   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	stopped bool
}

// NewCronAdapter returns a CronAdapter wired to the given gateway.
// interval must be > 0; zero or negative values are normalized to 10s for
// the spike (still not intended for production).
func NewCronAdapter(gw gateway.Gateway, interval time.Duration) *CronAdapter {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &CronAdapter{
		gw:       gw,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start implements Adapter.
func (c *CronAdapter) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return fmt.Errorf("cron adapter: already stopped")
	}
	c.mu.Unlock()

	c.wg.Add(1)
	go c.run(ctx)
	return nil
}

func (c *CronAdapter) run(parentCtx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			inv := gateway.Invocation{
				Title:  "cron scheduled task",
				Prompt: "This is a scheduled invocation from the CronAdapter spike. It exercises the central Gateway without any special-casing in the core.",
				Labels: []string{"cron", "spike"},
				Source: "cron",
				Metadata: map[string]string{
					"adapter":   "cron",
					"interval":  c.interval.String(),
					"triggered": time.Now().UTC().Format(time.RFC3339),
				},
			}
			// We deliberately ignore the returned Run and error here for the
			// spike. In real use you would log, meter, or handle submission
			// failures (e.g. policy rejection).
			_, _ = c.gw.Submit(context.Background(), inv)

		case <-parentCtx.Done():
			return
		case <-c.stop:
			return
		}
	}
}

// Stop implements Adapter. It is idempotent.
func (c *CronAdapter) Stop() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	close(c.stop)
	c.mu.Unlock()

	c.wg.Wait()
	return nil
}

// Ensure CronAdapter satisfies Adapter (compile-time check).
var _ Adapter = (*CronAdapter)(nil)
