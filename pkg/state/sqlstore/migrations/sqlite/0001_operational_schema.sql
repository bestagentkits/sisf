-- Operational schema of one or more factories, SQLite dialect.
--
-- Every table carries factory_id and every key starts with it, so factories
-- that share a database never meet. Instants are nanoseconds since the Unix
-- epoch in UTC; 0 means unset. The PostgreSQL script next to this one
-- creates the same tables and must change together with it.

-- The lifecycle record of each issue. version makes every write a
-- compare-and-swap.
CREATE TABLE records (
    factory_id      TEXT    NOT NULL,
    issue           TEXT    NOT NULL,
    state           TEXT    NOT NULL,
    version         INTEGER NOT NULL,
    idempotency_key TEXT    NOT NULL,
    lease_owner     TEXT    NOT NULL,
    fencing_token   INTEGER NOT NULL,
    attempt         INTEGER NOT NULL,
    run_id          TEXT    NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (factory_id, issue)
);
CREATE INDEX records_by_state ON records (factory_id, state, updated_at);

-- One row per applied lifecycle step, keyed by its idempotency key. It holds
-- the record as it was written, which is what a replayed write returns.
CREATE TABLE transitions (
    factory_id       TEXT    NOT NULL,
    issue            TEXT    NOT NULL,
    idempotency_key  TEXT    NOT NULL,
    from_state       TEXT    NOT NULL,
    to_state         TEXT    NOT NULL,
    event            TEXT    NOT NULL,
    version          INTEGER NOT NULL,
    lease_owner      TEXT    NOT NULL,
    fencing_token    INTEGER NOT NULL,
    attempt          INTEGER NOT NULL,
    run_id           TEXT    NOT NULL,
    actor_id         TEXT    NOT NULL,
    reason           TEXT    NOT NULL,
    evidence_refs    TEXT    NOT NULL,
    -- The built candidate or the release the step is about, when it names one.
    candidate_digest TEXT    NOT NULL,
    release_digest   TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (factory_id, issue, idempotency_key)
);

-- One row per leased resource, kept after release so that token only grows.
-- An empty holder means the lease is free.
CREATE TABLE leases (
    factory_id TEXT    NOT NULL,
    resource   TEXT    NOT NULL,
    holder     TEXT    NOT NULL,
    token      INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (factory_id, resource)
);

-- Deliveries received from outside. The unique key is what makes a delivery
-- count once however often it arrives.
CREATE TABLE inbox (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    factory_id   TEXT    NOT NULL,
    source       TEXT    NOT NULL,
    delivery_id  TEXT    NOT NULL,
    event_type   TEXT    NOT NULL,
    payload      BLOB    NOT NULL,
    received_at  INTEGER NOT NULL,
    processed_at INTEGER NOT NULL,
    UNIQUE (factory_id, source, delivery_id)
);
CREATE INDEX inbox_pending ON inbox (factory_id, processed_at, received_at, id);

-- Side effects to perform after commit; one message per key.
CREATE TABLE outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    factory_id      TEXT    NOT NULL,
    topic           TEXT    NOT NULL,
    msg_key         TEXT    NOT NULL,
    payload         BLOB    NOT NULL,
    attempts        INTEGER NOT NULL,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    sent_at         INTEGER NOT NULL,
    UNIQUE (factory_id, msg_key)
);
CREATE INDEX outbox_pending ON outbox (factory_id, sent_at, next_attempt_at, id);

-- Money set aside for runs. Amounts are millionths of the ledger currency.
CREATE TABLE reservations (
    factory_id        TEXT    NOT NULL,
    id                TEXT    NOT NULL,
    issue             TEXT    NOT NULL,
    run_id            TEXT    NOT NULL,
    budget_mode       TEXT    NOT NULL,
    amount            INTEGER NOT NULL,
    status            TEXT    NOT NULL,
    hard_cap_enforced BOOLEAN NOT NULL,
    idempotency_key   TEXT    NOT NULL,
    created_at        INTEGER NOT NULL,
    PRIMARY KEY (factory_id, id)
);
CREATE INDEX reservations_by_status ON reservations (factory_id, status);

-- Metered spending. Rows are only ever added.
CREATE TABLE usage_ledger (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    factory_id     TEXT    NOT NULL,
    issue          TEXT    NOT NULL,
    run_id         TEXT    NOT NULL,
    reservation_id TEXT    NOT NULL,
    step           TEXT    NOT NULL,
    model_route    TEXT    NOT NULL,
    input_tokens   INTEGER NOT NULL,
    output_tokens  INTEGER NOT NULL,
    cost           INTEGER NOT NULL,
    verified       BOOLEAN NOT NULL,
    recorded_at    INTEGER NOT NULL
);
CREATE INDEX usage_ledger_by_time ON usage_ledger (factory_id, recorded_at);

-- Versioned documents of components that have no table of their own.
CREATE TABLE kv (
    factory_id  TEXT    NOT NULL,
    namespace   TEXT    NOT NULL,
    entry_key   TEXT    NOT NULL,
    entry_value BLOB    NOT NULL,
    version     INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (factory_id, namespace, entry_key)
);

-- The audit chain. body is the exact bytes that hash covers, and it repeats
-- seq and prev_hash, so a row moved to another position no longer verifies.
CREATE TABLE audit_log (
    factory_id TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    prev_hash  TEXT    NOT NULL,
    hash       TEXT    NOT NULL,
    body       BLOB    NOT NULL,
    PRIMARY KEY (factory_id, seq)
);

-- The last link of each chain. Appends serialize on this row, and a chain
-- whose last rows were removed no longer matches it.
CREATE TABLE audit_head (
    factory_id TEXT    NOT NULL PRIMARY KEY,
    seq        INTEGER NOT NULL,
    hash       TEXT    NOT NULL
);

-- Written history cannot be changed or removed through any connection.
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
CREATE TRIGGER usage_ledger_no_update BEFORE UPDATE ON usage_ledger
BEGIN
    SELECT RAISE(ABORT, 'usage_ledger is append-only');
END;
CREATE TRIGGER usage_ledger_no_delete BEFORE DELETE ON usage_ledger
BEGIN
    SELECT RAISE(ABORT, 'usage_ledger is append-only');
END;
