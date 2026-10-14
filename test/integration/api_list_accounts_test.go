package integration

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CMDevRG/ledgerd/internal/service"
)

func TestAPI_ListAccounts(t *testing.T) {
	a := newAPIEnv(t)

	for i := 0; i < 5; i++ {
		a.createAccount(t, fmt.Sprintf("list_%d", i), "USD", "ASSET", "DEBIT", false)
	}
	eth := a.createAccount(t, "list_eth", "ETH", "ASSET", "DEBIT", false)
	a.createAccount(t, "other-1", "USD", "ASSET", "DEBIT", false)

	// Page through everything, two at a time, newest first.
	var seen []string
	path := "/v1/accounts?limit=2"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		page := a.mustStatus(t, a.do(t, "GET", path, ""), 200).json(t)
		for _, acct := range page["accounts"].([]any) {
			seen = append(seen, acct.(map[string]any)["code"].(string))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/v1/accounts?limit=2&cursor=" + url.QueryEscape(next)
	}
	want := []string{"other-1", "list_eth", "list_4", "list_3", "list_2", "list_1", "list_0"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want newest first: %v", seen, want)
	}

	t.Run("filters", func(t *testing.T) {
		page := a.mustStatus(t, a.do(t, "GET", "/v1/accounts?code_prefix=list_&currency=ETH", ""), 200).json(t)
		accounts := page["accounts"].([]any)
		if len(accounts) != 1 || accounts[0].(map[string]any)["id"] != eth || page["next_cursor"] != nil {
			t.Errorf("got %v", page)
		}
		none := a.mustStatus(t, a.do(t, "GET", "/v1/accounts?code_prefix=nothing", ""), 200).json(t)
		if len(none["accounts"].([]any)) != 0 || none["next_cursor"] != nil {
			t.Errorf("no match: %v", none)
		}
	})

	t.Run("a balance appears in the list", func(t *testing.T) {
		cash := a.createAccount(t, "bal-cash", "USD", "ASSET", "DEBIT", false)
		wallet := a.createAccount(t, "bal-wallet", "USD", "LIABILITY", "CREDIT", false)
		a.mustStatus(t, a.do(t, "POST", "/v1/transactions", txBody(cash, wallet, "777", "USD"), "Idempotency-Key", "k1"), 201)
		page := a.mustStatus(t, a.do(t, "GET", "/v1/accounts?code_prefix=bal-wallet", ""), 200).json(t)
		if got := page["accounts"].([]any)[0].(map[string]any)["balance"]; got != "777" {
			t.Errorf("balance in the list = %v, want 777", got)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for query, want := range map[string]struct {
			status int
			code   string
		}{
			"limit=0":           {422, "validation_error"},
			"limit=201":         {422, "validation_error"},
			"cursor=bogus":      {422, "validation_error"},
			"cursor=":           {422, "validation_error"},
			"code_prefix=":      {422, "validation_error"},
			"code_prefix=a%25b": {422, "validation_error"},
			"currency=usd":      {422, "validation_error"},
			"sort=code":         {400, "malformed_request"},
			"limit=1&limit=2":   {400, "malformed_request"},
		} {
			r := a.do(t, "GET", "/v1/accounts?"+query, "")
			if r.Status != want.status || r.json(t)["code"] != want.code {
				t.Errorf("?%s: %d %s, want %d %s", query, r.Status, r.Body, want.status, want.code)
			}
		}
	})

	t.Run("requires the API key", func(t *testing.T) {
		a.mustStatus(t, a.do(t, "GET", "/v1/accounts", "", "Authorization", ""), 401)
	})
}

// A client pages through the accounts while others keep creating new ones. Every
// account that existed when paging began must be seen exactly once.
func TestListAccounts_PagingWhileAccountsAreBeingCreated(t *testing.T) {
	e := newEnvWithConns(t, 20)
	ctx := context.Background()
	original := e.makeAccounts(t, "orig-", "USD", 150)

	var stop atomic.Bool
	var wg sync.WaitGroup
	var created atomic.Int64
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				code := fmt.Sprintf("late-%d-%d", w, i)
				if _, err := e.svc.CreateAccount(ctx, newUSDAccount(code)); err != nil {
					t.Errorf("create %s: %v", code, err)
					return
				}
				created.Add(1)
			}
		}()
	}

	seen := map[string]int{}
	p := service.ListAccountsParams{Limit: 10}
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatal("pagination did not terminate")
		}
		page, err := e.svc.ListAccounts(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, acct := range page.Accounts {
			seen[acct.Code]++
		}
		if page.NextCursor == "" {
			break
		}
		p.Cursor = page.NextCursor
	}
	stop.Store(true)
	wg.Wait()

	for code, n := range seen {
		if n != 1 {
			t.Errorf("account %s was returned %d times", code, n)
		}
	}
	// This client started paging at the top, so it cannot have missed any original account.
	for _, acct := range original {
		if seen[acct.Code] != 1 {
			t.Errorf("original account %s seen %d times, want 1", acct.Code, seen[acct.Code])
		}
	}
	t.Logf("%d accounts were created while paging through %d", created.Load(), len(seen))
}
