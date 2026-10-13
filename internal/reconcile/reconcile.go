// Package reconcile verifies the ledger against itself: stored balances against
// the postings they should be the sum of, and total debits against total credits.
// It only reads; it never repairs anything.
package reconcile

import (
	"context"
	"time"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/store"
)

// Options controls a reconciliation run.
type Options struct {
	// Deep also verifies, for every posting, that balance_after follows from the
	// previous posting of the same account. It scans every posting.
	Deep bool
	// Now supplies the report timestamp; defaults to time.Now.
	Now func() time.Time
}

// Run reconciles the whole ledger and reports what it found.
//
// All checks run in one REPEATABLE READ, READ ONLY transaction, so they see the
// same snapshot of the database. Transactions that commit while the run is in
// progress are simply not part of it: they cannot cause false mismatches, and
// the run does not block them.
func Run(ctx context.Context, st *store.Store, opts Options) (ledger.ReconciliationReport, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	report := ledger.ReconciliationReport{StartedAt: now().UTC(), Deep: opts.Deep}

	err := st.ReadSnapshot(ctx, func(ctx context.Context, snap *store.Snapshot) error {
		// The function can run again after a retry: start from a clean report.
		report = ledger.ReconciliationReport{StartedAt: report.StartedAt, Deep: opts.Deep}

		var err error
		if report.AccountsChecked, err = snap.CountAccounts(ctx); err != nil {
			return err
		}
		if report.Mismatches, err = snap.BalanceMismatches(ctx); err != nil {
			return err
		}
		if report.Currencies, err = snap.CurrencyTotals(ctx); err != nil {
			return err
		}
		if opts.Deep {
			if report.ChainBreaks, err = snap.BalanceChainBreaks(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ledger.ReconciliationReport{}, err
	}
	return report, nil
}
