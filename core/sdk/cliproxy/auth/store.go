package auth

import "context"

// Store abstracts persistence of Auth state across restarts.
type Store interface {
	// List returns all auth records stored in the backend.
	List(ctx context.Context) ([]*Auth, error)
	// SaveAuth persists the provided auth record, replacing any existing one with same ID.
	SaveAuth(ctx context.Context, auth *Auth) error
	// Delete removes the auth record identified by id.
	Delete(ctx context.Context, id string) error
}
