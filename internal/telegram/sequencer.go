// Package telegram provides per-chat sequential message processing.
package telegram

import (
	"context"
	"sync"

	"valinor/internal/applog"
)

// WorkItemFunc is the function type for processing a work item.
type WorkItemFunc func(ctx context.Context, item *WorkItem) error

// Sequencer ensures ordered processing of work items within each chat.
// Each chat key (including control lanes) gets its own sequential worker queue.
// This mirrors TUI's single-threaded Update() loop but adapted for multi-chat.
type Sequencer struct {
	workFn WorkItemFunc

	// queues maps sequential keys to their work queues
	queues map[string]chan *WorkItem
	// cancelFuncs maps sequential keys to their context cancel functions
	cancelFuncs map[string]context.CancelFunc
	// wg tracks active workers per key
	wg map[string]*sync.WaitGroup

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSequencer creates a new per-chat sequencer.
func NewSequencer(workFn WorkItemFunc) *Sequencer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Sequencer{
		workFn:      workFn,
		queues:      make(map[string]chan *WorkItem),
		cancelFuncs: make(map[string]context.CancelFunc),
		wg:          make(map[string]*sync.WaitGroup),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Stop gracefully stops all worker queues and waits for completion.
func (s *Sequencer) Stop() {
	s.cancel()

	s.mu.Lock()
	// Cancel all worker contexts
	for _, cancel := range s.cancelFuncs {
		cancel()
	}
	// Collect all wait groups
	groups := make([]*sync.WaitGroup, 0, len(s.wg))
	for _, wg := range s.wg {
		groups = append(groups, wg)
	}
	s.mu.Unlock()

	// Wait for all workers to finish outside the lock
	for _, wg := range groups {
		wg.Wait()
	}

	applog.Info("telegram sequencer: stopped", nil)
}

// Submit queues a work item for sequential processing by its sequential key.
// The work item is processed in order with other items sharing the same key.
// This allows both commands and gateway events to be serialized per chat.
func (s *Sequencer) Submit(item *WorkItem, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if we're shutting down
	select {
	case <-s.ctx.Done():
		return false
	default:
	}

	// Get or create the queue for this key
	queue, ok := s.queues[key]
	if !ok {
		// Create new worker queue for this key
		queue = make(chan *WorkItem, 64)
		s.queues[key] = queue

		// Create per-key context
		keyCtx, keyCancel := context.WithCancel(s.ctx)
		s.cancelFuncs[key] = keyCancel

		// Create wait group for this key
		wg := &sync.WaitGroup{}
		s.wg[key] = wg

		// Start worker goroutine for this key
		wg.Add(1)
		go s.worker(keyCtx, key, queue, wg)
	}

	// Try to queue the work item (non-blocking)
	select {
	case queue <- item:
		return true
	default:
		applog.Warning("telegram sequencer: queue full, dropping work item",
			applog.BuildMetadata("key", key, "request_id", item.RequestID))
		return false
	}
}

// worker processes work items for a single sequential key in order.
// This is the per-chat equivalent of TUI's Update() loop.
func (s *Sequencer) worker(ctx context.Context, key string, queue chan *WorkItem, wg *sync.WaitGroup) {
	defer wg.Done()
	defer s.cleanupWorker(key)

	applog.Debug("telegram sequencer: worker started", applog.BuildMetadata("key", key))

	for {
		select {
		case <-ctx.Done():
			applog.Debug("telegram sequencer: worker exiting", applog.BuildMetadata("key", key))
			return

		case item, ok := <-queue:
			if !ok {
				return
			}

			// Process the work item
			if err := s.workFn(ctx, item); err != nil {
				applog.Error("telegram sequencer: work function error",
					applog.BuildMetadata("key", key, "error", err, "request_id", item.RequestID))
			}
		}
	}
}

// cleanupWorker removes the worker's resources when it exits.
func (s *Sequencer) cleanupWorker(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.queues, key)
	delete(s.cancelFuncs, key)
	delete(s.wg, key)

	applog.Debug("telegram sequencer: worker cleaned up", applog.BuildMetadata("key", key))
}

// QueueDepth returns the current queue depth for a given sequential key.
// Useful for metrics and monitoring.
func (s *Sequencer) QueueDepth(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	queue, ok := s.queues[key]
	if !ok {
		return 0
	}

	return len(queue)
}

// ActiveKeys returns the number of active sequential keys (worker queues).
// Useful for metrics and monitoring.
func (s *Sequencer) ActiveKeys() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.queues)
}

// TotalQueued returns the total number of queued work items across all keys.
// Useful for metrics and monitoring.
func (s *Sequencer) TotalQueued() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, queue := range s.queues {
		total += len(queue)
	}
	return total
}

// SequencerStats contains telemetry metrics for the sequencer.
type SequencerStats struct {
	ActiveKeys  int            // Number of active sequential keys
	TotalQueued int            // Total work items queued across all keys
	QueueDepths map[string]int // Per-key queue depths
}

// GetStats returns a snapshot of sequencer telemetry metrics.
func (s *Sequencer) GetStats() SequencerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	depths := make(map[string]int, len(s.queues))
	for key, queue := range s.queues {
		depths[key] = len(queue)
	}

	return SequencerStats{
		ActiveKeys:  len(s.queues),
		TotalQueued: s.TotalQueued(),
		QueueDepths: depths,
	}
}
