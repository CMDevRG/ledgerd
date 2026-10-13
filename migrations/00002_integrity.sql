-- +goose Up

-- ---------------------------------------------------------------------------
-- Append-only: postings and transactions can never be updated, deleted or
-- truncated, by anyone (including the table owner). Corrections are made with
-- new, offsetting transactions.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION reject_modification() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% on % is not allowed: the ledger is append-only', TG_OP, TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';  -- 42501
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER transactions_append_only
  BEFORE UPDATE OR DELETE ON transactions
  FOR EACH ROW EXECUTE FUNCTION reject_modification();
CREATE TRIGGER postings_append_only
  BEFORE UPDATE OR DELETE ON postings
  FOR EACH ROW EXECUTE FUNCTION reject_modification();
CREATE TRIGGER transactions_no_truncate
  BEFORE TRUNCATE ON transactions
  FOR EACH STATEMENT EXECUTE FUNCTION reject_modification();
CREATE TRIGGER postings_no_truncate
  BEFORE TRUNCATE ON postings
  FOR EACH STATEMENT EXECUTE FUNCTION reject_modification();

-- ---------------------------------------------------------------------------
-- Balanced transactions: checked at COMMIT time (deferred), after every posting
-- of the transaction has been inserted. Whatever the application does, the
-- database refuses to commit a transaction whose debits and credits differ in
-- any currency, or that has fewer than two postings.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION check_transaction_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  tx_id    uuid;
  n        bigint;
  bad_cur  text;
  bad_diff numeric;
BEGIN
  IF TG_TABLE_NAME = 'transactions' THEN
    tx_id := NEW.id;
  ELSE
    tx_id := NEW.transaction_id;
  END IF;

  SELECT count(*) INTO n FROM postings WHERE transaction_id = tx_id;
  IF n < 2 THEN
    RAISE EXCEPTION 'transaction % must have at least 2 postings, found %', tx_id, n
      USING ERRCODE = 'check_violation';  -- 23514
  END IF;

  SELECT p.currency,
         sum(CASE p.direction WHEN 'DEBIT' THEN p.amount ELSE -p.amount END)
    INTO bad_cur, bad_diff
    FROM postings p
   WHERE p.transaction_id = tx_id
   GROUP BY p.currency
  HAVING sum(CASE p.direction WHEN 'DEBIT' THEN p.amount ELSE -p.amount END) <> 0
   ORDER BY p.currency
   LIMIT 1;

  IF FOUND THEN
    RAISE EXCEPTION 'transaction % is not balanced in %: debits minus credits = %', tx_id, bad_cur, bad_diff
      USING ERRCODE = 'check_violation';  -- 23514
  END IF;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Fires once per posting row at commit; with at most 100 postings per
-- transaction the repeated check is cheap.
CREATE CONSTRAINT TRIGGER postings_balanced
  AFTER INSERT ON postings
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION check_transaction_balanced();

-- Also covers a transaction row inserted with no postings at all.
CREATE CONSTRAINT TRIGGER transactions_have_postings
  AFTER INSERT ON transactions
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION check_transaction_balanced();

-- +goose Down
DROP TRIGGER transactions_have_postings ON transactions;
DROP TRIGGER postings_balanced ON postings;
DROP FUNCTION check_transaction_balanced();
DROP TRIGGER postings_no_truncate ON postings;
DROP TRIGGER transactions_no_truncate ON transactions;
DROP TRIGGER postings_append_only ON postings;
DROP TRIGGER transactions_append_only ON transactions;
DROP FUNCTION reject_modification();
