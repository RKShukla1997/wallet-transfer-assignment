# Wallet Transfer Assignment Repository

This repository is a reusable coding assignment template for evaluating backend engineers on wallet transfers, idempotency, concurrency control, and double-entry ledger design.

## Included

- `ASSIGNMENT.md` - candidate-facing prompt
- `.github/pull_request_template.md` - required PR structure
- `.github/workflows/ci.yml` - lint, format, test placeholder workflow
- `.github/workflows/sonarqube.yml` - SonarQube pull request analysis
- `.github/copilot-instructions.md` - repository-level Copilot review guidance
- `evaluation_guide.md` - reviewer rubric
- `branch-protection-checklist.md` - GitHub setup checklist

## Intended use

1. Mark this repository as a GitHub template repository.
2. Create one private repository per candidate from the template.
3. Add the candidate as a collaborator.
4. Ask them to submit via a pull request into `main`.
5. Enable required checks, SonarQube, and Copilot review in GitHub.

## Notes

- Copilot automatic pull request review is configured in GitHub repository or organization settings, not purely through files in the repo.
- The `copilot-instructions.md` file included here provides repository-specific review guidance once Copilot review is enabled.
- The CI workflow is language-agnostic by default and expects you to set the `LINT_CMD`, `FORMAT_CHECK_CMD`, and `TEST_CMD` repository variables or replace the commands directly.

## How to Submit Assignment

1. **Fork this repository** to your own GitHub account.
2. Complete the assignment described in [`ASSIGNMENT.md`](./ASSIGNMENT.md).
3. **Raise a Pull Request** back to this repository (`main` branch) with your full solution.

Your PR branch should be named: `solution/<your-name>` (e.g., `solution/jane-doe`).

## Running the Solution

### Prerequisites

- [Go 1.22+](https://go.dev/dl/)
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (for PostgreSQL)

---

### Step 1 — Start PostgreSQL

```powershell
docker compose up -d postgres postgres_test
```

This starts two containers:
- `postgres` on port **5432** — used by the API server
- `postgres_test` on port **5433** — used by tests

Both containers auto-run [migrations/001_init.sql](migrations/001_init.sql) on first start, which creates the schema and seeds three wallets (`wallet_1`, `wallet_2`, `wallet_3`).

---

### Step 2 — Create environment file

```powershell
Copy-Item .env.example .env
```

The default values in `.env.example` match the Docker Compose config — no edits needed for local use.

---

### Step 3 — Download dependencies

```powershell
go mod tidy
```

---

### Step 4 — Start the API server

```powershell
go run ./cmd/server
```

The server starts on `http://localhost:8080`. You should see:

```
server listening on :8080
```

---

### Step 5 — Test with Postman

1. Open Postman
2. Click **Import**
3. Select [postman/wallet-transfer-service.postman_collection.json](postman/wallet-transfer-service.postman_collection.json)
4. The collection loads with `baseUrl` already set to `http://localhost:8080`

Run requests in this recommended order:

| Request | Expected |
|---|---|
| Wallets / Get Wallet 1 | 200 — wallet_1 with balance 1000 |
| Transfers / Create Transfer - Success | 200 — status PROCESSED |
| Wallets / Get Wallet 1 (again) | 200 — balance reduced by 100 |
| Transfers / Create Transfer - Idempotency Replay | 200 — same transferId as first call |
| Transfers / Create Transfer - Insufficient Funds | 422 |
| Transfers / Create Transfer - Same Wallet | 422 |
| Transfers / Create Transfer - Invalid Amount | 422 |
| Transfers / Create Transfer - Wallet Not Found | 404 |
| Wallets / Get Wallet Not Found | 404 |

Each request includes a Postman test script — open the **Tests** tab in Postman and run the full collection via **Run collection** to see all assertions pass.

---

### Step 6 — Run automated tests

Tests run against the isolated `postgres_test` container (port 5433).

```powershell
go test ./tests/... -v -count=1 -timeout 60s
```

---

### Step 7 — Stop containers

```powershell
docker compose down
```
