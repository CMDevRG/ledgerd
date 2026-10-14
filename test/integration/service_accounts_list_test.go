package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CMDevRG/ledgerd/internal/ledger"
	"github.com/CMDevRG/ledgerd/internal/service"
)

// listAll follows next cursors and returns every account in the order served.
func (e *env) listAll(t *testing.T, p service.ListAccountsParams) []ledger.Account {
	t.Helper()
	var all []ledger.Account
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatal("pagination did not terminate")
		}
		page, err := e.svc.ListAccounts(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Accounts...)
		if page.NextCursor == "" {
			return all
		}
		p.Cursor = page.NextCursor
	}
}

func (e *env) makeAccounts(t *testing.T, prefix, currency string, n int) []ledger.Account {
	t.Helper()
	out := make([]ledger.Account, n)
	for i := range out {
		out[i] = e.account(t, fmt.Sprintf("%s%03d", prefix, i), currency, ledger.Asset, ledger.Debit, true)
	}
	return out
}

func TestListAccounts_NewestFirstWithTheirBalances(t *testing.T) {
	e := newEnv(t)
	cash, wallet := e.cashAndWallet(t)
	e.mustCreate(t, transfer(t, cash.ID, wallet.ID, "250", "USD"))

	page, err := e.svc.ListAccounts(context.Background(), service.ListAccountsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Accounts) != 2 || page.NextCursor != "" {
		t.Fatalf("got %d accounts, cursor %q", len(page.Accounts), page.NextCursor)
	}
	if page.Accounts[0].ID != wallet.ID || page.Accounts[1].ID != cash.ID {
		t.Errorf("the account created last must come first: %s then %s", page.Accounts[0].Code, page.Accounts[1].Code)
	}
	for _, a := range page.Accounts {
		if a.Balance.String() != "250" {
			t.Errorf("%s: balance %s, want the current balance 250", a.Code, a.Balance)
		}
	}
}

func TestListAccounts_AccountsWithTheSameTimestampAreOrderedById(t *testing.T) {
	e := newEnv(t)
	// Force identical creation times: the tie must be broken by id, descending.
	for i := 0; i < 5; i++ {
		e.exec(t, `INSERT INTO accounts (code, currency, type, normal_balance, created_at)
			VALUES ($1, 'USD', 'ASSET', 'DEBIT', '2026-01-01T00:00:00Z')`, fmt.Sprintf("tie-%d", i))
	}
	// A page size of 2 forces the cursor to land in the middle of the tie.
	got := e.listAll(t, service.ListAccountsParams{Limit: 2})
	if len(got) != 5 {
		t.Fatalf("got %d accounts, want 5", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID.String() <= got[i].ID.String() {
			t.Errorf("ids not strictly descending within equal timestamps: %s then %s", got[i-1].ID, got[i].ID)
		}
	}
	seen := map[uuid.UUID]bool{}
	for _, a := range got {
		if seen[a.ID] {
			t.Errorf("account %s returned twice", a.ID)
		}
		seen[a.ID] = true
	}
}

func TestListAccounts_PaginatesWithoutGapsOrRepeats(t *testing.T) {
	e := newEnv(t)
	created := e.makeAccounts(t, "acct-", "USD", 120)

	var pages, total int
	seen := map[uuid.UUID]bool{}
	p := service.ListAccountsParams{Limit: 50}
	for {
		page, err := e.svc.ListAccounts(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, a := range page.Accounts {
			if seen[a.ID] {
				t.Fatalf("account %s returned twice", a.Code)
			}
			seen[a.ID] = true
			total++
		}
		if page.NextCursor == "" {
			break
		}
		if len(page.Accounts) != 50 {
			t.Fatalf("a page with a next cursor must be full, got %d", len(page.Accounts))
		}
		p.Cursor = page.NextCursor
	}
	if pages != 3 || total != 120 {
		t.Fatalf("pages=%d accounts=%d, want 3 and 120", pages, total)
	}
	for _, a := range created {
		if !seen[a.ID] {
			t.Errorf("account %s was never returned", a.Code)
		}
	}
}

func TestListAccounts_ExactlyOnePageHasNoCursor(t *testing.T) {
	e := newEnv(t)
	e.makeAccounts(t, "a", "USD", 5)
	page, err := e.svc.ListAccounts(context.Background(), service.ListAccountsParams{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Accounts) != 5 || page.NextCursor != "" {
		t.Errorf("exactly one full page must not advertise another: %d accounts, cursor %q", len(page.Accounts), page.NextCursor)
	}
	page, err = e.svc.ListAccounts(context.Background(), service.ListAccountsParams{Limit: 4})
	if err != nil || len(page.Accounts) != 4 || page.NextCursor == "" {
		t.Errorf("limit 4 of 5 must return 4 and a cursor: %d, %q, %v", len(page.Accounts), page.NextCursor, err)
	}
}

// Accounts created while a client pages must not make it skip or repeat any that
// existed when it started.
func TestListAccounts_StableWhileAccountsAreCreated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	original := e.makeAccounts(t, "old-", "USD", 30)

	first, err := e.svc.ListAccounts(ctx, service.ListAccountsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	e.makeAccounts(t, "new-", "USD", 7) // created while the client is paging

	seen := map[uuid.UUID]int{}
	for _, a := range first.Accounts {
		seen[a.ID]++
	}
	p := service.ListAccountsParams{Limit: 10, Cursor: first.NextCursor}
	for _, a := range e.listAll(t, p) {
		seen[a.ID]++
	}
	for _, a := range original {
		if seen[a.ID] != 1 {
			t.Errorf("%s was returned %d times, want exactly once", a.Code, seen[a.ID])
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("account %s returned %d times", id, n)
		}
	}
	// New accounts are newer than the cursor, so this paging session does not reach them; a fresh one starts with them.
	fresh, err := e.svc.ListAccounts(ctx, service.ListAccountsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fresh.Accounts[0].Code, "new-") {
		t.Errorf("a new listing must start with the newest account, got %s", fresh.Accounts[0].Code)
	}
}

func TestListAccounts_Filters(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, code := range []string{"user_1", "userx2", "user.3", "lab-a", "lab-b"} {
		e.account(t, code, "USD", ledger.Asset, ledger.Debit, true)
	}
	e.account(t, "user_eth", "ETH", ledger.Asset, ledger.Debit, true)

	codes := func(p service.ListAccountsParams) []string {
		var out []string
		for _, a := range e.listAll(t, p) {
			out = append(out, a.Code)
		}
		return out
	}

	t.Run("a prefix is a plain prefix: '_' is not a wildcard", func(t *testing.T) {
		got := codes(service.ListAccountsParams{CodePrefix: ptr("user_")})
		if len(got) != 2 || got[0] != "user_eth" || got[1] != "user_1" {
			t.Errorf("prefix user_ matched %v; LIKE would also have matched userx2 and user.3", got)
		}
	})
	t.Run("prefix and currency combine with AND", func(t *testing.T) {
		got := codes(service.ListAccountsParams{CodePrefix: ptr("user"), Currency: ptr("ETH")})
		if len(got) != 1 || got[0] != "user_eth" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("currency alone", func(t *testing.T) {
		if got := codes(service.ListAccountsParams{Currency: ptr("ETH")}); len(got) != 1 {
			t.Errorf("got %v", got)
		}
	})
	t.Run("the whole prefix must match, case-sensitively", func(t *testing.T) {
		if got := codes(service.ListAccountsParams{CodePrefix: ptr("LAB")}); len(got) != 0 {
			t.Errorf("LAB must not match lab-: %v", got)
		}
		if got := codes(service.ListAccountsParams{CodePrefix: ptr("lab-")}); len(got) != 2 {
			t.Errorf("lab- should match two accounts: %v", got)
		}
	})
	t.Run("no match gives an empty list and no cursor", func(t *testing.T) {
		page, err := e.svc.ListAccounts(ctx, service.ListAccountsParams{CodePrefix: ptr("nothing-")})
		if err != nil {
			t.Fatal(err)
		}
		if page.Accounts == nil || len(page.Accounts) != 0 || page.NextCursor != "" {
			t.Errorf("got %+v", page)
		}
	})
	t.Run("filters keep working across pages", func(t *testing.T) {
		got := codes(service.ListAccountsParams{CodePrefix: ptr("user"), Limit: 1})
		if len(got) != 4 {
			t.Errorf("paging one at a time through 4 matches gave %v", got)
		}
	})
}

func TestListAccounts_Validation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tests := map[string]struct {
		p     service.ListAccountsParams
		field string
	}{
		"limit above the maximum": {service.ListAccountsParams{Limit: service.MaxPageSize + 1}, "limit"},
		"negative limit":          {service.ListAccountsParams{Limit: -1}, "limit"},
		"garbage cursor":          {service.ListAccountsParams{Cursor: "garbage"}, "cursor"},
		"empty prefix":            {service.ListAccountsParams{CodePrefix: ptr("")}, "code_prefix"},
		"prefix with a wildcard":  {service.ListAccountsParams{CodePrefix: ptr("a%")}, "code_prefix"},
		"prefix starting with -":  {service.ListAccountsParams{CodePrefix: ptr("-a")}, "code_prefix"},
		"prefix too long":         {service.ListAccountsParams{CodePrefix: ptr(strings.Repeat("a", 65))}, "code_prefix"},
		"lower-case currency":     {service.ListAccountsParams{Currency: ptr("usd")}, "currency"},
		"empty currency":          {service.ListAccountsParams{Currency: ptr("")}, "currency"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.ListAccounts(ctx, tc.p)
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
	t.Run("the largest page size is accepted", func(t *testing.T) {
		if _, err := e.svc.ListAccounts(ctx, service.ListAccountsParams{Limit: service.MaxPageSize}); err != nil {
			t.Error(err)
		}
	})
}

// The listing is served by the index on (created_at DESC, id DESC): PostgreSQL
// reads the first rows in order instead of sorting the table.
func TestListAccounts_UsesTheCreatedAtIndex(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.exec(t, `INSERT INTO accounts (code, currency, type, normal_balance, created_at)
		SELECT 'bulk-' || g, 'USD', 'ASSET', 'DEBIT', '2026-01-01T00:00:00Z'::timestamptz + g * interval '1 second'
		FROM generate_series(1, 5000) AS g`)
	e.exec(t, `ANALYZE accounts`)

	rows, err := e.db.Super.Query(ctx, `EXPLAIN (FORMAT TEXT)
		SELECT * FROM accounts
		WHERE (NULL::text IS NULL OR starts_with(code, NULL::text))
		  AND (NULL::text IS NULL OR currency = NULL::text)
		  AND (NULL::timestamptz IS NULL OR (created_at, id) < (NULL::timestamptz, NULL::uuid))
		ORDER BY created_at DESC, id DESC LIMIT 51`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "accounts_created_idx") {
		t.Errorf("the plan does not use accounts_created_idx:\n%s", plan)
	}
	if strings.Contains(plan, "Sort") {
		t.Errorf("the plan sorts instead of reading in index order:\n%s", plan)
	}
}

func newUSDAccount(code string) ledger.NewAccount {
	return ledger.NewAccount{Code: code, Currency: "USD", Type: ledger.Asset, NormalBalance: ledger.Debit, AllowNegative: true}
}
