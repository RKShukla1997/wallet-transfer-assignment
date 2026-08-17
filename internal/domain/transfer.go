package domain

import (
	"errors"
	"time"
)

// ErrInvalidTransition is returned when a state change violates the transfer state machine.
var ErrInvalidTransition = errors.New("invalid transfer state transition")

type TransferStatus string

const (
	// StatusPending is the initial state of a transfer. In our single-transaction
	// model the PENDING state exists only for the duration of the database transaction
	// and is never externally observable — committed transfers are always PROCESSED.
	// PENDING is preserved as a valid DB value for async or two-phase implementations.
	StatusPending   TransferStatus = "PENDING"
	StatusProcessed TransferStatus = "PROCESSED"
	// StatusFailed is a terminal state. Business-rule failures (e.g. insufficient
	// funds) result in a transaction rollback — no transfer record is written —
	// so the idempotency key is NOT consumed and the caller may retry with the
	// same key after resolving the underlying issue.
	StatusFailed TransferStatus = "FAILED"
)

// validTransitions defines the allowed state machine edges.
var validTransitions = map[TransferStatus][]TransferStatus{
	StatusPending:   {StatusProcessed, StatusFailed},
	StatusProcessed: {}, // terminal
	StatusFailed:    {}, // terminal
}

type Transfer struct {
	ID             string
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         float64
	Status         TransferStatus
	CreatedAt      time.Time
}

// ValidateTransition checks whether moving from the current status to next is
// permitted by the state machine. Returns ErrInvalidTransition if not.
func (t *Transfer) ValidateTransition(next TransferStatus) error {
	allowed := validTransitions[t.Status]
	for _, s := range allowed {
		if s == next {
			return nil
		}
	}
	return ErrInvalidTransition
}
