// Package telegram provides the long-polling update loop for Telegram.
package telegram

import (
	"context"
	"errors"
	"sync"
	"time"

	"valinor/internal/logger"
)

const (
	// DefaultPollLimit is the maximum number of updates to fetch per request.
	DefaultPollLimit = 100
	// ErrorRetryDelay is the delay between retries after an error.
	ErrorRetryDelay = 1 * time.Second
)

// Poller fetches updates from Telegram and sends them to a channel.
type Poller struct {
	client      *Client
	pollTimeout int
	limit       int
	startOffset int64

	// Updates is the channel where received updates are sent.
	// Consumers should read from this channel to process updates.
	Updates chan Update

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPoller creates a new update poller with a buffered update channel.
func NewPoller(client *Client, bufferSize int, startOffset int64) *Poller {
	if bufferSize <= 0 {
		bufferSize = DefaultPollLimit
	}
	return &Poller{
		client:      client,
		Updates:     make(chan Update, bufferSize),
		pollTimeout: DefaultLongPollTimeout,
		limit:       DefaultPollLimit,
		startOffset: startOffset,
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
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go p.pollLoop()

	logger.Info("telegram poller: started", logger.BuildMetadata(
		"poll_timeout", p.pollTimeout,
		"limit", p.limit,
		"start_offset", p.startOffset,
	))
}

// Stop gracefully stops the polling loop and closes the updates channel.
func (p *Poller) Stop() {
	p.cancel()
	p.wg.Wait()
	close(p.Updates)
	logger.Info("telegram poller: stopped", nil)
}

// pollLoop continuously fetches updates from Telegram and sends them to the channel.
func (p *Poller) pollLoop() {
	defer p.wg.Done()

	offset := p.startOffset

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		req := GetUpdatesRequest{
			Offset:         offset,
			Limit:          p.limit,
			Timeout:        p.pollTimeout,
			AllowedUpdates: []string{"message", "edited_message"},
		}

		updates, err := p.client.GetUpdates(p.ctx, req)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}

			logger.Warning("telegram poller: error fetching updates", logger.BuildMetadata("error", err))

			select {
			case <-p.ctx.Done():
				return
			case <-time.After(ErrorRetryDelay):
				continue
			}
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			select {
			case <-p.ctx.Done():
				return
			case p.Updates <- update:
			}
		}

		logger.Debug("telegram poller: fetched updates",
			logger.BuildMetadata("count", len(updates), "next_offset", offset))
	}
}
