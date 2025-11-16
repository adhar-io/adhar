package provider

import (
	"context"
	"fmt"
	"math"
	"time"
)

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxAttempts   int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	Timeout       time.Duration
}

// DefaultRetryConfig returns a sensible default retry configuration
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxAttempts:   5,
		InitialDelay:  time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		Timeout:       5 * time.Minute,
	}
}

// RetryableFunc is a function that can be retried
type RetryableFunc func() error

// RetryableFuncWithResult is a function that can be retried and returns a result
type RetryableFuncWithResult[T any] func() (T, error)

// Retry executes a function with retry logic
func Retry(ctx context.Context, config *RetryConfig, operation string, fn RetryableFunc) error {
	if config == nil {
		config = DefaultRetryConfig()
	}

	var lastErr error
	delay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation '%s' cancelled: %w", operation, ctx.Err())
		default:
		}

		// Execute the function
		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		// Don't retry on the last attempt
		if attempt == config.MaxAttempts {
			break
		}

		// Check if error is retryable
		if !IsRetryableError(err) {
			return fmt.Errorf("operation '%s' failed with non-retryable error: %w", operation, err)
		}

		// Wait before retry
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation '%s' cancelled during retry: %w", operation, ctx.Err())
		case <-time.After(delay):
		}

		// Calculate next delay with exponential backoff
		delay = time.Duration(float64(delay) * config.BackoffFactor)
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}
	}

	return fmt.Errorf("operation '%s' failed after %d attempts: %w", operation, config.MaxAttempts, lastErr)
}

// RetryWithResult executes a function with retry logic and returns a result
func RetryWithResult[T any](ctx context.Context, config *RetryConfig, operation string, fn RetryableFuncWithResult[T]) (T, error) {
	if config == nil {
		config = DefaultRetryConfig()
	}

	var lastErr error
	var result T
	delay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("operation '%s' cancelled: %w", operation, ctx.Err())
		default:
		}

		// Execute the function
		res, err := fn()
		if err == nil {
			return res, nil
		}

		lastErr = err
		result = res

		// Don't retry on the last attempt
		if attempt == config.MaxAttempts {
			break
		}

		// Check if error is retryable
		if !IsRetryableError(err) {
			return result, fmt.Errorf("operation '%s' failed with non-retryable error: %w", operation, err)
		}

		// Wait before retry
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("operation '%s' cancelled during retry: %w", operation, ctx.Err())
		case <-time.After(delay):
		}

		// Calculate next delay with exponential backoff
		delay = time.Duration(float64(delay) * config.BackoffFactor)
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}
	}

	return result, fmt.Errorf("operation '%s' failed after %d attempts: %w", operation, config.MaxAttempts, lastErr)
}

// IsRetryableError determines if an error should be retried
func IsRetryableError(err error) bool {
	// Network errors are usually retryable
	if IsErrorType(err, &NetworkError{}) {
		return true
	}

	// Timeout errors are retryable
	if IsErrorType(err, &TimeoutError{}) {
		return true
	}

	// Check for common retryable error patterns
	errStr := err.Error()
	retryablePatterns := []string{
		"timeout",
		"connection refused",
		"network",
		"temporary",
		"rate limit",
		"throttle",
		"service unavailable",
		"internal server error",
		"bad gateway",
		"gateway timeout",
	}

	for _, pattern := range retryablePatterns {
		if contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(len(s) == len(substr) ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					containsSubstring(s, substr))))
}

// containsSubstring is a simple substring check
func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ExponentialBackoff calculates exponential backoff delay
func ExponentialBackoff(attempt int, baseDelay time.Duration, maxDelay time.Duration) time.Duration {
	delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

// Jitter adds random jitter to avoid thundering herd
func Jitter(delay time.Duration, factor float64) time.Duration {
	if factor <= 0 {
		factor = 0.1
	}

	jitter := time.Duration(float64(delay) * factor)
	return delay + time.Duration(float64(jitter)*float64(time.Now().UnixNano()%1000)/1000.0)
}
