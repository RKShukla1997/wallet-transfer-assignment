package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"wallet-transfer/internal/domain"
)

type WalletRepository struct{}

func NewWalletRepository() *WalletRepository {
	return &WalletRepository{}
}

// FindByIDForUpdate fetches a wallet and acquires a row-level lock (SELECT FOR UPDATE).
// Must be called within an active transaction.
func (r *WalletRepository) FindByIDForUpdate(ctx context.Context, tx DBTX, id string) (*domain.Wallet, error) {
	w := &domain.Wallet{}
	err := tx.QueryRow(ctx,
		`SELECT id, balance, created_at, updated_at FROM wallets WHERE id = $1 FOR UPDATE`,
		id,
	).Scan(&w.ID, &w.Balance, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, fmt.Errorf("FindByIDForUpdate: %w", err)
	}
	return w, nil
}

// UpdateBalance sets a wallet's balance. Must be called within an active transaction.
func (r *WalletRepository) UpdateBalance(ctx context.Context, tx DBTX, id string, newBalance float64) error {
	tag, err := tx.Exec(ctx,
		`UPDATE wallets SET balance = $1, updated_at = NOW() WHERE id = $2`,
		newBalance, id,
	)
	if err != nil {
		return fmt.Errorf("UpdateBalance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrWalletNotFound
	}
	return nil
}

// FindByID fetches a wallet without a lock. Safe for read-only queries.
func (r *WalletRepository) FindByID(ctx context.Context, db DBTX, id string) (*domain.Wallet, error) {
	w := &domain.Wallet{}
	err := db.QueryRow(ctx,
		`SELECT id, balance, created_at, updated_at FROM wallets WHERE id = $1`,
		id,
	).Scan(&w.ID, &w.Balance, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrWalletNotFound
		}
		return nil, fmt.Errorf("FindByID: %w", err)
	}
	return w, nil
}
