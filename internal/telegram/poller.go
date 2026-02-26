// Package telegram provides the long-polling update loop for Telegram.
package telegram

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"valinor/internal/applog"
)

const (
	// DefaultPollLimit is the maximum number of updates to fetch per request.
	DefaultPollLimit = 100
	// MaxBackoff is the maximum exponential backoff for poll restarts.
	MaxBackoff = 30 * time.Second
	// InitialBackoff is the starting backoff duration.
	InitialBackoff = 1 * time.Second
)

// UpdateHandler is called for each received update.
type UpdateHandler func(ctx context.Context, update Update) error

// PollerStats contains telemetry metrics for the poller.
type PollerStats struct {
	PollCycles      uint64        // Total number of poll requests made
	UpdatesReceived uint64        // Total updates received from Telegram
	UpdatesDropped  uint64        // Updates dropped due to dedup or errors
	UpdatesProcessed uint64       // Updates successfully processed
	Retries         uint64        // Number of retry attempts after errors
	LastPollTime    time.Time     // Timestamp of last successful poll
	LastErrorTime   time.Time     // Timestamp of last error
	LastError       string        // Last error message (if any)
	CurrentBackoff  time.Duration // Current retry backoff
}

// Poller manages the long-polling loop for Telegram updates.
type Poller struct {
	client       *Client
	store        *OffsetStore
	handler      UpdateHandler
	pollTimeout  int
	limit        int

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
	running bool

	// Telemetry
	stats PollerStats
}

// NewPoller creates a new update poller.
func NewPoller(client *Client, store *OffsetStore, handler UpdateHandler) *Poller {
	return &Poller{
		client:      client,
		store:       store,
		handler:     handler,
		pollTimeout: DefaultLongPollTimeout,
		limit:       DefaultPollLimit,
	}
}

// SetPollTimeout configures the long-polling timeout (0-90 seconds).
func (p *Poller) SetPollTimeout(seconds int) {
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 90 {
		seconds = 90
	}
	p.pollTimeout = seconds
}

// SetLimit configures the maximum updates per request (1-100).
func (p *Poller) SetLimit(limit int) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	p.limit = limit
}

// Start begins the polling loop.
func (p *Poller) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return
	}

	p.ctx, p.cancel = context.WithCancel(ctx)
	p.running = true

	p.wg.Add(1)
	go p.pollLoop()

	applog.Info("telegram poller: started", applog.BuildMetadata(
		"poll_timeout", p.pollTimeout,
		"limit", p.limit,
	))
}

// Stop gracefully stops the polling loop.
func (p *Poller) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	p.cancel()
	p.mu.Unlock()

	p.wg.Wait()
	applog.Info("telegram poller: stopped", nil)
}

// IsRunning returns true if the poller is running.
func (p *Poller) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// pollLoop is the main polling goroutine.
func (p *Poller) pollLoop() {
	defer p.wg.Done()

	// Start from the stored offset + 1
	offset := p.store.Get() + 1
	backoff := InitialBackoff

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		// Track poll cycle
		atomic.AddUint64(&p.stats.PollCycles, 1)

		// Fetch updates with long polling
		req := GetUpdatesRequest{
			Offset:  offset,
			Limit:   p.limit,
			Timeout: p.pollTimeout,
			AllowedUpdates: []string{"message", "edited_message", "callback_query"},
		}

		updates, err := p.client.GetUpdates(p.ctx, req)

		if err != nil {
			// Check if this is a context cancellation (graceful shutdown)
			if IsContextCancellation(err) {
				return
			}

			// Update error stats
			p.mu.Lock()
			p.stats.LastErrorTime = time.Now()
			p.stats.LastError = err.Error()
			p.stats.CurrentBackoff = backoff
			p.mu.Unlock()

			// Check if this is a recoverable network error
			if IsRecoverableNetworkError(err) {
				atomic.AddUint64(&p.stats.Retries, 1)
				applog.Warning("telegram poller: recoverable error, retrying",
					applog.BuildMetadata("error", err, "backoff", backoff))

				// Wait with backoff and jitter
				jitter := time.Duration(rand.Int63n(int64(backoff / 4)))
				sleepTime := backoff + jitter

				select {
				case <-p.ctx.Done():
					return
				case <-time.After(sleepTime):
					// Exponential backoff
					backoff *= 2
					if backoff > MaxBackoff {
						backoff = MaxBackoff
					}
					continue
				}
			}

		// Check for API errors that might be recoverable
		if apiErr, ok := err.(*APIResponse); ok {
			if apiErr.IsRecoverableError() {
				atomic.AddUint64(&p.stats.Retries, 1)
				if apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
					backoff = time.Duration(apiErr.Parameters.RetryAfter) * time.Second
				}

				applog.Warning("telegram poller: API error, retrying",
					applog.BuildMetadata("error", apiErr, "backoff", backoff))

				select {
				case <-p.ctx.Done():
					return
				case <-time.After(backoff):
					continue
				}
			}

			// Non-recoverable API error
			applog.Error("telegram poller: non-recoverable API error",
				applog.BuildMetadata("error", apiErr))
			return
		}

			// Non-recoverable error
			applog.Error("telegram poller: fatal error",
				applog.BuildMetadata("error", err))
			return
		}

		// Success - reset backoff and update stats
		backoff = InitialBackoff
		p.mu.Lock()
		p.stats.LastPollTime = time.Now()
		p.stats.CurrentBackoff = 0
		p.mu.Unlock()

		// Track updates received
		atomic.AddUint64(&p.stats.UpdatesReceived, uint64(len(updates)))

		// Process updates
		dropped := 0
		processed := 0
		for _, update := range updates {
			// Update offset tracking
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			// Skip updates we've already processed (dedup via offset store)
			if update.UpdateID <= p.store.Get() {
				dropped++
				applog.Debug("telegram poller: skipping duplicate update",
					applog.BuildMetadata("update_id", update.UpdateID))
				continue
			}

			// Handle the update
			if err := p.handler(p.ctx, update); err != nil {
				applog.Error("telegram poller: handler error",
					applog.BuildMetadata("error", err, "update_id", update.UpdateID))
				// Continue processing other updates - don't let one bad update stop the loop
			} else {
				processed++
			}

			// Persist the offset after successful processing
			if err := p.store.Set(update.UpdateID); err != nil {
				applog.Warning("telegram poller: failed to persist offset",
					applog.BuildMetadata("error", err, "update_id", update.UpdateID))
			}
		}

		// Update stats
		atomic.AddUint64(&p.stats.UpdatesDropped, uint64(dropped))
		atomic.AddUint64(&p.stats.UpdatesProcessed, uint64(processed))

		applog.Debug("telegram poller: processed updates",
			applog.BuildMetadata("count", len(updates), "next_offset", offset, "dropped", dropped, "processed", processed))
	}
}

// GetStats returns a snapshot of poller telemetry metrics.
func (p *Poller) GetStats() PollerStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return PollerStats{
		PollCycles:       atomic.LoadUint64(&p.stats.PollCycles),
		UpdatesReceived:  atomic.LoadUint64(&p.stats.UpdatesReceived),
		UpdatesDropped:   atomic.LoadUint64(&p.stats.UpdatesDropped),
		UpdatesProcessed: atomic.LoadUint64(&p.stats.UpdatesProcessed),
		Retries:          atomic.LoadUint64(&p.stats.Retries),
		LastPollTime:     p.stats.LastPollTime,
		LastErrorTime:    p.stats.LastErrorTime,
		LastError:        p.stats.LastError,
		CurrentBackoff:   p.stats.CurrentBackoff,
	}
}
