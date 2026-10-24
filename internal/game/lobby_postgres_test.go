package game

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/storage"
	"github.com/lib/pq"
)

func TestPostgresLobbyConfigurationAndReadinessRecovery(t *testing.T) {
	raw := os.Getenv("UNDERCOVER_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set UNDERCOVER_TEST_DATABASE_URL for real PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := fmt.Sprintf("undercover_lobby_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	store, err := storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	noFill := false
	v, err := m.CreateWithOptions("host", "房主", "持久化房间", "peach", 4, "", CreateOptions{Password: "persistent-passcode", AutoFillAI: &noFill})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.JoinWithPassword(v.ID, "friend", "朋友", "", "persistent-passcode"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetAISeat(v.ID, "host", 4, true); err != nil {
		t.Fatal(err)
	}
	ready(t, m, v.ID, "friend")
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.View(v.ID, "friend")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.AutoFillAI || !recovered.PasswordRequired || !recovered.Players[1].Ready || !recovered.Players[2].Bot || recovered.Players[2].Seat != 4 || recovered.CanStart {
		t.Fatal("restart lost persisted lobby settings or readiness")
	}
	if _, err = restarted.JoinWithPassword(v.ID, "third", "第三位", "", "wrong-password"); err == nil {
		t.Fatal("restart lost room protection")
	}
	if _, err = restarted.JoinWithPassword(v.ID, "third", "第三位", "", "persistent-passcode"); err != nil {
		t.Fatal(err)
	}
	ready(t, restarted, v.ID, "friend")
	ready(t, restarted, v.ID, "third")
	if _, err = restarted.Start(v.ID, "host"); err != nil {
		t.Fatal("recovered room could not start", err)
	}
}
