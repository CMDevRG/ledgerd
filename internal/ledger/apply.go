package ledger

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"

	"github.com/google/uuid"
)

// AppliedPosting is a posting of a request together with the balance its
// account had right after that posting.
type AppliedPosting struct {
	PostingInput
	BalanceAfter *big.Int
}

// Application is the outcome of applying a transaction to a set of accounts.
type Application struct {
	// Postings are in request order. When an account has several postings in
	// one transaction, only its last posting's BalanceAfter is a committed
	// state; earlier ones are intermediate values.
	Postings []AppliedPosting
	// NewBalances holds the final balance of every touched account.
	NewBalances map[uuid.UUID]*big.Int
}

// Apply computes the effect of a transaction on the given accounts.
//
// It is a pure function: it reads the accounts (typically locked rows loaded
// from the database) and never mutates them, so a rejected transaction leaves
// no trace by construction. The accounts map must contain every account that
// the request references.
//
// Rules, in order:
//  1. The request must be valid and balanced (see TransactionRequest.Validate).
//  2. Every referenced account must exist.
//  3. Every posting's currency must equal its account's currency.
//  4. A posting changes its account's balance by +amount when its direction is
//     the account's normal balance side, and by -amount otherwise. Postings are
//     applied in request order.
//  5. After all postings, every account with AllowNegative == false must have a
//     balance >= 0. The check uses the FINAL balance, so an intermediate dip
//     below zero inside one transaction is not an overdraft. If any account
//     fails, the whole transaction is rejected.
func Apply(accounts map[uuid.UUID]Account, req TransactionRequest) (Application, error) {
	if err := req.Validate(); err != nil {
		return Application{}, err
	}

	running := make(map[uuid.UUID]*big.Int, len(req.Postings))
	applied := make([]AppliedPosting, 0, len(req.Postings))

	for i, p := range req.Postings {
		acct, ok := accounts[p.AccountID]
		if !ok {
			return Application{}, &AccountNotFoundError{ID: p.AccountID}
		}
		if p.Currency != acct.Currency {
			var vs violations
			vs.add(fmt.Sprintf("postings[%d].currency", i),
				"posting currency %s does not match account currency %s", p.Currency, acct.Currency)
			return Application{}, vs.err()
		}

		bal, seen := running[p.AccountID]
		if !seen {
			bal = new(big.Int)
			if acct.Balance != nil {
				bal.Set(acct.Balance)
			}
			running[p.AccountID] = bal
		}

		if p.Direction == acct.NormalBalance {
			bal.Add(bal, p.Amount.Big())
		} else {
			bal.Sub(bal, p.Amount.Big())
		}
		applied = append(applied, AppliedPosting{PostingInput: p, BalanceAfter: new(big.Int).Set(bal)})
	}

	// Check accounts in a fixed order so that, when several are overdrawn, the
	// reported one does not depend on map iteration order.
	ids := make([]uuid.UUID, 0, len(running))
	for id := range running {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })

	for _, id := range ids {
		if accounts[id].AllowNegative || running[id].Sign() >= 0 {
			continue
		}
		return Application{}, &InsufficientFundsError{
			AccountID: id,
			Balance:   running[id].String(),
			Shortfall: new(big.Int).Neg(running[id]).String(),
		}
	}

	return Application{Postings: applied, NewBalances: running}, nil
}
