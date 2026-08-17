package repository

import (
	"context"
	"fmt"

	"wallet-transfer/internal/domain"
)

type LedgerRepository struct{}

func NewLedgerRepository() *LedgerRepository {
	return &LedgerRepository{}
}

// InsertEntry persists a single ledger entry. Must be called within an active transaction.
func (r *LedgerRepository) InsertEntry(ctx context.Context, tx DBTX, entry *domain.LedgerEntry) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO ledger_entries (id, transfer_id, wallet_id, type, amount, created_at)
		 VALUES ($1, $2, $3, $4, $5, NOW())`,
		entry.ID, entry.TransferID, entry.WalletID, string(entry.Type), entry.Amount,
	)
	if err != nil {
		return fmt.Errorf("insert ledger entry: %w", err)
	}
	return nil
}
