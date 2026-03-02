// Package telegram provides per-chat sequential work processing.
package telegram

import (
	"context"
	"sync"

	"valinor/internal/gateway"
	"valinor/internal/logger"
)

// WorkItemType distinguishes between different work types.
type WorkItemType int

const (
	// WorkItemCommand is a user command from Telegram
	WorkItemCommand WorkItemType = iota
	// WorkItemEvent is a gateway event for this chat
	WorkItemEvent
)

// WorkItem represents a unit of work to be processed sequentially per chat.
// This is similar to how TUI's Update() receives both user input and gateway events.
type WorkItem struct {
	// Type indicates what kind of work this is
	Type WorkItemType

	// For command work items
	Command *Command

	// For event work items (stored by value for safety)
	Event gateway.Event

	// RequestID for correlation
	RequestID string

	// Identity for event routing (only used for events, populated before submission)
	Identity ChatIdentity
}

// IsControlCommand returns true if this work item is a control command.
func (wi *WorkItem) IsControlCommand() bool {
	if wi.Type != WorkItemCommand || wi.Command == nil {
		return false
	}
	return wi.Command.IsControlCommand
}

// GetSequentialKey returns the appropriate sequential key for this work item.
func (wi *WorkItem) GetSequentialKey() string {
	switch wi.Type {
	case WorkItemCommand:
		if wi.Command != nil {
			return wi.Command.GetSequentialKey()
		}
	case WorkItemEvent:
		// Events are pre-routed with their identity
		// They use the same sequential key as commands for ordering
		return wi.Identity.SequentialKey()
	}
	return ""
}

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

	logger.Info("telegram sequencer: stopped", nil)
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
		logger.Warning("telegram sequencer: queue full, dropping work item",
			logger.BuildMetadata("key", key, "request_id", item.RequestID))
		return false
	}
}

// worker processes work items for a single sequential key in order.
// This is the per-chat equivalent of TUI's Update() loop.
func (s *Sequencer) worker(ctx context.Context, key string, queue chan *WorkItem, wg *sync.WaitGroup) {
	defer wg.Done()
	defer s.cleanupWorker(key)

	logger.Debug("telegram sequencer: worker started", logger.BuildMetadata("key", key))

	for {
		select {
		case <-ctx.Done():
			logger.Debug("telegram sequencer: worker exiting", logger.BuildMetadata("key", key))
			return

		case item, ok := <-queue:
			if !ok {
				return
			}

			// Process the work item
			if err := s.workFn(ctx, item); err != nil {
				logger.Error("telegram sequencer: work function error",
					logger.BuildMetadata("key", key, "error", err, "request_id", item.RequestID))
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

	logger.Debug("telegram sequencer: worker cleaned up", logger.BuildMetadata("key", key))
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
