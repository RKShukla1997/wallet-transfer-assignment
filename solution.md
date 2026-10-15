# Solution Design — Wallet Transfer Service

This document captures the high-level design for the wallet-to-wallet transfer
service described in [`ASSIGNMENT.md`](./ASSIGNMENT.md), written against the
rubric in [`evaluation_guide.md`](./evaluation_guide.md). It is meant to be
read before the implementation, per the assignment's "documentation-first"
workflow.

## 1. High-Level Architecture

```text
                              ┌──────────────────────────┐
                              │        HTTP Client        │
                              └─────────────┬─────────────┘
                                            │ POST /transfers
                                            │ GET  /wallets/{id}
                                            ▼
┌───────────────────────────────────────────────────────────────────────┐
│ Handler Layer (internal/handler)                                      │
│  - decode & validate request shape (JSON, required fields, amount>0)  │
│  - map domain/service errors -> HTTP status codes                     │
│  - NO business logic, NO SQL                                          │
└───────────────────────────────────┬────────────────────────────────────┘
                                     ▼
┌───────────────────────────────────────────────────────────────────────┐
│ Service Layer (internal/service)                                      │
│  - orchestrates the transfer workflow                                 │
│  - idempotency check / replay                                         │
│  - opens the DB transaction & locking order                           │
│  - balance validation, state transitions, ledger generation           │
│  - the ONLY layer that knows "what a transfer means"                  │
└───────────────────────────────────┬────────────────────────────────────┘
                                     ▼
┌───────────────────────────────────────────────────────────────────────┐
│ Repository Layer (internal/repository)                                │
│  - plain persistence operations (CRUD, row locks)                     │
│  - no workflow decisions, no conditional business rules                │
│  - WalletRepo / TransferRepo / LedgerRepo                              │
└───────────────────────────────────┬────────────────────────────────────┘
                                     ▼
┌───────────────────────────────────────────────────────────────────────┐
│ PostgreSQL                                                             │
│  wallets  │  transfers (idempotency_key UNIQUE)  │  ledger_entries     │
└───────────────────────────────────────────────────────────────────────┘

Cross-cutting: internal/domain (entities + state-transition rules used by
the service layer; no persistence or transport concerns).
```

### Request flow for `POST /transfers`

```text
Client -> Handler -> Service.CreateTransfer(ctx, req)
                        │
                        ├─ 1. INSERT transfer ... ON CONFLICT (idempotency_key) DO NOTHING
                        │       │
                        │       ├─ row inserted (new)   -> continue to step 2
                        │       └─ no row inserted (dup) -> fetch existing transfer,
                        │                                   return its stored result
                        │                                   (no re-execution of side effects)
                        │
                        ├─ 2. BEGIN TX
                        │       ├─ SELECT wallets FOR UPDATE, locked in a stable
                        │       │   order (sorted by wallet id) to avoid deadlocks
                        │       ├─ validate balance >= amount
                        │       ├─ sufficient?  -> update balances, insert 2 ledger
                        │       │                  rows (DEBIT + CREDIT), mark
                        │       │                  transfer PROCESSED
                        │       └─ insufficient? -> mark transfer FAILED,
                        │                           no balance/ledger mutation
                        │   COMMIT (single boundary either way)
                        │
                        └─ 3. return transfer result (id, status, balances)
```

## 2. Database Schema

```sql
wallets (
  id              TEXT PRIMARY KEY,
  balance         BIGINT NOT NULL CHECK (balance >= 0),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
)

transfers (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  idempotency_key  TEXT NOT NULL UNIQUE,        -- dedup enforced by the DB
  from_wallet_id   TEXT NOT NULL REFERENCES wallets(id),
  to_wallet_id     TEXT NOT NULL REFERENCES wallets(id),
  amount           BIGINT NOT NULL CHECK (amount > 0),
  status           TEXT NOT NULL CHECK (status IN ('PENDING','PROCESSED','FAILED')),
  failure_reason   TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (from_wallet_id <> to_wallet_id)
)

ledger_entries (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  transfer_id    UUID NOT NULL REFERENCES transfers(id),   -- no orphan ledger rows
  wallet_id      TEXT NOT NULL REFERENCES wallets(id),
  entry_type     TEXT NOT NULL CHECK (entry_type IN ('DEBIT','CREDIT')),
  amount         BIGINT NOT NULL CHECK (amount > 0),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
)

-- Indexes
CREATE UNIQUE INDEX idx_transfers_idempotency_key ON transfers(idempotency_key);
CREATE INDEX idx_ledger_entries_transfer_id ON ledger_entries(transfer_id);
CREATE INDEX idx_ledger_entries_wallet_id ON ledger_entries(wallet_id);
```

Notes:

- Amounts are stored as integer minor units (e.g. cents), never floating point.
- There is no separate `idempotency_records` table: `transfers.idempotency_key`
  **is** the idempotency record. One fewer table to keep in sync, and the
  uniqueness guarantee lives where the side effect is recorded.
- `ledger_entries.transfer_id` is `NOT NULL`, so a ledger row can never exist
  without a parent transfer, and every `PROCESSED` transfer is required (by
  the service layer) to produce exactly one `DEBIT` and one `CREDIT` row
  whose amounts match the transfer amount — keeping the ledger balanced.

## 3. Idempotency Strategy

1. The client supplies `idempotencyKey`; it is stored as a `UNIQUE` column on
   `transfers`, not derived or checked only in application memory.
2. The service performs `INSERT ... ON CONFLICT (idempotency_key) DO NOTHING
   RETURNING *`.
   - If a row comes back, this is a new request — proceed to execute it.
   - If no row comes back, a transfer with that key already exists — fetch it
     and return its current state (`PENDING` / `PROCESSED` / `FAILED`)
     directly, without repeating any balance or ledger mutation.
3. Because the guarantee is a database constraint rather than an in-memory
   cache or lock, it is safe across concurrent requests, retries, and process
   restarts.

## 4. Concurrency Strategy

- Every transfer executes inside one database transaction; `BEGIN` happens
  right after the idempotency insert succeeds, and `COMMIT` is the only exit
  point (success or domain failure).
- Both wallet rows are locked with `SELECT ... FOR UPDATE`, always acquired
  in a **stable order** (e.g. sorted by wallet id) regardless of debit/credit
  direction. This prevents deadlocks when two concurrent transfers move
  money between the same pair of wallets in opposite directions.
- The balance check (`from_wallet.balance >= amount`) happens *after* the
  row lock is held, eliminating the classic read-then-write race where two
  concurrent transfers both read a sufficient balance before either writes.
- Insufficient funds is a normal, expected outcome: the transfer is marked
  `FAILED` and committed (not rolled back as an error), so a retry with the
  same idempotency key returns the same `FAILED` result rather than
  re-evaluating balances that may have since changed.

## 5. Transfer State Machine

```text
        INSERT (new idempotency key)
                 │
                 ▼
             PENDING
             /      \
   sufficient        insufficient
     funds              funds
        │                  │
        ▼                  ▼
   PROCESSED             FAILED
```

- `PENDING` exists only for the lifetime of the enclosing transaction; by the
  time the HTTP response is returned, a transfer is always `PROCESSED` or
  `FAILED`. This keeps the state machine simple while still modeling a
  distinct "accepted but not yet resolved" state internally.
- Duplicate requests never re-enter the state machine — they are served
  directly from the stored terminal state.

## 6. Layering Summary

| Layer | Responsibility | Must NOT contain |
|---|---|---|
| Handler | request/response shape, HTTP status mapping | business rules, SQL |
| Service | idempotency, locking order, validation, ledger orchestration, state transitions | raw SQL, HTTP concerns |
| Repository | SQL statements, row locks | conditional workflow decisions |
| Domain | entities, state-transition validity | persistence, transport |

## 7. Testing Strategy

Behavior-focused tests, not implementation-detail assertions:

- **Happy path**: a transfer produces exactly two balanced ledger entries and
  correct resulting balances on both wallets.
- **Idempotent replay**: submitting the same `idempotencyKey` twice (including
  concurrently) results in exactly one set of ledger entries and both calls
  return an identical transfer result.
- **Insufficient funds**: transfer is marked `FAILED`, no balances change, no
  ledger entries are created.
- **Concurrency**: multiple goroutines issuing concurrent debits against the
  same wallet converge on the correct final balance with no negative balance
  and no lost updates.

## 8. Out of Scope / Assumptions

- Authentication/authorization is out of scope; the assignment focuses on
  transactional correctness.
- Wallet creation/funding is assumed to be seeded or provided via a minimal
  endpoint, not a core deliverable.
- Optional enhancements (balance API, transfer history API, metrics) are
  listed in `ASSIGNMENT.md` as nice-to-haves and are addressed only if time
  permits after the core requirements above are solid.
