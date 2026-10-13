-- +goose Up

-- Least privilege for the running service. These grants assume the roles were
-- created by deploy/initdb/01-roles.sh. The migration runs as ledgerd_owner.

GRANT USAGE ON SCHEMA public TO ledgerd_app;

-- Append-only tables: the service may read and insert, nothing else.
GRANT SELECT, INSERT ON transactions, postings TO ledgerd_app;

-- Accounts: the service may create them and read them. After creation it may
-- change only the cached balance and the last-posting timestamp; code,
-- currency, type and normal balance are immutable for the service.
GRANT SELECT, INSERT ON accounts TO ledgerd_app;
GRANT UPDATE (balance, last_posting_at) ON accounts TO ledgerd_app;

-- Idempotency keys are operational data: full access, including cleanup.
GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO ledgerd_app;

-- postings.id is an identity column; inserting needs its sequence.
GRANT USAGE ON SEQUENCE postings_id_seq TO ledgerd_app;

-- +goose Down
REVOKE USAGE ON SEQUENCE postings_id_seq FROM ledgerd_app;
REVOKE ALL ON idempotency_keys FROM ledgerd_app;
REVOKE ALL ON accounts FROM ledgerd_app;
REVOKE ALL ON transactions, postings FROM ledgerd_app;
REVOKE USAGE ON SCHEMA public FROM ledgerd_app;
