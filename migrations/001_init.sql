-- =============================================================================
-- Wallet Transfer Service — Database Schema
-- =============================================================================

-- wallets
-- Stores each wallet and its current balance.
-- CHECK (balance >= 0) enforces the no-overdraft invariant at the DB level.
CREATE TABLE IF NOT EXISTS wallets (
    id         VARCHAR(36)    PRIMARY KEY,
    balance    NUMERIC(20, 8) NOT NULL DEFAULT 0 CHECK (balance >= 0),
    created_at TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

-- transfers
-- One row per transfer attempt. The UNIQUE constraint on idempotency_key is
-- the database-level guard against duplicate execution — application-level
-- checks are an optimisation on top of this constraint.
CREATE TABLE IF NOT EXISTS transfers (
    id               VARCHAR(36)    PRIMARY KEY,
    idempotency_key  VARCHAR(255)   NOT NULL UNIQUE,
    from_wallet_id   VARCHAR(36)    NOT NULL REFERENCES wallets(id),
    to_wallet_id     VARCHAR(36)    NOT NULL REFERENCES wallets(id),
    amount           NUMERIC(20, 8) NOT NULL CHECK (amount > 0),
    status           VARCHAR(20)    NOT NULL DEFAULT 'PENDING',
    created_at       TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_different_wallets CHECK (from_wallet_id <> to_wallet_id),
    CONSTRAINT chk_valid_status      CHECK (status IN ('PENDING', 'PROCESSED', 'FAILED'))
);

-- ledger_entries
-- Double-entry ledger: every transfer produces exactly one DEBIT and one CREDIT.
-- The combination of transfer_id + type is effectively unique (enforced by application logic).
CREATE TABLE IF NOT EXISTS ledger_entries (
    id          VARCHAR(36)    PRIMARY KEY,
    transfer_id VARCHAR(36)    NOT NULL REFERENCES transfers(id),
    wallet_id   VARCHAR(36)    NOT NULL REFERENCES wallets(id),
    type        VARCHAR(10)    NOT NULL CHECK (type IN ('DEBIT', 'CREDIT')),
    amount      NUMERIC(20, 8) NOT NULL CHECK (amount > 0),
    created_at  TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

-- idempotency_records
-- Stores the serialised HTTP response snapshot keyed by idempotency_key.
-- Allows exact replay of the original response for any duplicate request.
CREATE TABLE IF NOT EXISTS idempotency_records (
    idempotency_key   VARCHAR(255) PRIMARY KEY,
    transfer_id       VARCHAR(36)  NOT NULL REFERENCES transfers(id),
    response_snapshot JSONB        NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Indexes for common query patterns.
CREATE INDEX IF NOT EXISTS idx_transfers_from_wallet   ON transfers(from_wallet_id);
CREATE INDEX IF NOT EXISTS idx_transfers_to_wallet     ON transfers(to_wallet_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_transfer ON ledger_entries(transfer_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_wallet   ON ledger_entries(wallet_id);

-- =============================================================================
-- Seed data (development only — idempotent via ON CONFLICT DO NOTHING)
-- =============================================================================
INSERT INTO wallets (id, balance) VALUES
    ('wallet_1', 1000.00),
    ('wallet_2',  500.00),
    ('wallet_3',  250.00)
ON CONFLICT (id) DO NOTHING;
