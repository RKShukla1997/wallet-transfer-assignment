# Solution Design — Wallet Transfer Service

This document captures the high-level design for the wallet-to-wallet transfer
service described in [`ASSIGNMENT.md`](./ASSIGNMENT.md), written against the
rubric in [`evaluation_guide.md`](./evaluation_guide.md). It is meant to be
read before the implementation, per the assignment's "documentation-first"
workflow.

## Prerequisites

To build, run, and test this service locally you need:

| Requirement | Version | Why |
|---|---|---|
| [Go](https://go.dev/dl/) | 1.24+ | Builds `cmd/server`; `go.mod` pins `go 1.24` |
| [Docker](https://www.docker.com/) + Docker Compose | any recent version | Runs PostgreSQL locally via [`docker-compose.yml`](./docker-compose.yml) |
| PostgreSQL | 16 (via the `postgres:16-alpine` image) | Primary datastore; the `docker-compose.yml` service provisions this automatically |
| Network access to the Go module proxy | — | Needed once, to download `github.com/jackc/pgx/v5` and its transitive dependencies (`go mod download`/`go build` does this automatically) |

No other external services (message queues, caches, etc.) are required —
the whole system is the Go binary plus a single Postgres instance. Port
`5432` (Postgres) and `8080` (HTTP server, overridable via `HTTP_ADDR`) must
be free on the host. See [section 10](#10-how-to-run--test) for exact startup
commands.

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
                        ├─ 1. BEGIN TX
                        │
                        ├─ 2. SELECT wallets FOR UPDATE, locked in a stable
                        │       order (sorted by wallet id), BEFORE the
                        │       transfer row is inserted (see Design
                        │       Decisions below for why this ordering
                        │       matters)
                        │
                        ├─ 3. INSERT transfer ... ON CONFLICT (idempotency_key) DO NOTHING
                        │       │
                        │       ├─ row inserted (new)   -> continue to step 4
                        │       └─ no row inserted (dup) -> fetch existing transfer,
                        │                                   return its stored result
                        │                                   (no re-execution of side effects)
                        │
                        ├─ 4. validate balance >= amount
                        │       ├─ sufficient?  -> update balances, insert 2 ledger
                        │       │                  rows (DEBIT + CREDIT), mark
                        │       │                  transfer PROCESSED
                        │       └─ insufficient? -> mark transfer FAILED,
                        │                           no balance/ledger mutation
                        │
                        ├─ 5. COMMIT (single transaction boundary, either outcome)
                        │
                        └─ 6. return transfer result (id, status, balances)
```

## 2. Design Decisions & Trade-offs

This section explains *why* each major choice was made, including the
alternatives that were considered and rejected, and includes a sequence
view of the request flow focused on the decisions rather than the code.

### 2.1 High-level request sequence

```text
 Client            Handler             Service              Repository            PostgreSQL
   │   POST /transfers  │                   │                     │                     │
   │ ───────────────────▶                   │                     │                     │
   │                    │ decode + shape    │                     │                     │
   │                    │ validation only   │                     │                     │
   │                    │ ─────────────────▶│                     │                     │
   │                    │                   │ Validate() (domain) │                     │
   │                    │                   │ ───────────────────▶│                     │
   │                    │                   │                     │  BEGIN              │
   │                    │                   │                     │ ───────────────────▶│
   │                    │                   │ LockTwoForUpdate    │                     │
   │                    │                   │ ───────────────────▶│ SELECT ... FOR UPDATE
   │                    │                   │                     │ ───────────────────▶│
   │                    │                   │ InsertIfAbsent      │                     │
   │                    │                   │ ───────────────────▶│ INSERT ... ON CONFLICT
   │                    │                   │                     │ ───────────────────▶│
   │                    │                   │        ┌────────────┴─────────────┐       │
   │                    │                   │        │ new row?                 │       │
   │                    │                   │        │  no  -> fetch existing,  │       │
   │                    │                   │        │        skip to COMMIT    │       │
   │                    │                   │        │  yes -> continue below   │       │
   │                    │                   │        └────────────┬─────────────┘       │
   │                    │                   │ check balance, UpdateBalance x2,           │
   │                    │                   │ InsertDoubleEntry, MarkProcessed/Failed    │
   │                    │                   │ ───────────────────▶│ UPDATE / INSERT x N │
   │                    │                   │                     │ ───────────────────▶│
   │                    │                   │                     │  COMMIT             │
   │                    │                   │                     │ ───────────────────▶│
   │                    │  transfer result  │◀────────────────────│                     │
   │  HTTP response     │◀──────────────────│                     │                     │
   │◀───────────────────│                   │                     │                     │
```

### 2.2 Decisions and alternatives considered

| Decision | Chosen approach | Alternatives considered | Why chosen |
|---|---|---|---|
| Datastore | PostgreSQL | SQLite, MongoDB | Assignment's preferred option; strong transactional guarantees (MVCC, `SELECT ... FOR UPDATE`, unique constraints) that map directly onto the idempotency/concurrency requirements. SQLite is an acceptable fallback but doesn't demonstrate row-level locking as clearly under real concurrency. |
| Idempotency storage | Unique constraint on `transfers.idempotency_key`, no separate table | Dedicated `idempotency_records` table mapping key -> cached response | A separate table would need to be kept in sync with `transfers` and raises the question "what if they disagree?" — an extra failure mode with no added guarantee. Folding the key into `transfers` makes the row **the** record of whether a request has been seen, enforced by the database itself rather than application logic. |
| Idempotency detection mechanism | `INSERT ... ON CONFLICT (idempotency_key) DO NOTHING RETURNING *`, then fetch on miss | Read-then-write (`SELECT` to check existence, then `INSERT` if absent) | Read-then-write has a race: two concurrent requests with the same key can both see "no existing row" and both proceed to insert, before the unique constraint can be relied on. The atomic `INSERT ... ON CONFLICT` makes the database itself the single arbiter of "is this new," closing that race without any explicit locking. |
| Concurrency control for balances | Pessimistic locking: `SELECT ... FOR UPDATE` on both wallets, in a stable sorted order, inside one transaction | Optimistic locking (a `version` column checked on `UPDATE ... WHERE version = $x`) | Transfers are a write-heavy, contention-prone path (the same hot wallet can be debited by many concurrent transfers), where optimistic locking would mean frequent client-visible retries. Pessimistic locking lets conflicting transfers simply queue briefly instead of failing and having to be retried by the caller, which is simpler to reason about and keeps the API's exactly-once guarantee intact without extra retry logic in the service layer. |
| Lock acquisition order | Always lock the two wallets sorted by id, **before** inserting the transfer row | Lock in `(from, to)` request order; or lock only after inserting the transfer row | Locking in request order can deadlock: transfer A→B and transfer B→A running concurrently would lock in opposite orders. Sorting by id fixes a single global order. Locking *before* the insert also avoids a subtler deadlock: inserting into `transfers` takes an implicit foreign-key row lock on both wallets, and if that implicit lock were taken before the explicit `FOR UPDATE` lock, many concurrent transactions each holding their own implicit lock would deadlock trying to upgrade to exclusive access on the same row. This was caught by the concurrency test (`TestCreateTransfer_ConcurrentDebitsOnSameWallet`) during development — see the commit history for the before/after. |
| Balance check timing | After the row lock is acquired | Checking balance from an earlier, unlocked read | A read before the lock is a classic read-then-write race: two concurrent transfers could both read a sufficient balance before either writes. Checking after the lock is held guarantees no other transaction can change the balance out from under this one. |
| Insufficient funds handling | Mark the transfer `FAILED` and **commit** | Roll back the transaction and return an error | Insufficient funds is a normal business outcome, not an infrastructure failure. Committing a `FAILED` transfer means a retry with the same idempotency key deterministically replays the same `FAILED` result, rather than re-evaluating balances that may have changed since (which would break the "same key -> same result" guarantee). |
| Money representation | Integer minor units (`int64`, e.g. cents) | Floating point, or a `numeric`/decimal type | Floating point introduces rounding error that is unacceptable for a ledger. Integers in minor units are exact, simple to reason about, and map cleanly onto Go's `int64` without needing a decimal library. |
| Ledger writes | Always write DEBIT + CREDIT together via a single `InsertDoubleEntry` call | Allow callers to write individual ledger entries | A single call that always writes both sides makes it structurally impossible for the service layer to produce an unbalanced ledger (one entry without its pair). This is enforced by code shape, not just convention. |
| Transaction boundary | One DB transaction per `CreateTransfer` call, covering locking, idempotency insert, balance mutation, and ledger writes | Multiple smaller transactions (e.g., one for idempotency, one for the transfer) | Multiple transactions would reintroduce the exact race conditions idempotency and locking are meant to prevent (a crash between transactions could leave a `PENDING` transfer with no resolution). One transaction means the whole operation is atomic: it either fully commits or fully rolls back. |
| Layering | Handler → Service → Repository → Domain, each with a single responsibility | A "fat" handler or a single `transfer.go` file mixing SQL and business rules | Keeping business rules (idempotency, locking order, balance rules, state transitions) exclusively in the service layer, and SQL exclusively in the repository layer, makes each layer independently testable and keeps the repository free of workflow decisions — directly addressing the evaluation rubric's "repositories limited to persistence concerns" criterion. |

## 3. Database Schema

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

## 4. Idempotency Strategy

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

## 5. Concurrency Strategy

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

## 6. Transfer State Machine

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

## 7. Layering Summary

| Layer | Responsibility | Must NOT contain |
|---|---|---|
| Handler | request/response shape, HTTP status mapping | business rules, SQL |
| Service | idempotency, locking order, validation, ledger orchestration, state transitions | raw SQL, HTTP concerns |
| Repository | SQL statements, row locks | conditional workflow decisions |
| Domain | entities, state-transition validity | persistence, transport |

## 8. Testing Strategy

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

## 9. Out of Scope / Assumptions

- Authentication/authorization is out of scope; the assignment focuses on
  transactional correctness.
- Wallet creation/funding is assumed to be seeded or provided via a minimal
  endpoint, not a core deliverable.
- Optional enhancements (balance API, transfer history API, metrics) are
  listed in `ASSIGNMENT.md` as nice-to-haves and are addressed only if time
  permits after the core requirements above are solid.

## 10. How to Run & Test

See [Prerequisites](#prerequisites) above for required tooling.

### 1. Start PostgreSQL

```bash
docker compose up -d postgres
```

This starts Postgres on `localhost:5432` and automatically applies
[`migrations/001_init.sql`](./migrations/001_init.sql) on first boot (via the
container's `docker-entrypoint-initdb.d` mechanism). Check it's healthy:

```bash
docker compose ps
```

### 2. Run the HTTP server

```bash
go run ./cmd/server
```

By default the server listens on `:8080` and connects to
`postgres://postgres:postgres@localhost:5432/wallet_transfer?sslmode=disable`.
Both are overridable via environment variables:

```text
DATABASE_URL=postgres://postgres:postgres@localhost:5432/wallet_transfer?sslmode=disable
HTTP_ADDR=:8080
```

### 3. Exercise the API manually

```bash
curl -X POST localhost:8080/wallets -d '{"id":"wallet_1","startingBalance":1000}'
curl -X POST localhost:8080/wallets -d '{"id":"wallet_2","startingBalance":0}'

curl -X POST localhost:8080/transfers -d '{
  "idempotencyKey":"abc123",
  "fromWalletId":"wallet_1",
  "toWalletId":"wallet_2",
  "amount":100
}'

# replay with the same idempotencyKey: returns the identical transfer result,
# no additional balance/ledger changes
curl -X POST localhost:8080/transfers -d '{
  "idempotencyKey":"abc123",
  "fromWalletId":"wallet_1",
  "toWalletId":"wallet_2",
  "amount":100
}'

curl localhost:8080/wallets/wallet_1   # -> balance 900
curl localhost:8080/wallets/wallet_2   # -> balance 100
curl localhost:8080/transfers/<id>
```

### 4. Run the automated tests

The tests in [`tests/`](./tests) run against a real Postgres instance (they
call `t.Skip` automatically if one isn't reachable, so `go test ./...`
remains safe to run without a database available):

```bash
docker compose up -d postgres
go test ./... -v
```

Point tests at a different database with `TEST_DATABASE_URL`. The suite
covers the happy path, sequential and concurrent duplicate-idempotency-key
replay, concurrent debits on the same wallet (verifying no lost updates /
double spend), insufficient funds, and request validation.

### 5. Lint / format check

```bash
go vet ./...
gofmt -l .      # should print nothing
```
