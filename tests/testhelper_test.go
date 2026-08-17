package tests

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestPool connects to the test database. If the DB is unreachable the test
// is skipped rather than failed — the suite requires docker-compose up.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		testEnv("DB_HOST", "localhost"),
		testEnv("DB_PORT", "5433"),
		testEnv("DB_USER", "postgres"),
		testEnv("DB_PASSWORD", "postgres"),
		testEnv("DB_NAME", "wallet_transfer_test"),
	)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("test database not available (run docker-compose up first): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// cleanupTables truncates all tables to give each test a clean slate.
func cleanupTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`TRUNCATE idempotency_records, ledger_entries, transfers, wallets RESTART IDENTITY CASCADE`,
	)
	if err != nil {
		t.Fatalf("cleanup tables: %v", err)
	}
}

// seedWallet inserts or resets a wallet with a given balance.
func seedWallet(t *testing.T, pool *pgxpool.Pool, id string, balance float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO wallets (id, balance) VALUES ($1, $2)
		 ON CONFLICT (id) DO UPDATE SET balance = EXCLUDED.balance, updated_at = NOW()`,
		id, balance,
	)
	if err != nil {
		t.Fatalf("seed wallet %s: %v", id, err)
	}
}

func testEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
