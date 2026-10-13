package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/radadev/ledgerd/internal/ledger"
)

// newTestStore returns a Store that never touches a database and records the
// delays it was asked to sleep instead of sleeping.
func newTestStore(cfg Config) (*Store, *[]time.Duration) {
	var slept []time.Duration
	s := &Store{cfg: cfg.withDefaults()}
	s.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return s, &slept
}

func pgErr(code string) error { return &pgconn.PgError{Code: code, Message: "synthetic " + code} }

func TestConfig_Defaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.LockTimeout != DefaultLockTimeout || c.MaxAttempts != DefaultMaxAttempts || c.BackoffBase != DefaultBackoffBase {
		t.Errorf("unexpected defaults: %+v", c)
	}
	custom := Config{LockTimeout: time.Second, MaxAttempts: 2, BackoffBase: time.Millisecond}.withDefaults()
	if custom.LockTimeout != time.Second || custom.MaxAttempts != 2 || custom.BackoffBase != time.Millisecond {
		t.Errorf("custom values were overwritten: %+v", custom)
	}
}

func TestRetrying_SuccessFirstTry(t *testing.T) {
	s, slept := newTestStore(Config{})
	calls := 0
	if err := s.retrying(context.Background(), func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(*slept) != 0 {
		t.Errorf("calls=%d sleeps=%d, want 1 and 0", calls, len(*slept))
	}
}

func TestRetrying_RetriesRetryableErrorsThenSucceeds(t *testing.T) {
	for _, code := range []string{sqlStateSerializationFailure, sqlStateDeadlockDetected} {
		t.Run(code, func(t *testing.T) {
			s, slept := newTestStore(Config{MaxAttempts: 4, BackoffBase: 10 * time.Millisecond})
			calls := 0
			err := s.retrying(context.Background(), func() error {
				calls++
				if calls < 3 {
					return pgErr(code)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("expected success after retries, got %v", err)
			}
			if calls != 3 || len(*slept) != 2 {
				t.Errorf("calls=%d sleeps=%d, want 3 and 2", calls, len(*slept))
			}
		})
	}
}

func TestRetrying_GivesUpAfterMaxAttempts(t *testing.T) {
	s, slept := newTestStore(Config{MaxAttempts: 3})
	calls := 0
	err := s.retrying(context.Background(), func() error { calls++; return pgErr(sqlStateDeadlockDetected) })

	if !errors.Is(err, ledger.ErrTemporarilyUnavailable) {
		t.Fatalf("want ErrTemporarilyUnavailable, got %v", err)
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != sqlStateDeadlockDetected {
		t.Errorf("the original database error should stay reachable, got %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want exactly MaxAttempts (3)", calls)
	}
	if len(*slept) != 2 {
		t.Errorf("sleeps = %d, want 2 (no sleep after the last attempt)", len(*slept))
	}
}

func TestRetrying_DoesNotRetryOtherErrors(t *testing.T) {
	plain := errors.New("boom")
	for name, in := range map[string]error{
		"plain error":           plain,
		"unique violation":      pgErr(sqlStateUniqueViolation),
		"check violation":       pgErr("23514"),
		"insufficient funds":    &ledger.InsufficientFundsError{},
		"connection lost":       fmt.Errorf("commit: %w", errors.New("conn closed")),
		"context deadline":      context.DeadlineExceeded,
		"unbalanced from check": &ledger.UnbalancedError{},
	} {
		t.Run(name, func(t *testing.T) {
			s, slept := newTestStore(Config{})
			calls := 0
			err := s.retrying(context.Background(), func() error { calls++; return in })
			if calls != 1 || len(*slept) != 0 {
				t.Errorf("calls=%d sleeps=%d, want 1 and 0", calls, len(*slept))
			}
			if !errors.Is(err, in) {
				t.Errorf("error should be returned unchanged, got %v", err)
			}
			if errors.Is(err, ledger.ErrTemporarilyUnavailable) {
				t.Error("must not be reported as temporarily unavailable")
			}
		})
	}
}

func TestRetrying_LockTimeoutIsNotRetried(t *testing.T) {
	s, slept := newTestStore(Config{})
	calls := 0
	err := s.retrying(context.Background(), func() error { calls++; return pgErr(sqlStateLockNotAvailable) })
	if !errors.Is(err, ledger.ErrTemporarilyUnavailable) {
		t.Fatalf("want ErrTemporarilyUnavailable, got %v", err)
	}
	if calls != 1 || len(*slept) != 0 {
		t.Errorf("a lock timeout must be reported at once: calls=%d sleeps=%d", calls, len(*slept))
	}
}

func TestRetrying_RecognisesWrappedDatabaseErrors(t *testing.T) {
	s, _ := newTestStore(Config{MaxAttempts: 2})
	calls := 0
	err := s.retrying(context.Background(), func() error {
		calls++
		return fmt.Errorf("commit: %w", fmt.Errorf("outer: %w", pgErr(sqlStateSerializationFailure)))
	})
	if calls != 2 || !errors.Is(err, ledger.ErrTemporarilyUnavailable) {
		t.Errorf("wrapped 40001 must be retried: calls=%d err=%v", calls, err)
	}
}

func TestRetrying_StopsWhenContextIsCancelledDuringBackoff(t *testing.T) {
	s := &Store{cfg: Config{MaxAttempts: 5}.withDefaults(), sleep: sleepContext}
	s.cfg.BackoffBase = time.Hour // would block forever if the context were ignored

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- s.retrying(ctx, func() error { calls++; return pgErr(sqlStateDeadlockDetected) })
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retrying did not return after the context was cancelled")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestBackoff_GrowsExponentiallyWithBoundedJitter(t *testing.T) {
	s, _ := newTestStore(Config{BackoffBase: 10 * time.Millisecond})
	for attempt := 1; attempt <= 4; attempt++ {
		base := (10 * time.Millisecond) << (attempt - 1)
		for i := 0; i < 200; i++ {
			d := s.backoff(attempt)
			if d < base || d > base+base/2 {
				t.Fatalf("attempt %d: backoff %v outside [%v, %v]", attempt, d, base, base+base/2)
			}
		}
	}
}

func TestBackoff_IsCapped(t *testing.T) {
	s, _ := newTestStore(Config{BackoffBase: 10 * time.Millisecond})
	for _, attempt := range []int{10, 30, 62, 63, 64, 200} {
		if d := s.backoff(attempt); d < maxBackoff || d > maxBackoff+maxBackoff/2 {
			t.Errorf("attempt %d: backoff %v outside the cap [%v, %v]", attempt, d, maxBackoff, maxBackoff+maxBackoff/2)
		}
	}
}
