package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func postgresTestURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("UNDERCOVER_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set UNDERCOVER_TEST_DATABASE_URL for real PostgreSQL integration")
	}
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("undercover_test_%d", time.Now().UnixNano())
	if _, err = db.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := db.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		if err != nil {
			t.Error(err)
		}
		db.Close()
	})
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("test database setting must be a PostgreSQL URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
func TestPostgresDurabilityDeletionAndSingleWriter(t *testing.T) {
	dsn := postgresTestURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if duplicate, err := OpenPostgres(ctx, dsn); err == nil {
		duplicate.Close()
		t.Fatal("two processes acquired the same room store")
	}
	for _, id := range []string{"a", "b"} {
		state := []byte(fmt.Sprintf(`{"id":%q,"capacity":4,"version":1,"game":{"words":{"p":"secret"}}}`, id))
		if err = p.Save(ctx, id, state); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.Save(ctx, "a", []byte(`{"id":"a","capacity":4,"version":2}`)); err != nil {
		t.Fatal(err)
	}
	p.Close()
	p, err = OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	records, err := p.Load(ctx)
	if err != nil || len(records) != 2 || !strings.Contains(string(records["a"]), `"version": 2`) || !strings.Contains(string(records["b"]), "secret") {
		t.Fatalf("restart lost room state: %v", err)
	}
	// The explicit import preserves existing rooms and inserts new ones atomically.
	filename := filepath.Join(t.TempDir(), "legacy.json")
	if err = os.WriteFile(filename, []byte(`{"a":{"id":"a","capacity":4,"version":0},"c":{"id":"c","capacity":6,"version":1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := p.ImportLegacy(ctx, filename)
	if err != nil || n != 1 {
		t.Fatalf("legacy import: %d %v", n, err)
	}
	records, _ = p.Load(ctx)
	var current struct {
		Version int `json:"version"`
	}
	json.Unmarshal(records["a"], &current)
	if current.Version != 2 {
		t.Fatal("import overwrote a live room")
	}
	for _, id := range []string{"a", "b", "c"} {
		if err = p.Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.Delete(ctx, "a"); err != ErrNotFound {
		t.Fatalf("missing delete: %v", err)
	}
	p.Close()
	p, err = OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	records, err = p.Load(ctx)
	if err != nil || len(records) != 0 {
		t.Fatal("deleted rooms reappeared after restart")
	}
}
func TestRedisSessionTTLSharingAndRevocation(t *testing.T) {
	raw := os.Getenv("UNDERCOVER_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("set UNDERCOVER_TEST_REDIS_URL for real Redis integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := OpenRedis(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenRedis(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	prefix := fmt.Sprintf("undercover:test:%d:", time.Now().UnixNano())
	first.prefix = prefix
	second.prefix = prefix
	key := strings.Repeat("a", 64)
	defer first.Delete(context.Background(), key)
	value := []byte(`{"user":{"id":"owner"}}`)
	if err = first.Set(ctx, key, value, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	ttl, err := first.client.TTL(ctx, prefix+key).Result()
	if err != nil || ttl < 6*24*time.Hour || ttl > 7*24*time.Hour {
		t.Fatalf("bad session TTL: %v %v", ttl, err)
	}
	found, err := second.Get(ctx, key)
	if err != nil || string(found) != string(value) {
		t.Fatal("session not shared across clients")
	}
	if err = second.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err = first.Get(ctx, key); err != ErrNotFound {
		t.Fatal("logout did not revoke the shared session")
	}
	if err = first.Set(ctx, key, value, 25*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err = second.Get(ctx, key); err != ErrNotFound {
		t.Fatal("expired session remained usable")
	}
}
