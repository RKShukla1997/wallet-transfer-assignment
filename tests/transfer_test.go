package tests

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-transfer/internal/domain"
	"wallet-transfer/internal/repository"
	"wallet-transfer/internal/service"
)

// buildService wires up all dependencies against the provided pool.
func buildService(pool *pgxpool.Pool) *service.TransferService {
	return service.NewTransferService(
		pool,
		repository.NewWalletRepository(),
		repository.NewTransferRepository(),
		repository.NewLedgerRepository(),
		repository.NewIdempotencyRepository(),
	)
}

// ---------------------------------------------------------------------------
// Success path
// ---------------------------------------------------------------------------

func TestCreateTransfer_Success(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)
	seedWallet(t, pool, "w2", 500)

	svc := buildService(pool)
	resp, err := svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
		IdempotencyKey: "key-success",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         200,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != domain.StatusProcessed {
		t.Errorf("expected status PROCESSED, got %s", resp.Status)
	}
	if resp.TransferID == "" {
		t.Error("expected non-empty transfer ID")
	}

	wr := repository.NewWalletRepository()
	w1, _ := wr.FindByID(context.Background(), pool, "w1")
	w2, _ := wr.FindByID(context.Background(), pool, "w2")

	if w1.Balance != 800 {
		t.Errorf("w1 balance: want 800, got %f", w1.Balance)
	}
	if w2.Balance != 700 {
		t.Errorf("w2 balance: want 700, got %f", w2.Balance)
	}
}

// ---------------------------------------------------------------------------
// Idempotency: duplicate request must return the original result unchanged
// ---------------------------------------------------------------------------

func TestCreateTransfer_Idempotency_ReturnsSameResult(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)
	seedWallet(t, pool, "w2", 500)

	svc := buildService(pool)
	req := service.CreateTransferRequest{
		IdempotencyKey: "key-idempotent",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         100,
	}

	resp1, err := svc.CreateTransfer(context.Background(), req)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	resp2, err := svc.CreateTransfer(context.Background(), req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if resp1.TransferID != resp2.TransferID {
		t.Errorf("duplicate request returned different transfer IDs: %s vs %s", resp1.TransferID, resp2.TransferID)
	}
	if resp1.Status != resp2.Status {
		t.Errorf("duplicate request returned different statuses")
	}
}

func TestCreateTransfer_Idempotency_DebitsOnlyOnce(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)
	seedWallet(t, pool, "w2", 500)

	svc := buildService(pool)
	req := service.CreateTransferRequest{
		IdempotencyKey: "key-debit-once",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         150,
	}

	for i := 0; i < 5; i++ {
		if _, err := svc.CreateTransfer(context.Background(), req); err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
	}

	wr := repository.NewWalletRepository()
	w1, _ := wr.FindByID(context.Background(), pool, "w1")
	if w1.Balance != 850 {
		t.Errorf("w1 balance: want 850 (debited once only), got %f", w1.Balance)
	}
}

// ---------------------------------------------------------------------------
// Validation and domain errors
// ---------------------------------------------------------------------------

func TestCreateTransfer_InsufficientFunds(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 50)
	seedWallet(t, pool, "w2", 500)

	svc := buildService(pool)
	_, err := svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
		IdempotencyKey: "key-insuf",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         200,
	})

	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Errorf("want ErrInsufficientFunds, got: %v", err)
	}
}

func TestCreateTransfer_WalletNotFound(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)

	svc := buildService(pool)
	_, err := svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
		IdempotencyKey: "key-notfound",
		FromWalletID:   "w1",
		ToWalletID:     "does-not-exist",
		Amount:         100,
	})

	if !errors.Is(err, domain.ErrWalletNotFound) {
		t.Errorf("want ErrWalletNotFound, got: %v", err)
	}
}

func TestCreateTransfer_SameWallet(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)

	svc := buildService(pool)
	_, err := svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
		IdempotencyKey: "key-same",
		FromWalletID:   "w1",
		ToWalletID:     "w1",
		Amount:         100,
	})

	if !errors.Is(err, domain.ErrSameWallet) {
		t.Errorf("want ErrSameWallet, got: %v", err)
	}
}

func TestCreateTransfer_ZeroAmount(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)
	seedWallet(t, pool, "w2", 500)

	svc := buildService(pool)
	_, err := svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
		IdempotencyKey: "key-zero",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         0,
	})

	if !errors.Is(err, domain.ErrInvalidAmount) {
		t.Errorf("want ErrInvalidAmount, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Ledger correctness
// ---------------------------------------------------------------------------

func TestCreateTransfer_LedgerHasExactlyTwoEntries(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)
	seedWallet(t, pool, "w2", 500)

	svc := buildService(pool)
	resp, err := svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
		IdempotencyKey: "key-ledger",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         300,
	})
	if err != nil {
		t.Fatalf("transfer failed: %v", err)
	}

	rows, err := pool.Query(context.Background(),
		`SELECT type, amount, wallet_id FROM ledger_entries WHERE transfer_id = $1 ORDER BY type`,
		resp.TransferID,
	)
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer rows.Close()

	type entry struct {
		entryType string
		amount    float64
		walletID  string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.entryType, &e.amount, &e.walletID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		entries = append(entries, e)
	}

	if len(entries) != 2 {
		t.Fatalf("want exactly 2 ledger entries, got %d", len(entries))
	}

	var debit, credit entry
	for _, e := range entries {
		switch e.entryType {
		case "DEBIT":
			debit = e
		case "CREDIT":
			credit = e
		}
	}

	if debit.walletID != "w1" {
		t.Errorf("DEBIT wallet: want w1, got %s", debit.walletID)
	}
	if credit.walletID != "w2" {
		t.Errorf("CREDIT wallet: want w2, got %s", credit.walletID)
	}
	if debit.amount != credit.amount {
		t.Errorf("ledger unbalanced: DEBIT %f != CREDIT %f", debit.amount, credit.amount)
	}
	if debit.amount != 300 {
		t.Errorf("ledger amount: want 300, got %f", debit.amount)
	}
}

// ---------------------------------------------------------------------------
// Concurrency: N goroutines debiting the same wallet simultaneously
// ---------------------------------------------------------------------------

func TestCreateTransfer_ConcurrentDebits_CorrectBalance(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 1000)
	seedWallet(t, pool, "w2", 0)

	svc := buildService(pool)

	const workers = 10
	const amount = 150.0 // 10 * 150 = 1500, only 6 can succeed (6*150=900 ≤ 1000)

	var wg sync.WaitGroup
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.CreateTransfer(context.Background(), service.CreateTransferRequest{
				IdempotencyKey: fmt.Sprintf("concurrent-%d", i),
				FromWalletID:   "w1",
				ToWalletID:     "w2",
				Amount:         amount,
			})
		}(i)
	}
	wg.Wait()

	var successCount int
	for _, err := range errs {
		if err == nil {
			successCount++
		} else if !errors.Is(err, domain.ErrInsufficientFunds) {
			t.Errorf("unexpected error: %v", err)
		}
	}

	wr := repository.NewWalletRepository()
	w1, _ := wr.FindByID(context.Background(), pool, "w1")
	w2, _ := wr.FindByID(context.Background(), pool, "w2")

	wantDebited := float64(successCount) * amount
	if w1.Balance != 1000-wantDebited {
		t.Errorf("w1 balance: want %.2f, got %.2f", 1000-wantDebited, w1.Balance)
	}
	if w2.Balance != wantDebited {
		t.Errorf("w2 balance: want %.2f, got %.2f", wantDebited, w2.Balance)
	}
	if w1.Balance < 0 {
		t.Error("w1 went negative: double-spend occurred")
	}
}

func TestCreateTransfer_ConcurrentDebits_LedgerBalances(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 500)
	seedWallet(t, pool, "w2", 0)

	svc := buildService(pool)

	const workers = 5
	const amount = 100.0

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc.CreateTransfer(context.Background(), service.CreateTransferRequest{ //nolint:errcheck
				IdempotencyKey: fmt.Sprintf("ledger-concurrent-%d", i),
				FromWalletID:   "w1",
				ToWalletID:     "w2",
				Amount:         amount,
			})
		}(i)
	}
	wg.Wait()

	// Every successful transfer must have produced a balanced pair.
	row := pool.QueryRow(context.Background(), `
		SELECT
			SUM(CASE WHEN type = 'DEBIT'  THEN amount ELSE 0 END) as total_debit,
			SUM(CASE WHEN type = 'CREDIT' THEN amount ELSE 0 END) as total_credit
		FROM ledger_entries`,
	)
	var totalDebit, totalCredit float64
	if err := row.Scan(&totalDebit, &totalCredit); err != nil {
		t.Fatalf("ledger aggregate query: %v", err)
	}
	if totalDebit != totalCredit {
		t.Errorf("ledger out of balance: total_debit=%.2f total_credit=%.2f", totalDebit, totalCredit)
	}
}

// ---------------------------------------------------------------------------
// State machine: FAILED state and idempotency key reuse
// ---------------------------------------------------------------------------

// TestCreateTransfer_FailedTransferDoesNotConsumeIdempotencyKey verifies that a
// failed transfer (insufficient funds) does NOT consume the idempotency key, so
// the caller can retry with the exact same key once the underlying issue is fixed.
func TestCreateTransfer_FailedTransferDoesNotConsumeIdempotencyKey(t *testing.T) {
	pool := newTestPool(t)
	cleanupTables(t, pool)
	seedWallet(t, pool, "w1", 50)
	seedWallet(t, pool, "w2", 0)

	svc := buildService(pool)
	req := service.CreateTransferRequest{
		IdempotencyKey: "key-retry-after-failure",
		FromWalletID:   "w1",
		ToWalletID:     "w2",
		Amount:         200,
	}

	// First attempt — should fail due to insufficient funds.
	_, err := svc.CreateTransfer(context.Background(), req)
	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds on first attempt, got: %v", err)
	}

	// Top up the wallet.
	seedWallet(t, pool, "w1", 500)

	// Retry with the SAME idempotency key — should now succeed because the key
	// was not consumed by the previous failure.
	resp, err := svc.CreateTransfer(context.Background(), req)
	if err != nil {
		t.Fatalf("expected retry to succeed after top-up, got: %v", err)
	}
	if resp.Status != domain.StatusProcessed {
		t.Errorf("expected PROCESSED after successful retry, got %s", resp.Status)
	}

	// Verify the balance reflects exactly one debit.
	wr := repository.NewWalletRepository()
	w1, _ := wr.FindByID(context.Background(), pool, "w1")
	if w1.Balance != 300 {
		t.Errorf("expected w1 balance 300 after retry, got %f", w1.Balance)
	}
}

// TestCreateTransfer_StateTransitionGuard verifies the domain model rejects
// invalid state transitions (e.g. terminal state PROCESSED cannot move to FAILED).
func TestCreateTransfer_StateTransitionGuard(t *testing.T) {
	transfer := &domain.Transfer{Status: domain.StatusProcessed}

	if err := transfer.ValidateTransition(domain.StatusFailed); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition for PROCESSED->FAILED, got: %v", err)
	}
	if err := transfer.ValidateTransition(domain.StatusPending); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition for PROCESSED->PENDING, got: %v", err)
	}

	// PENDING → PROCESSED must be allowed.
	pending := &domain.Transfer{Status: domain.StatusPending}
	if err := pending.ValidateTransition(domain.StatusProcessed); err != nil {
		t.Errorf("expected PENDING->PROCESSED to be valid, got: %v", err)
	}
	if err := pending.ValidateTransition(domain.StatusFailed); err != nil {
		t.Errorf("expected PENDING->FAILED to be valid, got: %v", err)
	}
}
