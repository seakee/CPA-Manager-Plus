package main

import (
	"context"
	"errors"
	"testing"
)

func TestRetryIdentityForwardCompletionsAttemptsBothAfterFirstFailure(t *testing.T) {
	apiKeyFailure := errors.New("API-key marker unavailable")
	called := 0
	err := retryIdentityForwardCompletions(context.Background(),
		func(context.Context) error {
			called++
			return apiKeyFailure
		},
		func(context.Context) error {
			called++
			return nil
		})
	if called != 2 || !errors.Is(err, apiKeyFailure) {
		t.Fatalf("called=%d err=%v", called, err)
	}
}
