package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	_ "github.com/lib/pq"
)

type PostgresRooms struct {
	db    *sql.DB
	lease *sql.Conn
}

// A dedicated advisory-lock connection prevents two processes from running
// independent game timers over the same rooms. PostgreSQL is the durable store;
// subscriptions, audio and turn scheduling still belong to one Go instance.
func OpenPostgres(ctx context.Context, dsn string) (*PostgresRooms, error) {
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	p := &PostgresRooms{db: db}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	p.lease, err = db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	var locked bool
	err = p.lease.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext(current_database()), hashtext(current_schema() || ':undercover'))`).Scan(&locked)
	if err != nil || !locked {
		p.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("another undercover process is already using this PostgreSQL schema; run one Go instance")
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS undercover_rooms (
  id TEXT PRIMARY KEY,
  state JSONB NOT NULL CHECK (jsonb_typeof(state) = 'object' AND state->>'id' = id),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
 )`)
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("initialize rooms table: %w", err)
	}
	return p, nil
}

func (p *PostgresRooms) Close() error {
	// Return the dedicated connection, then close the entire pool so its
	// session-level advisory lock is released rather than reused by this app.
	if p.lease != nil {
		_ = p.lease.Close()
	}
	return p.db.Close()
}
func (p *PostgresRooms) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, state FROM undercover_rooms ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rooms := make(map[string]json.RawMessage)
	for rows.Next() {
		var id string
		var state []byte
		if err = rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		rooms[id] = append(json.RawMessage(nil), state...)
	}
	return rooms, rows.Err()
}
func (p *PostgresRooms) Save(ctx context.Context, id string, state []byte) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO undercover_rooms (id, state) VALUES ($1, $2::jsonb)
  ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()`, id, string(state))
	return err
}
func (p *PostgresRooms) Delete(ctx context.Context, id string) error {
	result, err := p.db.ExecContext(ctx, `DELETE FROM undercover_rooms WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// ImportLegacy is an explicit, one-time import. Existing database rooms win,
// so a stale JSON backup never overwrites games already stored in PostgreSQL.
func (p *PostgresRooms) ImportLegacy(ctx context.Context, filename string) (int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var rooms map[string]json.RawMessage
	decoder := json.NewDecoder(io.LimitReader(f, 64<<20))
	if err = decoder.Decode(&rooms); err != nil {
		return 0, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return 0, errors.New("invalid room backup")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	imported := 0
	for id, state := range rooms {
		var room struct {
			ID       string `json:"id"`
			Capacity int    `json:"capacity"`
		}
		if json.Unmarshal(state, &room) != nil || id == "" || room.ID != id || (room.Capacity != 4 && room.Capacity != 6) {
			return 0, errors.New("invalid room in backup")
		}
		result, e := tx.ExecContext(ctx, `INSERT INTO undercover_rooms (id, state) VALUES ($1, $2::jsonb) ON CONFLICT (id) DO NOTHING`, id, string(state))
		if e != nil {
			return 0, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return 0, e
		}
		imported += int(n)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return imported, nil
}
