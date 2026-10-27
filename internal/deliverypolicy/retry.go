// Package deliverypolicy holds transport-independent bounded retry policy.
package deliverypolicy

import "time"

const MaxAttempts = 8

// RetryDelay applies exponential backoff and honors a provider's minimum delay.
// Clamp provider seconds before conversion to avoid overflowing time.Duration.
func RetryDelay(attempts, retryAfter int) time.Duration {
	return max(time.Second<<min(max(attempts, 0), 8), time.Duration(min(max(retryAfter, 0), 2147483647))*time.Second)
}
