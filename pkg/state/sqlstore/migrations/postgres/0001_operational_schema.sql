-- Operational schema of one or more factories, PostgreSQL dialect.
--
-- Every table carries factory_id and every key starts with it, so factories
-- that share a database never meet. Instants are nanoseconds since the Unix
-- epoch in UTC; 0 means unset. The SQLite script next to this one creates
-- the same tables and must change together with it. Keys that are listed in
-- order use the "C" collation, which is the byte order SQLite sorts by.

-- The lifecycle record of each issue. version makes every write a
-- compare-and-swap.
CREATE TABLE records (
    factory_id      TEXT   NOT NULL,
    issue           TEXT   COLLATE "C" NOT NULL,
    state           TEXT   NOT NULL,
    version         BIGINT NOT NULL,
    idempotency_key TEXT   NOT NULL,
    lease_owner     TEXT   NOT NULL,
    fencing_token   BIGINT NOT NULL,
    attempt         BIGINT NOT NULL,
    run_id          TEXT   NOT NULL,
    updated_at      BIGINT NOT NULL,
    PRIMARY KEY (factory_id, issue)
);
CREATE INDEX records_by_state ON records (factory_id, state, updated_at);

-- One row per applied lifecycle step, keyed by its idempotency key. It holds
-- the record as it was written, which is what a replayed write returns.
CREATE TABLE transitions (
    factory_id       TEXT   NOT NULL,
    issue            TEXT   COLLATE "C" NOT NULL,
    idempotency_key  TEXT   NOT NULL,
    from_state       TEXT   NOT NULL,
    to_state         TEXT   NOT NULL,
    event            TEXT   NOT NULL,
    version          BIGINT NOT NULL,
    lease_owner      TEXT   NOT NULL,
    fencing_token    BIGINT NOT NULL,
    attempt          BIGINT NOT NULL,
    run_id           TEXT   NOT NULL,
    actor_id         TEXT   NOT NULL,
    reason           TEXT   NOT NULL,
    evidence_refs    TEXT   NOT NULL,
    -- The built candidate or the release the step is about, when it names one.
    candidate_digest TEXT   NOT NULL,
    release_digest   TEXT   NOT NULL,
    created_at       BIGINT NOT NULL,
    PRIMARY KEY (factory_id, issue, idempotency_key)
);

-- One row per leased resource, kept after release so that token only grows.
-- An empty holder means the lease is free.
CREATE TABLE leases (
    factory_id TEXT   NOT NULL,
    resource   TEXT   NOT NULL,
    holder     TEXT   NOT NULL,
    token      BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    PRIMARY KEY (factory_id, resource)
);

-- Deliveries received from outside. The unique key is what makes a delivery
-- count once however often it arrives.
CREATE TABLE inbox (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    factory_id   TEXT   NOT NULL,
    source       TEXT   NOT NULL,
    delivery_id  TEXT   NOT NULL,
    event_type   TEXT   NOT NULL,
    payload      BYTEA  NOT NULL,
    received_at  BIGINT NOT NULL,
    processed_at BIGINT NOT NULL,
    UNIQUE (factory_id, source, delivery_id)
);
CREATE INDEX inbox_pending ON inbox (factory_id, processed_at, received_at, id);

-- Side effects to perform after commit; one message per key.
CREATE TABLE outbox (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    factory_id      TEXT   NOT NULL,
    topic           TEXT   NOT NULL,
    msg_key         TEXT   NOT NULL,
    payload         BYTEA  NOT NULL,
    attempts        BIGINT NOT NULL,
    next_attempt_at BIGINT NOT NULL,
    last_error      TEXT   NOT NULL,
    created_at      BIGINT NOT NULL,
    sent_at         BIGINT NOT NULL,
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
    amount            BIGINT  NOT NULL,
    status            TEXT    NOT NULL,
    hard_cap_enforced BOOLEAN NOT NULL,
    idempotency_key   TEXT    NOT NULL,
    created_at        BIGINT  NOT NULL,
    PRIMARY KEY (factory_id, id)
);
CREATE INDEX reservations_by_status ON reservations (factory_id, status);

-- Metered spending. Rows are only ever added.
CREATE TABLE usage_ledger (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    factory_id     TEXT    NOT NULL,
    issue          TEXT    NOT NULL,
    run_id         TEXT    NOT NULL,
    reservation_id TEXT    NOT NULL,
    step           TEXT    NOT NULL,
    model_route    TEXT    NOT NULL,
    input_tokens   BIGINT  NOT NULL,
    output_tokens  BIGINT  NOT NULL,
    cost           BIGINT  NOT NULL,
    verified       BOOLEAN NOT NULL,
    recorded_at    BIGINT  NOT NULL
);
CREATE INDEX usage_ledger_by_time ON usage_ledger (factory_id, recorded_at);

-- Versioned documents of components that have no table of their own.
CREATE TABLE kv (
    factory_id  TEXT   NOT NULL,
    namespace   TEXT   NOT NULL,
    entry_key   TEXT   COLLATE "C" NOT NULL,
    entry_value BYTEA  NOT NULL,
    version     BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL,
    PRIMARY KEY (factory_id, namespace, entry_key)
);

-- The audit chain. body is the exact bytes that hash covers, and it repeats
-- seq and prev_hash, so a row moved to another position no longer verifies.
CREATE TABLE audit_log (
    factory_id TEXT   NOT NULL,
    seq        BIGINT NOT NULL,
    prev_hash  TEXT   NOT NULL,
    hash       TEXT   NOT NULL,
    body       BYTEA  NOT NULL,
    PRIMARY KEY (factory_id, seq)
);

-- The last link of each chain. Appends serialize on this row, and a chain
-- whose last rows were removed no longer matches it.
CREATE TABLE audit_head (
    factory_id TEXT   NOT NULL PRIMARY KEY,
    seq        BIGINT NOT NULL,
    hash       TEXT   NOT NULL
);

-- Written history cannot be changed or removed by any role, the table owner
-- included, for as long as these triggers are in place.
CREATE FUNCTION sisf_reject_change() RETURNS trigger LANGUAGE plpgsql AS $reject$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$reject$;

CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION sisf_reject_change();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION sisf_reject_change();
CREATE TRIGGER usage_ledger_append_only BEFORE UPDATE OR DELETE ON usage_ledger
    FOR EACH ROW EXECUTE FUNCTION sisf_reject_change();
CREATE TRIGGER usage_ledger_no_truncate BEFORE TRUNCATE ON usage_ledger
    FOR EACH STATEMENT EXECUTE FUNCTION sisf_reject_change();
