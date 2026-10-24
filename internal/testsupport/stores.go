// Package testsupport provides isolated in-memory stores for unit tests only.
// The executable always connects to PostgreSQL and Redis.
package testsupport

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-wyvern/undercover/internal/storage"
)

type Rooms struct {
	mu      sync.Mutex
	Records map[string]json.RawMessage
	Fail    error
}

func NewRooms(seeded bool) *Rooms {
	s := &Rooms{Records: make(map[string]json.RawMessage)}
	if seeded {
		for i, name := range []string{"微风茶话会", "周末摸鱼局", "午夜推理社"} {
			capacity := 4
			if i == 2 {
				capacity = 6
			}
			id := fmt.Sprintf("room-%d", i+1)
			s.Records[id], _ = json.Marshal(map[string]any{"id": id, "name": name, "theme": []string{"jade", "peach", "night"}[i], "capacity": capacity, "version": 1, "players": []any{}, "seeded": true, "createdAt": i + 1})
		}
	}
	return s
}
func (s *Rooms) Load(context.Context) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return nil, s.Fail
	}
	out := make(map[string]json.RawMessage)
	for k, v := range s.Records {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out, nil
}
func (s *Rooms) Save(_ context.Context, id string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	s.Records[id] = append(json.RawMessage(nil), b...)
	return nil
}
func (s *Rooms) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	if _, ok := s.Records[id]; !ok {
		return storage.ErrNotFound
	}
	delete(s.Records, id)
	return nil
}

type Sessions struct {
	mu      sync.Mutex
	Records map[string][]byte
	Expires map[string]time.Time
	Fail    error
}

func NewSessions() *Sessions {
	return &Sessions{Records: map[string][]byte{}, Expires: map[string]time.Time{}}
}
func (s *Sessions) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return nil, s.Fail
	}
	if !s.Expires[key].After(time.Now()) {
		delete(s.Records, key)
		delete(s.Expires, key)
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), s.Records[key]...), nil
}
func (s *Sessions) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	s.Records[key] = append([]byte(nil), value...)
	s.Expires[key] = time.Now().Add(ttl)
	return nil
}
func (s *Sessions) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	delete(s.Records, key)
	delete(s.Expires, key)
	return nil
}
