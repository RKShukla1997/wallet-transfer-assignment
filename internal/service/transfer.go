package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-transfer/internal/domain"
	"wallet-transfer/internal/repository"
)

// CreateTransferRequest is the input DTO for the transfer use-case.
type CreateTransferRequest struct {
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         float64
}

// TransferResponse is the output DTO returned to callers and stored as the idempotency snapshot.
type TransferResponse struct {
	TransferID   string                `json:"transferId"`
	Status       domain.TransferStatus `json:"status"`
	FromWalletID string                `json:"fromWalletId"`
	ToWalletID   string                `json:"toWalletId"`
	Amount       float64               `json:"amount"`
}

// TransferService owns the transfer workflow and the transaction boundary.
type TransferService struct {
	pool            *pgxpool.Pool
	walletRepo      *repository.WalletRepository
	transferRepo    *repository.TransferRepository
	ledgerRepo      *repository.LedgerRepository
	idempotencyRepo *repository.IdempotencyRepository
}

func NewTransferService(
	pool *pgxpool.Pool,
	walletRepo *repository.WalletRepository,
	transferRepo *repository.TransferRepository,
	ledgerRepo *repository.LedgerRepository,
	idempotencyRepo *repository.IdempotencyRepository,
) *TransferService {
	return &TransferService{
		pool:            pool,
		walletRepo:      walletRepo,
		transferRepo:    transferRepo,
		ledgerRepo:      ledgerRepo,
		idempotencyRepo: idempotencyRepo,
	}
}

// CreateTransfer executes a wallet transfer with the following guarantees:
//   - Idempotent: same idempotencyKey always returns the original result.
//   - Atomic: all DB writes (balances, transfer, ledger, idempotency) are in one transaction.
//   - Deadlock-safe: wallet locks are acquired in ascending ID order.
//   - Concurrent-safe: SELECT FOR UPDATE serialises concurrent debits on the same wallet.
//
// State machine notes:
//   - PENDING is the logical start state but is never externally observable.
//     Since the entire workflow is a single atomic transaction, committed
//     transfers are always PROCESSED. No observer ever sees PENDING.
//   - FAILED transfers are NOT persisted. Business-rule failures (insufficient
//     funds, wallet not found) roll back the transaction entirely — no transfer
//     record is written and the idempotency key is NOT consumed. The caller may
//     retry with the same key once the underlying issue is resolved.
func (s *TransferService) CreateTransfer(ctx context.Context, req CreateTransferRequest) (*TransferResponse, error) {
	if req.FromWalletID == req.ToWalletID {
		return nil, domain.ErrSameWallet
	}
	if req.Amount <= 0 {
		return nil, domain.ErrInvalidAmount
	}

	// Fast path: return cached response for already-processed idempotency keys.
	existing, err := s.idempotencyRepo.FindByKey(ctx, s.pool, req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}
	if existing != nil {
		return unmarshalResponse(existing.ResponseSnapshot)
	}

	// --- Single atomic transaction ---
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck — safe to call on an already-committed tx

	// Acquire row-level locks in ascending wallet ID order to prevent deadlocks.
	// Two concurrent transfers involving the same wallets will serialize here.
	firstID, secondID := req.FromWalletID, req.ToWalletID
	if firstID > secondID {
		firstID, secondID = secondID, firstID
	}

	first, err := s.walletRepo.FindByIDForUpdate(ctx, tx, firstID)
	if err != nil {
		return nil, err
	}
	second, err := s.walletRepo.FindByIDForUpdate(ctx, tx, secondID)
	if err != nil {
		return nil, err
	}

	// Map the locked rows back to semantic from/to roles.
	fromWallet, toWallet := first, second
	if first.ID != req.FromWalletID {
		fromWallet, toWallet = second, first
	}

	if fromWallet.Balance < req.Amount {
		return nil, domain.ErrInsufficientFunds
	}

	// Update balances.
	if err := s.walletRepo.UpdateBalance(ctx, tx, fromWallet.ID, fromWallet.Balance-req.Amount); err != nil {
		return nil, fmt.Errorf("debit wallet: %w", err)
	}
	if err := s.walletRepo.UpdateBalance(ctx, tx, toWallet.ID, toWallet.Balance+req.Amount); err != nil {
		return nil, fmt.Errorf("credit wallet: %w", err)
	}

	// Insert transfer record.
	// The UNIQUE constraint on transfers.idempotency_key is the safety net for any
	// concurrent duplicate that slipped past the pre-tx check.
	transferID := uuid.New().String()
	transfer := &domain.Transfer{
		ID:             transferID,
		IdempotencyKey: req.IdempotencyKey,
		FromWalletID:   req.FromWalletID,
		ToWalletID:     req.ToWalletID,
		Amount:         req.Amount,
		Status:         domain.StatusPending, // logical start; updated to PROCESSED before commit
	}
	// Guard the PENDING → PROCESSED transition via the domain state machine.
	if err := transfer.ValidateTransition(domain.StatusProcessed); err != nil {
		return nil, fmt.Errorf("invalid state transition: %w", err)
	}
	transfer.Status = domain.StatusProcessed
	if err := s.transferRepo.Insert(ctx, tx, transfer); err != nil {
		if isUniqueViolation(err) {
			// A concurrent duplicate won the race. Roll back and return its cached result.
			_ = tx.Rollback(ctx)
			rec, fetchErr := s.idempotencyRepo.FindByKey(ctx, s.pool, req.IdempotencyKey)
			if fetchErr != nil {
				return nil, fetchErr
			}
			if rec != nil {
				return unmarshalResponse(rec.ResponseSnapshot)
			}
		}
		return nil, fmt.Errorf("insert transfer: %w", err)
	}

	// Double-entry ledger: exactly one DEBIT and one CREDIT per transfer.
	debit := &domain.LedgerEntry{
		ID:         uuid.New().String(),
		TransferID: transferID,
		WalletID:   req.FromWalletID,
		Type:       domain.EntryDebit,
		Amount:     req.Amount,
	}
	credit := &domain.LedgerEntry{
		ID:         uuid.New().String(),
		TransferID: transferID,
		WalletID:   req.ToWalletID,
		Type:       domain.EntryCredit,
		Amount:     req.Amount,
	}
	if err := s.ledgerRepo.InsertEntry(ctx, tx, debit); err != nil {
		return nil, fmt.Errorf("insert debit entry: %w", err)
	}
	if err := s.ledgerRepo.InsertEntry(ctx, tx, credit); err != nil {
		return nil, fmt.Errorf("insert credit entry: %w", err)
	}

	// Persist the idempotency record with the full response snapshot so future
	// duplicate requests can return the identical result without re-executing.
	resp := &TransferResponse{
		TransferID:   transferID,
		Status:       domain.StatusProcessed,
		FromWalletID: req.FromWalletID,
		ToWalletID:   req.ToWalletID,
		Amount:       req.Amount,
	}
	if err := s.idempotencyRepo.Insert(ctx, tx, req.IdempotencyKey, transferID, resp); err != nil {
		return nil, fmt.Errorf("insert idempotency record: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return resp, nil
}

func unmarshalResponse(data json.RawMessage) (*TransferResponse, error) {
	var resp TransferResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal cached response: %w", err)
	}
	return &resp, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
