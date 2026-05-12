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

package retry

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "EIO syscall error",
			err:      syscall.EIO,
			expected: true,
		},
		{
			name:     "EAGAIN syscall error",
			err:      syscall.EAGAIN,
			expected: true,
		},
		{
			name:     "EBUSY syscall error",
			err:      syscall.EBUSY,
			expected: true,
		},
		{
			name:     "ENOENT syscall error",
			err:      syscall.ENOENT,
			expected: false,
		},
		{
			name:     "EACCES syscall error",
			err:      syscall.EACCES,
			expected: false,
		},
		{
			name:     "PathError with EIO",
			err:      &os.PathError{Op: "open", Path: "/dev/test", Err: syscall.EIO},
			expected: true,
		},
		{
			name:     "PathError with ENOENT",
			err:      &os.PathError{Op: "open", Path: "/dev/test", Err: syscall.ENOENT},
			expected: false,
		},
		{
			name:     "RetryableError marked as retryable",
			err:      NewRetryableError(errors.New("test error"), true, "test operation"),
			expected: true,
		},
		{
			name:     "RetryableError marked as non-retryable",
			err:      NewRetryableError(errors.New("test error"), false, "test operation"),
			expected: false,
		},
		{
			name:     "Generic error with transient message",
			err:      errors.New("device or resource busy"),
			expected: true,
		},
		{
			name:     "Generic error with timeout message",
			err:      errors.New("operation timeout"),
			expected: true,
		},
		{
			name:     "Generic error with non-transient message",
			err:      errors.New("invalid argument"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTransientError(tt.err)
			if result != tt.expected {
				t.Errorf("IsTransientError() = %v, expected %v for error: %v", result, tt.expected, tt.err)
			}
		})
	}
}

func TestRetryableError(t *testing.T) {
	originalErr := errors.New("original error")
	retryableErr := NewRetryableError(originalErr, true, "test operation")

	// Test Error() method
	expectedMsg := "test operation operation failed: original error (retryable)"
	if retryableErr.Error() != expectedMsg {
		t.Errorf("Error() = %q, expected %q", retryableErr.Error(), expectedMsg)
	}

	// Test Unwrap() method
	if unwrapped := retryableErr.Unwrap(); unwrapped != originalErr {
		t.Errorf("Unwrap() = %v, expected %v", unwrapped, originalErr)
	}

	// Test retryable flag
	if !retryableErr.Retryable {
		t.Errorf("Retryable = %v, expected true", retryableErr.Retryable)
	}
}

func TestCalculateDelay(t *testing.T) {
	config := Config{
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      1 * time.Second,
		BackoffFactor: 2.0,
		Jitter:        0, // No jitter for predictable testing
	}

	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{
			name:     "first retry (attempt 0)",
			attempt:  0,
			expected: 100 * time.Millisecond,
		},
		{
			name:     "second retry (attempt 1)",
			attempt:  1,
			expected: 200 * time.Millisecond,
		},
		{
			name:     "third retry (attempt 2)",
			attempt:  2,
			expected: 400 * time.Millisecond,
		},
		{
			name:     "fourth retry (attempt 3)",
			attempt:  3,
			expected: 800 * time.Millisecond,
		},
		{
			name:     "fifth retry (attempt 4) - capped at max",
			attempt:  4,
			expected: 1 * time.Second,
		},
		{
			name:     "sixth retry (attempt 5) - still capped",
			attempt:  5,
			expected: 1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := calculateDelay(config, tt.attempt)
			if delay != tt.expected {
				t.Errorf("calculateDelay() = %v, expected %v", delay, tt.expected)
			}
		})
	}
}

func TestDoSuccess(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.Logger = logr.Discard()

	callCount := 0
	err := Do(ctx, config, "test operation", func() error {
		callCount++
		return nil // Success on first try
	})

	if err != nil {
		t.Errorf("Do() returned error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Function called %d times, expected 1", callCount)
	}
}

func TestDoRetryableError(t *testing.T) {
	ctx := context.Background()
	config := Config{
		MaxRetries:    2,
		InitialDelay:  1 * time.Millisecond, // Very short for testing
		MaxDelay:      10 * time.Millisecond,
		BackoffFactor: 2.0,
		Logger:        logr.Discard(),
	}

	callCount := 0
	err := Do(ctx, config, "test operation", func() error {
		callCount++
		if callCount < 3 {
			return NewRetryableError(errors.New("transient error"), true, "test")
		}
		return nil // Success on third try
	})

	if err != nil {
		t.Errorf("Do() returned error: %v", err)
	}

	if callCount != 3 {
		t.Errorf("Function called %d times, expected 3", callCount)
	}
}

func TestDoNonRetryableError(t *testing.T) {
	ctx := context.Background()
	config := DefaultConfig()
	config.Logger = logr.Discard()

	callCount := 0
	expectedErr := NewRetryableError(errors.New("permanent error"), false, "test")
	err := Do(ctx, config, "test operation", func() error {
		callCount++
		return expectedErr
	})

	if err == nil {
		t.Errorf("Do() should have returned an error")
	}

	if callCount != 1 {
		t.Errorf("Function called %d times, expected 1 (no retries for non-retryable error)", callCount)
	}

	// Check that the error is the expected one
	var retryableErr *RetryableError
	if !errors.As(err, &retryableErr) || retryableErr.Retryable {
		t.Errorf("Expected non-retryable error, got: %v", err)
	}
}

func TestDoMaxRetriesExceeded(t *testing.T) {
	ctx := context.Background()
	config := Config{
		MaxRetries:    2,
		InitialDelay:  1 * time.Millisecond,
		MaxDelay:      10 * time.Millisecond,
		BackoffFactor: 2.0,
		Logger:        logr.Discard(),
	}

	callCount := 0
	err := Do(ctx, config, "test operation", func() error {
		callCount++
		return NewRetryableError(errors.New("persistent transient error"), true, "test")
	})

	if err == nil {
		t.Errorf("Do() should have returned an error after max retries")
	}

	expectedCalls := config.MaxRetries + 1 // Initial attempt + retries
	if callCount != expectedCalls {
		t.Errorf("Function called %d times, expected %d", callCount, expectedCalls)
	}

	// Check error message contains retry information
	if !contains(err.Error(), "failed after") {
		t.Errorf("Error message should indicate retry failure: %v", err)
	}
}

func TestDoContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	config := Config{
		MaxRetries:    5,
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      1 * time.Second,
		BackoffFactor: 2.0,
		Logger:        logr.Discard(),
	}

	callCount := 0
	err := Do(ctx, config, "test operation", func() error {
		callCount++
		if callCount == 2 {
			// Cancel context during retry delay
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancel()
			}()
		}
		return NewRetryableError(errors.New("transient error"), true, "test")
	})

	if err == nil {
		t.Errorf("Do() should have returned an error due to context cancellation")
	}

	if !contains(err.Error(), "cancelled") {
		t.Errorf("Error should indicate context cancellation: %v", err)
	}

	// Should have been called at least twice before cancellation
	if callCount < 2 {
		t.Errorf("Function called %d times, expected at least 2", callCount)
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.MaxRetries != DefaultMaxRetries {
		t.Errorf("MaxRetries = %d, expected %d", config.MaxRetries, DefaultMaxRetries)
	}

	if config.InitialDelay != DefaultInitialDelay {
		t.Errorf("InitialDelay = %v, expected %v", config.InitialDelay, DefaultInitialDelay)
	}

	if config.MaxDelay != DefaultMaxDelay {
		t.Errorf("MaxDelay = %v, expected %v", config.MaxDelay, DefaultMaxDelay)
	}

	if config.BackoffFactor != DefaultBackoffFactor {
		t.Errorf("BackoffFactor = %f, expected %f", config.BackoffFactor, DefaultBackoffFactor)
	}
}

func TestDoWithSyscallErrors(t *testing.T) {
	ctx := context.Background()
	config := Config{
		MaxRetries:    2,
		InitialDelay:  1 * time.Millisecond,
		MaxDelay:      10 * time.Millisecond,
		BackoffFactor: 2.0,
		Logger:        logr.Discard(),
	}

	// Test with transient syscall error
	callCount := 0
	err := Do(ctx, config, "test operation", func() error {
		callCount++
		if callCount < 3 {
			return syscall.EIO // Transient error
		}
		return nil
	})

	if err != nil {
		t.Errorf("Do() returned error for transient syscall error: %v", err)
	}

	if callCount != 3 {
		t.Errorf("Function called %d times, expected 3", callCount)
	}

	// Test with non-transient syscall error
	callCount = 0
	err = Do(ctx, config, "test operation", func() error {
		callCount++
		return syscall.ENOENT // Non-transient error
	})

	if err == nil {
		t.Errorf("Do() should have returned error for non-transient syscall error")
	}

	if callCount != 1 {
		t.Errorf("Function called %d times, expected 1 (no retries)", callCount)
	}
}
