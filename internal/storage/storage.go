// Package storage contains the PostgreSQL room and Redis session adapters.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("record not found")

type RoomStore interface {
	Load(context.Context) (map[string]json.RawMessage, error)
	Save(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type SessionStore interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}
