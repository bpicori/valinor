// Package telegram provides the long-polling update loop for Telegram.
package telegram

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"valinor/internal/applog"
)

const (
	// DefaultPollLimit is the maximum number of updates to fetch per request.
	DefaultPollLimit = 100
	// ErrorRetryDelay is the delay between retries after an error.
	ErrorRetryDelay = 1 * time.Second
)

// UpdateHandler is called for each received update.
type UpdateHandler func(ctx context.Context, update Update) error

// PollerStats contains telemetry metrics for the poller.
type PollerStats struct {
	PollCycles       uint64    // Total number of poll requests made
	UpdatesReceived  uint64    // Total updates received from Telegram
	UpdatesProcessed uint64    // Updates successfully processed
	LastPollTime     time.Time // Timestamp of last successful poll
}

// Poller manages the long-polling loop for Telegram updates.
type Poller struct {
	client      *Client
	store       *OffsetStore
	handler     UpdateHandler
	pollTimeout int
	limit       int

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
			Offset:         offset,
			Limit:          p.limit,
			Timeout:        p.pollTimeout,
			AllowedUpdates: []string{"message", "edited_message", "callback_query"},
		}

		updates, err := p.client.GetUpdates(p.ctx, req)

		if err != nil {
			// Check if this is a context cancellation (graceful shutdown)
			if err == context.Canceled {
				return
			}

			applog.Warning("telegram poller: error fetching updates", applog.BuildMetadata("error", err))

			// Brief delay before retry to avoid hammering the API
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(ErrorRetryDelay):
				continue
			}
		}

		// Success - update stats
		p.mu.Lock()
		p.stats.LastPollTime = time.Now()
		p.mu.Unlock()

		// Track updates received
		atomic.AddUint64(&p.stats.UpdatesReceived, uint64(len(updates)))

		// Process updates
		processed := 0
		for _, update := range updates {
			// Update offset tracking
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			// Handle the update
			if err := p.handler(p.ctx, update); err != nil {
				applog.Error("telegram poller: handler error",
					applog.BuildMetadata("error", err, "update_id", update.UpdateID))
				// Continue processing other updates
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
		atomic.AddUint64(&p.stats.UpdatesProcessed, uint64(processed))

		applog.Debug("telegram poller: processed updates",
			applog.BuildMetadata("count", len(updates), "next_offset", offset, "processed", processed))
	}
}

// GetStats returns a snapshot of poller telemetry metrics.
func (p *Poller) GetStats() PollerStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return PollerStats{
		PollCycles:       atomic.LoadUint64(&p.stats.PollCycles),
		UpdatesReceived:  atomic.LoadUint64(&p.stats.UpdatesReceived),
		UpdatesProcessed: atomic.LoadUint64(&p.stats.UpdatesProcessed),
		LastPollTime:     p.stats.LastPollTime,
	}
}
