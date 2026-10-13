-- +goose Up

-- Amounts are whole numbers of minor units (cents, wei, ...) stored as
-- NUMERIC(38,0): exact integer arithmetic with room for 18-decimal assets.

CREATE TABLE accounts (
  id               uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
  code             text          NOT NULL UNIQUE
                   CONSTRAINT accounts_code_format CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$'),
  currency         text          NOT NULL
                   CONSTRAINT accounts_currency_format CHECK (currency ~ '^[A-Z0-9]{2,10}$'),
  type             text          NOT NULL
                   CONSTRAINT accounts_type_valid CHECK (type IN ('ASSET','LIABILITY','EQUITY','INCOME','EXPENSE')),
  normal_balance   text          NOT NULL
                   CONSTRAINT accounts_normal_balance_valid CHECK (normal_balance IN ('DEBIT','CREDIT')),
  allow_negative   boolean       NOT NULL DEFAULT false,
  balance          numeric(38,0) NOT NULL DEFAULT 0,
  last_posting_at  timestamptz,
  created_at       timestamptz   NOT NULL DEFAULT now(),
  -- Target of the composite foreign key on postings (currency must match).
  CONSTRAINT accounts_id_currency_key UNIQUE (id, currency),
  -- Last line of defence for overdraft prevention (the service checks first).
  CONSTRAINT accounts_non_negative CHECK (allow_negative OR balance >= 0)
);

CREATE TABLE transactions (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  description  text        NOT NULL DEFAULT ''
               CONSTRAINT transactions_description_length CHECK (char_length(description) <= 500),
  -- UNIQUE: a transaction can be reversed at most once.
  reverses_id  uuid        UNIQUE REFERENCES transactions (id),
  created_at   timestamptz NOT NULL,
  CONSTRAINT transactions_not_self_reversing CHECK (reverses_id IS NULL OR reverses_id <> id)
);

CREATE TABLE postings (
  id             bigint        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  transaction_id uuid          NOT NULL REFERENCES transactions (id),
  account_id     uuid          NOT NULL,
  currency       text          NOT NULL,
  direction      text          NOT NULL
                 CONSTRAINT postings_direction_valid CHECK (direction IN ('DEBIT','CREDIT')),
  amount         numeric(38,0) NOT NULL
                 CONSTRAINT postings_amount_range CHECK (amount > 0 AND amount <= 1000000000000000000000000000000),
  balance_after  numeric(38,0) NOT NULL,
  recorded_at    timestamptz   NOT NULL,
  -- The posting's currency must equal its account's currency.
  CONSTRAINT postings_account_currency_fkey
    FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency)
);

-- Newest-first listing and keyset pagination per account.
CREATE INDEX postings_account_id_idx   ON postings (account_id, id DESC);
-- Historical balance lookups (as_of).
CREATE INDEX postings_account_time_idx ON postings (account_id, recorded_at DESC, id DESC);
CREATE INDEX postings_transaction_idx  ON postings (transaction_id);

CREATE TABLE idempotency_keys (
  key              text        PRIMARY KEY,
  request_hash     bytea       NOT NULL,
  response_status  integer     NOT NULL,
  response_body    jsonb       NOT NULL,
  transaction_id   uuid        REFERENCES transactions (id),
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE postings;
DROP TABLE transactions;
DROP TABLE accounts;
