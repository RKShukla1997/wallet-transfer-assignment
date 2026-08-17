## Summary

Implemented a wallet-to-wallet transfer service in Go (PostgreSQL) that satisfies:

- **Idempotent transfers** via a dedicated `idempotency_records` table backed by a UNIQUE DB constraint
- **Double-entry ledger** — every transfer atomically produces one DEBIT and one CREDIT entry
- **Concurrency-safe balance updates** via `SELECT FOR UPDATE` with deterministic lock ordering
- **Clean layered architecture** — handler → service → repository → domain, with no cross-layer business logic leakage
- **12 behavioral tests** covering success, idempotency, ledger correctness, validation errors, concurrent debits, state transitions, and retry-after-failure

Tech stack: Go 1.22, PostgreSQL 16, `pgx/v5`, `chi` router.

---

## AI disclosure

**Tool used:** GitHub Copilot (Claude Sonnet 4.6 model) inside VS Code.

**How I used it:** I used Copilot as a collaborative design and implementation partner — not to blindly generate the solution, but to work through design decisions step by step before writing any code. The workflow was:

1. Asked Copilot to analyse the assignment and identify key design areas
2. Discussed three candidate approaches (pessimistic locking, optimistic locking, event sourcing) and evaluated pros/cons before selecting one
3. Created `solution.md` documenting the chosen design with architecture diagrams before any implementation
4. Implemented each layer (domain → repository → service → handler) in order, reviewing each file before moving on
5. Asked Copilot to audit the implementation against the evaluation guide and identify gaps — it found the missing `ValidateTransition` domain guard and the untested "retry-after-failure" behaviour, which were then fixed

**Prompts used (in session order):**
1. "go through the assignment.md and help me understand the problem statement and also analyse the key areas while designing the solution"
2. "can you create a solution.md and help me with three possible approaches and which one to pick based on pros and cons"
3. "keeping approach 1 as final best solution can you create a high level architectural diagram of this approach in solution.md"
4. "in this now remove the comparison summary and other two approaches"
5. "now based on the solution.md file implement the solution along with its various components using golang"
6. "can we do this without makefile"
7. "instead of doing this all manually can we create a script for doing this"
8. "can you help me with sample payload to test these APIs"
9. "yes please create a postman collection preconfigured"
10. "can you check whether all the required parameters are met from evaluation guideline and solution.md"
11. "now remove the scripts folder and add the manual steps to start the service and test it using the postman collection"
12. "what are the steps to submit the solution"
13. "can you update this pull_request_template.md to update the pull request summary as per our implementation and session"

A full transcript of the session is included in the repository at `ai-transcript.md` *(or available on request)*.

---

## Schema Design

Four tables introduced:

### `wallets`
Stores each wallet and its running balance.
- `balance NUMERIC(20,8) CHECK (balance >= 0)` — DB-enforced non-negative constraint prevents any overdraft reaching persistent storage
- `updated_at` tracked for audit

### `transfers`
One row per completed or in-flight transfer.
- `idempotency_key VARCHAR(255) UNIQUE` — the primary deduplication guard; a second INSERT with the same key fails with SQLSTATE 23505 regardless of application-level checks
- `CHECK (from_wallet_id <> to_wallet_id)` — rejects self-transfers at the DB level
- `CHECK (status IN ('PENDING','PROCESSED','FAILED'))` — only valid states accepted
- `CHECK (amount > 0)` — non-positive amounts rejected

### `ledger_entries`
Append-only double-entry records — exactly two rows per transfer.
- FK to `transfers(id)` and `wallets(id)` — orphaned entries are impossible
- `CHECK (type IN ('DEBIT','CREDIT'))`

### `idempotency_records`
Stores the full serialised HTTP response snapshot keyed by `idempotency_key` (PK).
- FK to `transfers(id)` — a record can only exist if the transfer committed
- `response_snapshot JSONB` — allows exact replay of the original response without re-executing the transfer

### Indexes
```sql
idx_transfers_from_wallet    ON transfers(from_wallet_id)
idx_transfers_to_wallet      ON transfers(to_wallet_id)
idx_ledger_entries_transfer  ON ledger_entries(transfer_id)
idx_ledger_entries_wallet    ON ledger_entries(wallet_id)
```

---

## Idempotency Strategy

Two-level enforcement:

**Level 1 — Pre-transaction fast path**
Before opening a transaction, the service queries `idempotency_records` for the incoming key. If found, the stored `response_snapshot` is deserialised and returned immediately — no DB writes, no wallet locks.

**Level 2 — UNIQUE constraint safety net**
If two concurrent requests with the same key both pass the pre-tx check (race window), they both enter transactions and race to INSERT into `transfers`. The UNIQUE constraint on `transfers.idempotency_key` ensures only one succeeds. The loser receives SQLSTATE 23505, rolls back, and falls back to the idempotency lookup to return the winner's result.

**Failed transfers do not consume the idempotency key**
Business-rule failures (e.g. insufficient funds) cause a full transaction rollback — no `transfers` row and no `idempotency_records` row are written. The caller may retry with the exact same key once the underlying issue is resolved (e.g. after topping up the wallet). This is intentional and provides better UX than locking the key on failure.

---

## Concurrency Strategy

**Approach: Pessimistic row-level locking (`SELECT FOR UPDATE`)**

Inside the transaction, both wallet rows are locked before any balance is read or written:

```sql
SELECT id, balance, ... FROM wallets WHERE id = $1 FOR UPDATE
```

This serialises all concurrent transfers that touch the same wallet — only one transaction holds the lock at a time, preventing read-then-write races and double-spending.

**Deadlock prevention — ascending lock ordering**

When a transfer involves two wallets (A and B), locks are always acquired in ascending wallet ID order regardless of which is source and which is destination:

```
Lock(min(from_wallet_id, to_wallet_id)) first
Lock(max(from_wallet_id, to_wallet_id)) second
```

This single rule eliminates all deadlock scenarios. Two concurrent transfers between the same pair of wallets always compete for the same lock in the same order, so one blocks and waits rather than forming a deadlock cycle.

---

## How to Run

```powershell
# 1. Start PostgreSQL (schema and seed wallets applied automatically)
docker compose up -d postgres postgres_test

# 2. Create environment file
Copy-Item .env.example .env

# 3. Download dependencies
go mod tidy

# 4. Start API server (http://localhost:8080)
go run ./cmd/server
```

---

## How to Test

**Automated tests** (requires `postgres_test` container on port 5433):
```powershell
go test ./tests/... -v -count=1 -timeout 60s
```

**Manual testing via Postman:**
1. Open Postman → Import → select `postman/wallet-transfer-service.postman_collection.json`
2. `baseUrl` is pre-set to `http://localhost:8080`
3. Run the collection — each request includes assertion scripts

Seed wallets available out of the box: `wallet_1` (1000), `wallet_2` (500), `wallet_3` (250).

---

## Tradeoffs / Assumptions

| Decision | Rationale |
|---|---|
| `float64` for amounts (not `decimal`) | Acceptable for this scope; stored as `NUMERIC(20,8)` in Postgres so no precision is lost in persistence. A production system should use `shopspring/decimal`. |
| PENDING state never externally observable | In a single-transaction synchronous model, PENDING only exists inside the transaction boundary. No external observer sees it before commit. Documented in code. |
| FAILED transfers not persisted | Avoids locking the idempotency key on transient failures, allowing safe retry with the same key. Audit of failed attempts would require a separate `transfer_attempts` table (out of scope). |
| Pessimistic over optimistic locking | Chosen for correctness clarity at the cost of throughput under high contention on hot wallets. Optimistic locking would require retry loops which are harder to test and reason about. |
| UUIDs as string IDs (not `uuid` type) | Keeps the schema portable (works on SQLite too) and avoids pgx UUID mapping complexity. No functional impact. |

---

## Checklist

- [x] Tests pass
- [x] Lint passes (`go vet ./...`)
- [x] Format check passes (`go build ./...`)
- [x] README updated with run and test instructions
- [x] PR description explains schema, idempotency, and concurrency
