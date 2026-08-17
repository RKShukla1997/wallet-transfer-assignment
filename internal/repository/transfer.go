package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"wallet-transfer/internal/domain"
)

type TransferRepository struct{}

func NewTransferRepository() *TransferRepository {
	return &TransferRepository{}
}

// Insert persists a new transfer record. Must be called within an active transaction.
func (r *TransferRepository) Insert(ctx context.Context, tx DBTX, t *domain.Transfer) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO transfers (id, idempotency_key, from_wallet_id, to_wallet_id, amount, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW())`,
		t.ID, t.IdempotencyKey, t.FromWalletID, t.ToWalletID, t.Amount, string(t.Status),
	)
	if err != nil {
		return fmt.Errorf("insert transfer: %w", err)
	}
	return nil
}

// FindByID fetches a transfer by its primary key.
func (r *TransferRepository) FindByID(ctx context.Context, db DBTX, id string) (*domain.Transfer, error) {
	t := &domain.Transfer{}
	var status string
	err := db.QueryRow(ctx,
		`SELECT id, idempotency_key, from_wallet_id, to_wallet_id, amount, status, created_at
		 FROM transfers WHERE id = $1`,
		id,
	).Scan(&t.ID, &t.IdempotencyKey, &t.FromWalletID, &t.ToWalletID, &t.Amount, &status, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTransferNotFound
		}
		return nil, fmt.Errorf("FindByID transfer: %w", err)
	}
	t.Status = domain.TransferStatus(status)
	return t, nil
}
