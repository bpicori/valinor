// Package telegram provides network error classification for resilience.
package telegram

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"
)

// IsRecoverableNetworkError returns true if the error is transient
// and the polling/request should be retried rather than failing permanently.
func IsRecoverableNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Context cancellation is not recoverable (it's intentional shutdown)
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Deadline exceeded (timeout) is generally recoverable - the network may recover
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check for specific HTTP status codes that are transient
	var httpErr interface{ HTTPStatusCode() int }
	if errors.As(err, &httpErr) {
		code := httpErr.HTTPStatusCode()
		// Gateway errors and service unavailable are transient
		if code == http.StatusBadGateway ||
			code == http.StatusServiceUnavailable ||
			code == http.StatusGatewayTimeout {
			return true
		}
	}

	// Check for network-level transient errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		// Temporary errors include timeouts
		if netErr.Timeout() {
			return true
		}
		if netErr.Temporary() {
			return true
		}
	}

	// Check for syscall-level errors (connection reset, broken pipe, etc.)
	var syscallErr *net.OpError
	if errors.As(err, &syscallErr) {
		if errors.Is(syscallErr.Err, syscall.ECONNRESET) ||
			errors.Is(syscallErr.Err, syscall.ECONNABORTED) ||
			errors.Is(syscallErr.Err, syscall.EPIPE) ||
			errors.Is(syscallErr.Err, syscall.ETIMEDOUT) {
			return true
		}
	}

	// Check for URL errors which often wrap network issues
	var urlErr interface{ Timeout() bool }
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return true
		}
	}

	return false
}

// IsContextCancellation returns true if the error is due to context cancellation.
func IsContextCancellation(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled)
}

// IsContextDeadlineExceeded returns true if the error is due to deadline exceeded.
func IsContextDeadlineExceeded(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded)
}
