/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package retry provides utilities for retrying operations with exponential backoff
// and classification of transient vs permanent errors.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"syscall"
	"time"

	"github.com/go-logr/logr"
)

// Constants for retry configuration
const (
	// DefaultMaxRetries is the default maximum number of retry attempts
	DefaultMaxRetries = 3
	// DefaultInitialDelay is the default initial delay between retries
	DefaultInitialDelay = 100 * time.Millisecond
	// DefaultMaxDelay is the default maximum delay between retries
	DefaultMaxDelay = 10 * time.Second
	// DefaultBackoffFactor is the default exponential backoff factor
	DefaultBackoffFactor = 2.0
	// DefaultJitter is the default jitter factor to add randomness
	DefaultJitter = 0.1
)

// Config holds the configuration for retry operations
type Config struct {
	// MaxRetries is the maximum number of retry attempts (excluding the initial attempt)
	MaxRetries int
	// InitialDelay is the initial delay between retries
	InitialDelay time.Duration
	// MaxDelay is the maximum delay between retries
	MaxDelay time.Duration
	// BackoffFactor is the exponential backoff factor
	BackoffFactor float64
	// Jitter adds randomness to delays to avoid thundering herd
	Jitter float64
	// Logger for logging retry attempts
	Logger logr.Logger
	// TestDelay is the delay to add to the retry loop to simulate a test environment
	TestDelay time.Duration
}

// DefaultConfig returns a default retry configuration
func DefaultConfig() Config {
	return Config{
		MaxRetries:    DefaultMaxRetries,
		InitialDelay:  DefaultInitialDelay,
		MaxDelay:      DefaultMaxDelay,
		BackoffFactor: DefaultBackoffFactor,
		Jitter:        DefaultJitter,
	}
}

// RetryableError represents an error that can be retried
type RetryableError struct {
	Underlying error
	Retryable  bool
	Operation  string
}

func (e *RetryableError) Error() string {
	retryableStr := "non-retryable"
	if e.Retryable {
		retryableStr = "retryable"
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s operation failed: %v (%s)", e.Operation, e.Underlying, retryableStr)
	}
	return fmt.Sprintf("%v (%s)", e.Underlying, retryableStr)
}

func (e *RetryableError) Unwrap() error {
	return e.Underlying
}

// NewRetryableError creates a new retryable error
func NewRetryableError(err error, retryable bool, operation string) *RetryableError {
	return &RetryableError{
		Underlying: err,
		Retryable:  retryable,
		Operation:  operation,
	}
}

// IsTransientError determines if an error is transient and should be retried
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}

	// Check for RetryableError wrapper
	var retryableErr *RetryableError
	if errors.As(err, &retryableErr) {
		return retryableErr.Retryable
	}

	// Check for specific error types that are typically transient
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		switch pathErr.Err {
		case syscall.EIO, syscall.EAGAIN, syscall.EBUSY, syscall.EINTR, syscall.ETIMEDOUT:
			return true
		case syscall.ENOENT, syscall.EACCES, syscall.EPERM:
			return false // Permanent errors
		}
	}

	// Check for syscall errors directly
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EIO, syscall.EAGAIN, syscall.EBUSY, syscall.EINTR, syscall.ETIMEDOUT:
			return true
		case syscall.ENOENT, syscall.EACCES, syscall.EPERM:
			return false
		}
	}

	// Check error message for common transient patterns
	errMsg := err.Error()
	transientPatterns := []string{
		"device or resource busy",
		"temporary failure",
		"try again",
		"timeout",
		"connection refused",
		"no such device",
		"i/o error",
	}

	for _, pattern := range transientPatterns {
		if contains(errMsg, pattern) {
			return true
		}
	}

	// Default to non-retryable for unknown errors
	return false
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					containsSubstring(s, substr))))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// RetryFunc is a function that can be retried
type RetryFunc func() error

// Do executes a function with retry logic using exponential backoff
func Do(ctx context.Context, config Config, operation string, fn RetryFunc) error {
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		// Execute the function
		err := fn()
		if err == nil {
			// Success!
			if attempt > 0 && config.Logger.GetSink() != nil {
				config.Logger.Info("Operation succeeded after retries",
					"operation", operation,
					"attempts", attempt+1)
			}
			return nil
		}

		lastErr = err

		// Check if this is the last attempt
		if attempt == config.MaxRetries {
			break
		}

		// Check if the error is retryable
		if !IsTransientError(err) {
			if config.Logger.GetSink() != nil {
				config.Logger.Info("Permanent error encountered, not retrying",
					"operation", operation,
					"attempt", attempt+1,
					"error", err)
			}
			return err
		}

		// Calculate delay with exponential backoff and jitter
		delay := calculateDelay(config, attempt)

		if config.Logger.GetSink() != nil {
			config.Logger.Info("Operation failed, retrying",
				"operation", operation,
				"attempt", attempt+1,
				"maxAttempts", config.MaxRetries+1,
				"error", err,
				"retryDelay", delay)
		}

		// Wait before retrying (with context cancellation support)
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation cancelled during retry: %w", ctx.Err())
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// All attempts failed
	return fmt.Errorf("operation %s failed after %d attempts, last error: %w",
		operation, config.MaxRetries+1, lastErr)
}

// calculateDelay calculates the delay for the next retry attempt using exponential backoff
func calculateDelay(config Config, attempt int) time.Duration {
	if config.InitialDelay <= 0 {
		config.InitialDelay = DefaultInitialDelay
	}
	if config.BackoffFactor <= 1.0 {
		config.BackoffFactor = DefaultBackoffFactor
	}
	if config.MaxDelay <= 0 {
		config.MaxDelay = DefaultMaxDelay
	}

	// Calculate exponential backoff delay
	delay := float64(config.InitialDelay)
	for i := 0; i < attempt; i++ {
		delay *= config.BackoffFactor
	}

	// Apply maximum delay cap
	if time.Duration(delay) > config.MaxDelay {
		delay = float64(config.MaxDelay)
	}

	// Add jitter to avoid thundering herd
	if config.Jitter > 0 {
		jitterAmount := delay * config.Jitter
		// Simple jitter: +/- jitterAmount
		delay += (jitterAmount * 2 * (rand.Float64() - 0.5)) // Random jitter
	}

	return time.Duration(delay)
}
