# Wallet Transfer Service — Solution Design

## Overview

This document describes the chosen architecture for the wallet transfer service — **Pessimistic Locking with a Dedicated Idempotency Table** — covering system design, data model, request flow, and the rationale behind key decisions.

---

## High-Level Architecture — Approach 1 (Recommended)

### System Layers

```mermaid
flowchart TD
    Client(["Client / API Consumer"])

    subgraph API["Handler Layer"]
        H["TransferHandler\n─────────────\n• Validate request shape\n• Parse idempotencyKey\n• Map HTTP ↔ service DTOs"]
    end

    subgraph SVC["Service Layer"]
        S["TransferService\n─────────────\n• Check idempotency_records\n• Enforce business rules\n• Orchestrate transfer workflow\n• Own transaction boundary"]
    end

    subgraph REPO["Repository Layer"]
        R1["WalletRepository\n─────────────\n• findByIdForUpdate (FOR UPDATE)\n• updateBalance"]
        R2["TransferRepository\n─────────────\n• insert\n• findById"]
        R3["LedgerRepository\n─────────────\n• insertEntry (DEBIT / CREDIT)"]
        R4["IdempotencyRepository\n─────────────\n• findByKey\n• insert (UNIQUE constraint)"]
    end

    subgraph DB["PostgreSQL"]
        W[("wallets")]
        T[("transfers")]
        L[("ledger_entries")]
        I[("idempotency_records")]
    end

    Client -->|"POST /transfers"| H
    H --> S
    S --> R1
    S --> R2
    S --> R3
    S --> R4
    R1 <--> W
    R2 <--> T
    R3 <--> L
    R4 <--> I
    H -->|"HTTP 200 / 409 / 422"| Client
```

---

### Transfer Request Flow

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant H as Handler
    participant S as TransferService
    participant IR as IdempotencyRepo
    participant WR as WalletRepo
    participant TR as TransferRepo
    participant LR as LedgerRepo
    participant DB as PostgreSQL

    C->>H: POST /transfers {idempotencyKey, from, to, amount}
    H->>H: Validate request shape

    H->>S: createTransfer(dto)

    S->>IR: findByKey(idempotencyKey)
    alt Duplicate request
        IR-->>S: existing record found
        S-->>H: return cached response
        H-->>C: 200 OK (original result)
    else New request
        IR-->>S: not found

        S->>DB: BEGIN TRANSACTION

        S->>IR: insert(idempotencyKey) [UNIQUE — fail fast on race]

        Note over S,WR: Lock wallets in ascending ID order to prevent deadlocks
        S->>WR: SELECT FOR UPDATE (lower wallet ID)
        S->>WR: SELECT FOR UPDATE (higher wallet ID)

        S->>S: Assert source balance >= amount

        alt Insufficient balance
            S->>DB: ROLLBACK
            S-->>H: InsufficientFundsError
            H-->>C: 422 Unprocessable Entity
        else Balance OK
            S->>WR: UPDATE balance -= amount (source)
            S->>WR: UPDATE balance += amount (destination)
            S->>TR: INSERT transfer (status = PROCESSED)
            S->>LR: INSERT ledger_entry (DEBIT, source wallet)
            S->>LR: INSERT ledger_entry (CREDIT, destination wallet)
            S->>DB: COMMIT

            S-->>H: TransferResult
            H-->>C: 200 OK {transferId, status: PROCESSED}
        end
    end
```

---

### Database Schema

```mermaid
erDiagram
    wallets {
        uuid id PK
        decimal balance
        timestamp created_at
        timestamp updated_at
    }

    transfers {
        uuid id PK
        varchar idempotency_key UK
        uuid from_wallet_id FK
        uuid to_wallet_id FK
        decimal amount
        varchar status
        timestamp created_at
    }

    ledger_entries {
        uuid id PK
        uuid transfer_id FK
        uuid wallet_id FK
        varchar type
        decimal amount
        timestamp created_at
    }

    idempotency_records {
        varchar idempotency_key PK
        uuid transfer_id FK
        jsonb response_snapshot
        timestamp created_at
    }

    wallets ||--o{ transfers : "from_wallet_id"
    wallets ||--o{ transfers : "to_wallet_id"
    transfers ||--|| idempotency_records : "transfer_id"
    transfers ||--|{ ledger_entries : "transfer_id"
    wallets ||--o{ ledger_entries : "wallet_id"
```

---

### Atomic Transaction Boundary

```mermaid
flowchart LR
    subgraph TX["Single Database Transaction"]
        direction TB
        A["1. INSERT idempotency_records\n   ← UNIQUE guard against races"]
        B["2. SELECT wallets FOR UPDATE\n   ← serialize concurrent debits"]
        C["3. Validate balance"]
        D["4. UPDATE wallet balances"]
        E["5. INSERT transfer record\n   status = PROCESSED"]
        F["6. INSERT ledger_entry DEBIT\n   source wallet"]
        G["7. INSERT ledger_entry CREDIT\n   destination wallet"]

        A --> B --> C --> D --> E --> F --> G
    end

    FAIL(["Any step fails\n→ full ROLLBACK\n→ no partial state"])
    OK(["All steps pass\n→ COMMIT\n→ atomic, consistent"])

    TX -- failure --> FAIL
    TX -- success --> OK
```

---

## Design Decisions

### Why Pessimistic Locking

For a wallet transfer service evaluated on **correctness, transactional safety, and idempotency**, Approach 1 is the strongest choice:

1. **Correctness is easy to verify.** Row-level locking makes the execution path deterministic and serial for any pair of wallets. There are no retry loops or conflict windows to reason about.

2. **Idempotency is airtight.** The combination of an application-level check and a `UNIQUE` DB constraint on `idempotency_key` handles all race conditions — including two concurrent identical requests hitting the server simultaneously.

3. **Matches the evaluation criteria directly.** The assignment explicitly asks you to choose and justify a lock strategy. Pessimistic locking is the clearest answer to "safe under concurrent debits on the same wallet."

4. **Deadlock prevention is mechanical.** Always acquire wallet locks in ascending ID order — this is a one-liner in the repository layer and completely prevents deadlocks.

5. **Scope-appropriate.** Optimistic locking adds retry complexity that is hard to test correctly. Event sourcing adds query complexity with no benefit at this scale.

### Lock Ordering Rule (Critical)

```text
Always lock wallets in ascending wallet_id order.

Lock(min(from_wallet_id, to_wallet_id)) first
Lock(max(from_wallet_id, to_wallet_id)) second
```

This single rule eliminates all deadlock scenarios regardless of concurrent request ordering.

### Transaction Boundary (Single Atomic Unit)

```text
BEGIN TRANSACTION
  1. INSERT idempotency_records (idempotency_key) — fail fast if duplicate
  2. SELECT ... FOR UPDATE on source wallet
  3. SELECT ... FOR UPDATE on destination wallet
  4. Validate source wallet balance >= amount
  5. UPDATE wallets SET balance = balance - amount WHERE id = source
  6. UPDATE wallets SET balance = balance + amount WHERE id = destination
  7. INSERT transfers (status = PROCESSED)
  8. INSERT ledger_entries (DEBIT for source)
  9. INSERT ledger_entries (CREDIT for destination)
COMMIT
```

If any step fails, the entire transaction rolls back — no partial state, no orphaned ledger entries, no inconsistent balances.

---

## Tech Stack Suggestion

| Component | Choice | Reason |
|---|---|---|
| Language | Node.js (TypeScript) or Go | Clean layering is natural in both |
| Database | PostgreSQL | Native `FOR UPDATE`, strong transaction support |
| ORM/Query | Knex.js / `pgx` (Go) / Prisma | Explicit transaction control without magic |
| Testing | Jest / Go test | Behavioral tests against a real DB (not mocks) |

> **Note on testing:** Use a real PostgreSQL instance (or SQLite for simplicity) in tests — not mocked repositories. Idempotency and concurrency correctness cannot be meaningfully tested against mocks.
