package api

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"

	"github.com/radadev/ledgerd/internal/ledger"
	"github.com/radadev/ledgerd/internal/service"
)

// ---- requests ----

type accountRequest struct {
	Code          string `json:"code"`
	Currency      string `json:"currency"`
	Type          string `json:"type"`
	NormalBalance string `json:"normal_balance"`
	AllowNegative bool   `json:"allow_negative"`
}

func (a accountRequest) toDomain() ledger.NewAccount {
	return ledger.NewAccount{
		Code:          a.Code,
		Currency:      ledger.Currency(a.Currency),
		Type:          ledger.AccountType(a.Type),
		NormalBalance: ledger.Side(a.NormalBalance),
		AllowNegative: a.AllowNegative,
	}
}

type transactionRequest struct {
	Description string           `json:"description"`
	Postings    []postingRequest `json:"postings"`
}

type postingRequest struct {
	AccountID string `json:"account_id"`
	Direction string `json:"direction"`
	// Amount is kept raw so that a JSON number (which clients may have already
	// rounded) can be told apart from the required JSON string.
	Amount   json.RawMessage `json:"amount"`
	Currency string          `json:"currency"`
}

// canonicalTransaction is what gets hashed for idempotency: the request after
// parsing and normalisation, in a fixed field order, so that key order,
// whitespace, UUID letter case and so on in the client's JSON do not matter.
type canonicalTransaction struct {
	Description string             `json:"description"`
	Postings    []canonicalPosting `json:"postings"`
}

type canonicalPosting struct {
	AccountID string `json:"account_id"`
	Direction string `json:"direction"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
}

// toDomain converts the request into a domain request and its canonical form.
// Problems with the shape of a field (not a UUID, amount not a string of
// digits) are reported with the same field paths as domain validation. The
// domain rules themselves (limits, balance, ...) are checked later by the service.
func (t transactionRequest) toDomain() (ledger.TransactionRequest, []byte, error) {
	var vs []ledger.Violation
	req := ledger.TransactionRequest{Description: t.Description, Postings: make([]ledger.PostingInput, len(t.Postings))}
	canon := canonicalTransaction{Description: t.Description, Postings: make([]canonicalPosting, len(t.Postings))}

	for i, p := range t.Postings {
		field := func(name string) string { return fmt.Sprintf("postings[%d].%s", i, name) }

		var accountID uuid.UUID
		if p.AccountID == "" {
			vs = append(vs, ledger.Violation{Field: field("account_id"), Message: "is required"})
		} else if id, ok := parseUUID(p.AccountID); !ok {
			vs = append(vs, ledger.Violation{Field: field("account_id"), Message: "must be a UUID"})
		} else {
			accountID = id
		}

		amount, err := parseAmountField(p.Amount)
		if err != nil {
			vs = append(vs, ledger.Violation{Field: field("amount"), Message: err.Error()})
		}

		req.Postings[i] = ledger.PostingInput{
			AccountID: accountID,
			Direction: ledger.Side(p.Direction),
			Amount:    amount,
			Currency:  ledger.Currency(p.Currency),
		}
		canon.Postings[i] = canonicalPosting{
			AccountID: accountID.String(), Direction: p.Direction, Amount: amount.String(), Currency: p.Currency,
		}
	}
	if len(vs) > 0 {
		return ledger.TransactionRequest{}, nil, &ledger.ValidationError{Violations: vs}
	}
	body, err := json.Marshal(canon)
	if err != nil {
		return ledger.TransactionRequest{}, nil, fmt.Errorf("encode canonical request: %w", err)
	}
	return req, body, nil
}

// parseAmountField accepts only a JSON string holding a canonical amount.
func parseAmountField(raw json.RawMessage) (ledger.Amount, error) {
	if len(raw) == 0 {
		return ledger.Amount{}, fmt.Errorf("is required")
	}
	if raw[0] != '"' {
		return ledger.Amount{}, fmt.Errorf("must be a JSON string of decimal digits (for example \"10000\"), not a JSON number or other type")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ledger.Amount{}, fmt.Errorf("must be a JSON string of decimal digits")
	}
	a, err := ledger.ParseAmount(s)
	if err != nil {
		return ledger.Amount{}, err
	}
	return a, nil
}

// parseUUID accepts only the canonical 36-character form; the library also
// accepts braces, URNs and hyphen-less forms, which would give one resource
// several spellings.
func parseUUID(s string) (uuid.UUID, bool) {
	if len(s) != 36 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// ---- responses ----

// timestamp formats an instant as RFC 3339 in UTC with fixed microsecond
// precision (the resolution of the database), so values sort as strings.
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

func timestampPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := timestamp(*t)
	return &s
}

func bigString(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

type accountResponse struct {
	ID            string  `json:"id"`
	Code          string  `json:"code"`
	Currency      string  `json:"currency"`
	Type          string  `json:"type"`
	NormalBalance string  `json:"normal_balance"`
	AllowNegative bool    `json:"allow_negative"`
	Balance       string  `json:"balance"`
	LastPostingAt *string `json:"last_posting_at"`
	CreatedAt     string  `json:"created_at"`
}

func accountDTO(a ledger.Account) accountResponse {
	return accountResponse{
		ID: a.ID.String(), Code: a.Code, Currency: string(a.Currency), Type: string(a.Type),
		NormalBalance: string(a.NormalBalance), AllowNegative: a.AllowNegative, Balance: bigString(a.Balance),
		LastPostingAt: timestampPtr(a.LastPostingAt), CreatedAt: timestamp(a.CreatedAt),
	}
}

type postingResponse struct {
	ID            int64  `json:"id"`
	TransactionID string `json:"transaction_id"`
	AccountID     string `json:"account_id"`
	Direction     string `json:"direction"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	BalanceAfter  string `json:"balance_after"`
	RecordedAt    string `json:"recorded_at"`
}

func postingDTO(p ledger.Posting) postingResponse {
	return postingResponse{
		ID: p.ID, TransactionID: p.TransactionID.String(), AccountID: p.AccountID.String(),
		Direction: string(p.Direction), Amount: p.Amount.String(), Currency: string(p.Currency),
		BalanceAfter: bigString(p.BalanceAfter), RecordedAt: timestamp(p.RecordedAt),
	}
}

type transactionResponse struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	ReversesID  *string           `json:"reverses_id"`
	CreatedAt   string            `json:"created_at"`
	Postings    []postingResponse `json:"postings"`
}

func transactionDTO(t ledger.Transaction) transactionResponse {
	out := transactionResponse{
		ID: t.ID.String(), Description: t.Description, CreatedAt: timestamp(t.CreatedAt),
		Postings: make([]postingResponse, len(t.Postings)),
	}
	if t.ReversesID != nil {
		s := t.ReversesID.String()
		out.ReversesID = &s
	}
	for i, p := range t.Postings {
		out.Postings[i] = postingDTO(p)
	}
	return out
}

type balanceResponse struct {
	AccountID     string  `json:"account_id"`
	Currency      string  `json:"currency"`
	Balance       string  `json:"balance"`
	LastPostingAt *string `json:"last_posting_at"`
	AsOf          *string `json:"as_of"`
}

func balanceDTO(b service.Balance) balanceResponse {
	return balanceResponse{
		AccountID: b.AccountID.String(), Currency: string(b.Currency), Balance: bigString(b.Amount),
		LastPostingAt: timestampPtr(b.LastPostingAt), AsOf: timestampPtr(b.AsOf),
	}
}

type postingsPageResponse struct {
	Postings   []postingResponse `json:"postings"`
	NextCursor *string           `json:"next_cursor"`
}

func pageDTO(p service.PostingPage) postingsPageResponse {
	out := postingsPageResponse{Postings: make([]postingResponse, len(p.Postings))}
	for i, posting := range p.Postings {
		out.Postings[i] = postingDTO(posting)
	}
	if p.NextCursor != "" {
		out.NextCursor = &p.NextCursor
	}
	return out
}

type reconciliationResponse struct {
	StartedAt       string               `json:"started_at"`
	Deep            bool                 `json:"deep"`
	OK              bool                 `json:"ok"`
	AccountsChecked int64                `json:"accounts_checked"`
	Mismatches      []mismatchResponse   `json:"mismatches"`
	Currencies      []currencyResponse   `json:"currencies"`
	ChainBreaks     []chainBreakResponse `json:"chain_breaks"`
}

type mismatchResponse struct {
	AccountID string `json:"account_id"`
	Code      string `json:"code"`
	Currency  string `json:"currency"`
	Stored    string `json:"stored"`
	Expected  string `json:"expected"`
}

type currencyResponse struct {
	Currency string `json:"currency"`
	Debits   string `json:"debits"`
	Credits  string `json:"credits"`
	Balanced bool   `json:"balanced"`
}

type chainBreakResponse struct {
	PostingID int64  `json:"posting_id"`
	AccountID string `json:"account_id"`
	Recorded  string `json:"recorded"`
	Expected  string `json:"expected"`
}

func reconciliationDTO(r ledger.ReconciliationReport) reconciliationResponse {
	out := reconciliationResponse{
		StartedAt: timestamp(r.StartedAt), Deep: r.Deep, OK: r.OK(), AccountsChecked: r.AccountsChecked,
		Mismatches:  make([]mismatchResponse, len(r.Mismatches)),
		Currencies:  make([]currencyResponse, len(r.Currencies)),
		ChainBreaks: make([]chainBreakResponse, len(r.ChainBreaks)),
	}
	for i, m := range r.Mismatches {
		out.Mismatches[i] = mismatchResponse{
			AccountID: m.AccountID.String(), Code: m.Code, Currency: string(m.Currency),
			Stored: bigString(m.Stored), Expected: bigString(m.Expected),
		}
	}
	for i, c := range r.Currencies {
		out.Currencies[i] = currencyResponse{Currency: string(c.Currency), Debits: bigString(c.Debits), Credits: bigString(c.Credits), Balanced: c.Balanced()}
	}
	for i, b := range r.ChainBreaks {
		out.ChainBreaks[i] = chainBreakResponse{PostingID: b.PostingID, AccountID: b.AccountID.String(), Recorded: bigString(b.Recorded), Expected: bigString(b.Expected)}
	}
	return out
}
