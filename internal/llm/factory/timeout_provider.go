package factory

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"time"

	"valinor/internal/llm"
)

const (
	streamMaxAttempts = 3
	streamBaseBackoff = time.Second
	streamJitterMax   = 250 * time.Millisecond
)

type timeoutProvider struct {
	inner llm.Provider
}

func withRequestTimeouts(provider llm.Provider) llm.Provider {
	return &timeoutProvider{inner: provider}
}

func (p *timeoutProvider) Name() string {
	return p.inner.Name()
}

// Stream retries transient provider failures before any delta is emitted.
// A partial response is never replayed, so tool calls are never duplicated.
func (p *timeoutProvider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	var emitted bool
	originalDelta := params.OnDelta
	params.OnDelta = func(delta llm.StreamDelta) {
		emitted = true
		if originalDelta != nil {
			originalDelta(delta)
		}
	}

	var lastErr error
	for attempt := 0; attempt < streamMaxAttempts; attempt++ {
		if attempt > 0 {
			if emitted || !isRetryableStreamError(lastErr) {
				return nil, lastErr
			}
			if !sleepContext(ctx, backoffFor(attempt-1)) {
				return nil, lastErr
			}
		}

		emitted = false
		result, err := p.inner.Stream(ctx, params)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (p *timeoutProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	return p.inner.ListModels(ctx)
}

func (p *timeoutProvider) TranscribeAudio(ctx context.Context, params llm.AudioTranscribeParams) (string, error) {
	transcriber, ok := p.inner.(llm.AudioTranscriber)
	if !ok {
		return "", errors.New("provider does not support audio transcription")
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	return transcriber.TranscribeAudio(ctx, params)
}

// isRetryableStreamError reports whether a provider error is worth retrying.
func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"429",
		"500",
		"502",
		"503",
		"504",
		"rate limit",
		"overloaded",
		"temporarily unavailable",
		"connection reset",
		"broken pipe",
		"unexpected eof",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func backoffFor(attempt int) time.Duration {
	backoff := streamBaseBackoff << attempt
	if backoff > 15*time.Second {
		backoff = 15 * time.Second
	}
	return backoff + time.Duration(rand.Int63n(int64(streamJitterMax)))
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
