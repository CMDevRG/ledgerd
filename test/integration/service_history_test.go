package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/service"
)

func TestBalance_Current(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	empty, err := e.svc.Balance(ctx, wallet.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Amount.Sign() != 0 || empty.LastPostingAt != nil || empty.AsOf != nil || empty.Currency != "USD" || empty.AccountID != wallet.ID {
		t.Errorf("a new account must have a zero balance and no postings: %+v", empty)
	}

	tx := e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "12345", "USD"))
	got, err := e.svc.Balance(ctx, wallet.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount.String() != "12345" || got.LastPostingAt == nil || !got.LastPostingAt.Equal(tx.CreatedAt) {
		t.Errorf("unexpected balance: %+v", got)
	}
}

func TestBalance_UnknownAccount(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Balance(context.Background(), uuid.New(), nil)
	if !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestBalance_AsOf(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	t0 := e.clock.Now().UTC().Truncate(time.Microsecond)
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD")) // t0:      100
	e.clock.Set(t0.Add(time.Hour))
	e.mustCreate(t, transfer(t, wallet.ID, cash.ID, "30", "USD")) // t0+1h:    70
	e.clock.Set(t0.Add(2 * time.Hour))
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "50", "USD")) // t0+2h:   120
	e.clock.Set(t0.Add(3 * time.Hour))                            // "now"

	tests := []struct {
		name     string
		asOf     time.Time
		want     string
		wantLast *time.Time
	}{
		{"one second before the first posting", t0.Add(-time.Second), "0", nil},
		{"one microsecond before the first posting", t0.Add(-time.Microsecond), "0", nil},
		{"exactly at the first posting (inclusive)", t0, "100", &t0},
		{"between the first and second", t0.Add(30 * time.Minute), "100", &t0},
		{"exactly at the second", t0.Add(time.Hour), "70", ptr(t0.Add(time.Hour))},
		{"between the second and third", t0.Add(90 * time.Minute), "70", ptr(t0.Add(time.Hour))},
		{"exactly at the third", t0.Add(2 * time.Hour), "120", ptr(t0.Add(2 * time.Hour))},
		{"after the last posting but before now", t0.Add(150 * time.Minute), "120", ptr(t0.Add(2 * time.Hour))},
		{"now", t0.Add(3 * time.Hour), "120", ptr(t0.Add(2 * time.Hour))},
		// A fraction of a microsecond past the posting still counts it; a fraction
		// before the next one does not include it.
		{"sub-microsecond precision is ignored", t0.Add(time.Hour).Add(999 * time.Nanosecond), "70", ptr(t0.Add(time.Hour))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			asOf := tc.asOf
			b, err := e.svc.Balance(ctx, wallet.ID, &asOf)
			if err != nil {
				t.Fatal(err)
			}
			if b.Amount.String() != tc.want {
				t.Errorf("balance = %s, want %s", b.Amount, tc.want)
			}
			switch {
			case tc.wantLast == nil && b.LastPostingAt != nil:
				t.Errorf("LastPostingAt = %v, want nil", b.LastPostingAt)
			case tc.wantLast != nil && (b.LastPostingAt == nil || !b.LastPostingAt.Equal(*tc.wantLast)):
				t.Errorf("LastPostingAt = %v, want %v", b.LastPostingAt, *tc.wantLast)
			}
			if b.AsOf == nil || !b.AsOf.Equal(tc.asOf) {
				t.Errorf("AsOf = %v, want the requested instant %v", b.AsOf, tc.asOf)
			}
		})
	}

	t.Run("an instant in the future returns the current balance", func(t *testing.T) {
		future := t0.Add(10 * time.Hour)
		b, err := e.svc.Balance(ctx, wallet.ID, &future)
		if err != nil {
			t.Fatal(err)
		}
		if b.Amount.String() != "120" || b.AsOf != nil {
			t.Errorf("got %+v, want the current balance 120 with no AsOf", b)
		}
	})

	t.Run("an account's own history is independent of the others", func(t *testing.T) {
		asOf := t0.Add(30 * time.Minute)
		b, err := e.svc.Balance(ctx, cash.ID, &asOf)
		if err != nil {
			t.Fatal(err)
		}
		if b.Amount.String() != "100" {
			t.Errorf("cash balance = %s, want 100", b.Amount)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// Inside one transaction an account can have several postings with the same
// timestamp. A historical query must return the final balance of that
// transaction, never an intermediate value.
func TestBalance_AsOfSeesTheFinalBalanceOfATransaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash := e.account(t, "cash", "USD", ledger.Asset, ledger.Debit, false)
	other := e.account(t, "other", "USD", ledger.Asset, ledger.Debit, true)

	tx := e.mustCreate(t, ledger.TransactionRequest{Postings: []ledger.PostingInput{
		in(t, cash.ID, ledger.Credit, "10", "USD"), // cash dips to -10 ...
		in(t, other.ID, ledger.Debit, "10", "USD"),
		in(t, cash.ID, ledger.Debit, "10", "USD"), // ... and ends at 0
		in(t, other.ID, ledger.Credit, "10", "USD"),
	}})
	if tx.Postings[0].BalanceAfter.String() != "-10" {
		t.Fatalf("scenario setup: first posting balance_after = %s", tx.Postings[0].BalanceAfter)
	}

	at := tx.CreatedAt
	b, err := e.svc.Balance(ctx, cash.ID, &at)
	if err != nil {
		t.Fatal(err)
	}
	if b.Amount.String() != "0" {
		t.Errorf("balance at the transaction's timestamp = %s, want the final 0 (not the intermediate -10)", b.Amount)
	}
}

func TestBalance_AsOfStaysCorrectWhenTheClockGoesBackwards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)

	t0 := e.clock.Now().UTC().Truncate(time.Microsecond)
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "100", "USD"))
	e.clock.Set(t0.Add(-time.Hour)) // clock steps back
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "5", "USD"))
	e.clock.Set(t0.Add(time.Hour))

	// Both postings are stamped t0 (the guard), so at t0 the balance is 105.
	b, err := e.svc.Balance(ctx, wallet.ID, &t0)
	if err != nil {
		t.Fatal(err)
	}
	if b.Amount.String() != "105" {
		t.Errorf("balance at t0 = %s, want 105", b.Amount)
	}
	// And a point before t0 sees nothing, which it would not if the second
	// posting had been stamped an hour earlier.
	before := t0.Add(-30 * time.Minute)
	b, err = e.svc.Balance(ctx, wallet.ID, &before)
	if err != nil {
		t.Fatal(err)
	}
	if b.Amount.Sign() != 0 {
		t.Errorf("balance before t0 = %s, want 0", b.Amount)
	}
}

// fill makes n one-unit deposits into the wallet and returns the transactions.
func (e *env) fill(t *testing.T, cash, wallet ledger.Account, n int) []ledger.Transaction {
	t.Helper()
	out := make([]ledger.Transaction, n)
	for i := range out {
		out[i] = e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "1", "USD"))
	}
	return out
}

func TestListPostings_FieldsAndOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	txs := e.fill(t, cash, wallet, 3)

	page, err := e.svc.ListPostings(ctx, wallet.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Postings) != 3 || page.NextCursor != "" {
		t.Fatalf("got %d postings, cursor %q; want 3 and no cursor", len(page.Postings), page.NextCursor)
	}
	for i, p := range page.Postings { // newest first: balances 3, 2, 1
		want := fmt.Sprint(3 - i)
		last := txs[2-i]
		if p.BalanceAfter.String() != want || p.TransactionID != last.ID || p.AccountID != wallet.ID ||
			p.Direction != ledger.Credit || p.Amount.String() != "1" || p.Currency != "USD" {
			t.Errorf("posting %d = %+v", i, p)
		}
	}
	for i := 1; i < len(page.Postings); i++ {
		if page.Postings[i-1].ID <= page.Postings[i].ID {
			t.Error("postings must be ordered newest first")
		}
	}
}

func TestListPostings_EmptyAndUnknownAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, wallet := e.cashAndWallet(t)

	page, err := e.svc.ListPostings(ctx, wallet.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Postings == nil || len(page.Postings) != 0 || page.NextCursor != "" {
		t.Errorf("an account without postings gives an empty (non-nil) page: %+v", page)
	}

	if _, err := e.svc.ListPostings(ctx, uuid.New(), "", 0); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("unknown account: want not found, got %v", err)
	}
}

func TestListPostings_PaginatesWithoutGapsOrRepeats(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	e.fill(t, cash, wallet, 120)

	var seen []int64
	cursor, pages := "", 0
	for {
		page, err := e.svc.ListPostings(ctx, wallet.ID, cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, p := range page.Postings {
			seen = append(seen, p.ID)
		}
		if page.NextCursor == "" {
			break
		}
		if len(page.Postings) != 50 {
			t.Fatalf("a page with a next cursor must be full, got %d", len(page.Postings))
		}
		cursor = page.NextCursor
	}

	if pages != 3 || len(seen) != 120 {
		t.Fatalf("pages=%d postings=%d, want 3 pages and 120 postings", pages, len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] <= seen[i] {
			t.Fatalf("ids not strictly decreasing at %d: %d then %d (gap-free means no repeats and no reordering)", i, seen[i-1], seen[i])
		}
	}
}

func TestListPostings_ExactlyOnePageHasNoCursor(t *testing.T) {
	e := newEnv(t)
	cash, wallet := e.cashAndWallet(t)
	e.fill(t, cash, wallet, 5)

	page, err := e.svc.ListPostings(context.Background(), wallet.ID, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Postings) != 5 || page.NextCursor != "" {
		t.Errorf("exactly one full page must not advertise a next page: %d postings, cursor %q", len(page.Postings), page.NextCursor)
	}
	page, err = e.svc.ListPostings(context.Background(), wallet.ID, "", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Postings) != 4 || page.NextCursor == "" {
		t.Errorf("limit 4 of 5 must return 4 and a cursor: %d, %q", len(page.Postings), page.NextCursor)
	}
}

func TestListPostings_DefaultAndMaximumPageSize(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	e.fill(t, cash, wallet, service.DefaultPageSize+5)

	page, err := e.svc.ListPostings(ctx, wallet.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Postings) != service.DefaultPageSize || page.NextCursor == "" {
		t.Errorf("default page: %d postings, cursor %q; want %d and a cursor", len(page.Postings), page.NextCursor, service.DefaultPageSize)
	}

	if _, err := e.svc.ListPostings(ctx, wallet.ID, "", service.MaxPageSize); err != nil {
		t.Errorf("the maximum page size must be accepted: %v", err)
	}
}

func TestListPostings_Validation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, wallet := e.cashAndWallet(t)

	for name, tc := range map[string]struct {
		cursor string
		limit  int
		field  string
	}{
		"limit above the maximum": {"", service.MaxPageSize + 1, "limit"},
		"negative limit":          {"", -1, "limit"},
		"garbage cursor":          {"definitely-not-a-cursor", 10, "cursor"},
		"both wrong":              {"%%%", 9999, "limit"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.ListPostings(ctx, wallet.ID, tc.cursor, tc.limit)
			var ve *ledger.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %v", err)
			}
			found := false
			for _, v := range ve.Violations {
				found = found || v.Field == tc.field
			}
			if !found {
				t.Errorf("no violation on %q: %v", tc.field, err)
			}
		})
	}
}

// New postings are added between two page requests. Because pages are keyed on
// the posting id, the client still receives exactly the postings that existed
// when it started, with nothing skipped and nothing repeated.
func TestListPostings_StableWhileNewPostingsArrive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cash, wallet := e.cashAndWallet(t)
	e.fill(t, cash, wallet, 30)

	first, err := e.svc.ListPostings(ctx, wallet.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}

	e.fill(t, cash, wallet, 7) // arrives while the client is paging

	var rest []int64
	cursor := first.NextCursor
	for cursor != "" {
		page, err := e.svc.ListPostings(ctx, wallet.ID, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Postings {
			rest = append(rest, p.ID)
		}
		cursor = page.NextCursor
	}

	if len(rest) != 20 {
		t.Fatalf("remaining pages returned %d postings, want the 20 that were older than the first page", len(rest))
	}
	if first.Postings[len(first.Postings)-1].ID <= rest[0] {
		t.Error("the second page must continue strictly below the first page")
	}
	for i := 1; i < len(rest); i++ {
		if rest[i-1] <= rest[i] {
			t.Fatalf("repeat or reorder at %d", i)
		}
	}

	// A fresh read now starts with the 7 new postings.
	fresh, err := e.svc.ListPostings(ctx, wallet.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Postings[0].BalanceAfter.String() != "37" {
		t.Errorf("newest posting balance_after = %s, want 37", fresh.Postings[0].BalanceAfter)
	}
}
