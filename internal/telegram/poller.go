// Package telegram provides the long-polling update loop for Telegram.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// offsetStore persists and retrieves the last processed update_id.
// This ensures we don't process the same updates multiple times after restarts.
type offsetStore struct {
	path string
	mu   sync.RWMutex
	data offsetData
}

type offsetData struct {
	LastUpdateID int64 `json:"last_update_id"`
}

// newOffsetStore creates a new offset store in the specified directory.
func newOffsetStore(dir string) (*offsetStore, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create offset store directory: %w", err)
	}

	store := &offsetStore{
		path: filepath.Join(dir, offsetFileName),
	}

	// Load existing data if present
	if err := store.load(); err != nil {
		applog.Warning("telegram offset store: failed to load, starting fresh",
			applog.BuildMetadata("error", err))
	}

	return store, nil
}

// Get returns the last processed update ID.
// Returns 0 if no offset has been stored.
func (s *offsetStore) Get() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LastUpdateID
}

// Set updates the last processed update ID and persists it to disk.
func (s *offsetStore) Set(updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Only update if the new ID is higher
	if updateID <= s.data.LastUpdateID {
		return nil
	}

	s.data.LastUpdateID = updateID
	return s.saveLocked()
}

// load reads the offset data from disk.
func (s *offsetStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing file is fine
		}
		return fmt.Errorf("read offset file: %w", err)
	}

	var loaded offsetData
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse offset file: %w", err)
	}

	s.data = loaded
	return nil
}

// saveLocked persists the current offset to disk.
// Must be called with s.mu held.
func (s *offsetStore) saveLocked() error {
	data, err := json.Marshal(s.data)
	if err != nil {
		return fmt.Errorf("marshal offset data: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0640); err != nil {
		return fmt.Errorf("write offset temp file: %w", err)
	}

	if err := os.Rename(tempPath, s.path); err != nil {
		os.Remove(tempPath) // Clean up on failure
		return fmt.Errorf("rename offset file: %w", err)
	}

	return nil
}

const offsetFileName = "telegram_offset.json"

// pollerStats contains telemetry metrics for the poller.
type pollerStats struct {
	pollCycles       uint64    // Total number of poll requests made
	updatesReceived  uint64    // Total updates received from Telegram
	updatesProcessed uint64    // Updates successfully processed
	lastPollTime     time.Time // Timestamp of last successful poll
}

// Poller manages the long-polling loop for Telegram updates.
type Poller struct {
	client      *Client
	store       *offsetStore
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
	stats pollerStats
}

// NewPoller creates a new update poller.
func NewPoller(client *Client, store *offsetStore, handler UpdateHandler) *Poller {
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
		atomic.AddUint64(&p.stats.pollCycles, 1)

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
			if errors.Is(err, context.Canceled) {
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
		p.stats.lastPollTime = time.Now()
		p.mu.Unlock()

		// Track updates received
		atomic.AddUint64(&p.stats.updatesReceived, uint64(len(updates)))

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
		atomic.AddUint64(&p.stats.updatesProcessed, uint64(processed))

		applog.Debug("telegram poller: processed updates",
			applog.BuildMetadata("count", len(updates), "next_offset", offset, "processed", processed))
	}
}

