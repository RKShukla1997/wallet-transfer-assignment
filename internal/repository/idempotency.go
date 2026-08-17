package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// IdempotencyRecord holds a stored idempotency key with its associated response snapshot.
type IdempotencyRecord struct {
	IdempotencyKey   string
	TransferID       string
	ResponseSnapshot json.RawMessage
}

type IdempotencyRepository struct{}

func NewIdempotencyRepository() *IdempotencyRepository {
	return &IdempotencyRepository{}
}

// FindByKey looks up an existing idempotency record by key.
// Returns nil, nil when the key does not exist (not an error condition).
func (r *IdempotencyRepository) FindByKey(ctx context.Context, db DBTX, key string) (*IdempotencyRecord, error) {
	var rec IdempotencyRecord
	var snapshot []byte
	err := db.QueryRow(ctx,
		`SELECT idempotency_key, transfer_id, response_snapshot
		 FROM idempotency_records WHERE idempotency_key = $1`,
		key,
	).Scan(&rec.IdempotencyKey, &rec.TransferID, &snapshot)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("FindByKey: %w", err)
	}
	rec.ResponseSnapshot = snapshot
	return &rec, nil
}

// Insert stores an idempotency record with the serialised response snapshot.
// Must be called within an active transaction. The UNIQUE constraint on
// idempotency_key acts as the final safety net against concurrent duplicates.
func (r *IdempotencyRepository) Insert(ctx context.Context, tx DBTX, key, transferID string, snapshot any) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO idempotency_records (idempotency_key, transfer_id, response_snapshot, created_at)
		 VALUES ($1, $2, $3, NOW())`,
		key, transferID, raw,
	)
	if err != nil {
		return fmt.Errorf("insert idempotency record: %w", err)
	}
	return nil
}
