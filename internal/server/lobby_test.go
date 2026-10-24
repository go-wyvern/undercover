package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestProtectedLobbyHTTPAndVoiceShareStartChecks(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, RoomStore: testsupport.NewRooms(false)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	host := newClient(t, srv.URL, "房主")
	friend := newClient(t, srv.URL, "新一")
	outsider := newClient(t, srv.URL, "路人")
	v := readView(t, host.post(t, "/api/rooms", map[string]any{"name": "口令测试", "capacity": 4, "password": "test-secret"}, 200))
	path := "/api/rooms/" + v.ID
	for _, password := range []string{"", "wrong"} {
		res := friend.post(t, path+"/join", map[string]string{"password": password}, 403)
		var body map[string]string
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if !strings.HasPrefix(body["code"], "room_password_") {
			t.Fatal("missing password error code")
		}
	}
	for _, endpoint := range []string{path, path + "/events"} {
		res, err := outsider.http.Get(srv.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatal("share URL bypassed membership", endpoint, res.StatusCode)
		}
	}
	joined := readView(t, friend.post(t, path+"/join", map[string]string{"password": "test-secret"}, 200))
	friend.post(t, path+"/ready", map[string]any{"ready": true, "playerId": v.MyPlayerID}, 400).Body.Close()
	friend.post(t, path+"/settings", map[string]bool{"autoFillAI": false}, 403).Body.Close()
	outsider.post(t, path+"/ai-seat", map[string]any{"seat": 4, "bot": true}, 403).Body.Close()
	friend.post(t, path+"/members/"+v.MyPlayerID+"/remove", map[string]any{}, 403).Body.Close()
	res := host.post(t, path+"/start", map[string]any{}, 400)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "2号新一") {
		t.Fatal("HTTP start did not identify unready member")
	}
	handler := a.gameToolHandler(v.ID, a.roomsUserID(t, v.ID), func() []byte { return nil }, nil)
	if _, err = handler(context.Background(), "game.start", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "2号新一") {
		t.Fatal("voice tool bypassed readiness")
	}
	friend.post(t, path+"/ready", map[string]bool{"ready": true}, 200).Body.Close()
	host.post(t, path+"/settings", map[string]bool{"autoFillAI": false}, 200).Body.Close()
	host.post(t, path+"/ai-seat", map[string]any{"seat": 4, "bot": true}, 200).Body.Close()
	friend.post(t, path+"/ready", map[string]bool{"ready": true}, 200).Body.Close()
	host.post(t, path+"/start", map[string]any{}, 400).Body.Close()
	host.post(t, path+"/ai-seat", map[string]any{"seat": 3, "bot": true}, 200).Body.Close()
	friend.post(t, path+"/ready", map[string]bool{"ready": true}, 200).Body.Close()
	for _, endpoint := range []string{"/api/rooms", path} {
		res, err := host.http.Get(srv.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		assertNoRoomPassword(t, raw)
	}
	public, err := a.publicAgentState(v.ID, a.roomsUserID(t, v.ID))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(public)
	assertNoRoomPassword(t, raw)
	v = readView(t, host.post(t, path+"/start", map[string]any{}, 200))
	if v.Phase != "dealing" || v.MyWord == "" {
		t.Fatal("ready lobby failed to start")
	}
	other := friend.view(t, v.ID)
	if other.MyPlayerID != joined.MyPlayerID || len(other.Reveals) != 0 || other.MyWord == "" {
		t.Fatal("dealing exposed wrong card state")
	}
	host.post(t, path+"/members/"+joined.MyPlayerID+"/remove", map[string]any{}, 400).Body.Close()
	host.post(t, path+"/ai-seat", map[string]any{"seat": 4, "bot": false}, 400).Body.Close()
	host.post(t, path+"/settings", map[string]bool{"autoFillAI": true}, 400).Body.Close()
	friend.post(t, path+"/ready", map[string]bool{"ready": false}, 400).Body.Close()
}

func (a *App) roomsUserID(t *testing.T, id string) string {
	t.Helper()
	// Find the host from a session-independent test account lookup.
	records, err := a.cfg.RoomStore.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var room game.Room
	if err = json.Unmarshal(records[id], &room); err != nil {
		t.Fatal(err)
	}
	return room.OwnerID
}

func assertNoRoomPassword(t *testing.T, raw []byte) {
	t.Helper()
	for _, secret := range []string{"test-secret", "passwordHash", "pbkdf2", "userId"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("public response leaked %s", secret)
		}
	}
}

func TestReadinessSSEAndRemovedMemberAccessRevocation(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, RoomStore: testsupport.NewRooms(false)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	host := newClient(t, srv.URL, "房主")
	friend := newClient(t, srv.URL, "朋友")
	v := readView(t, host.post(t, "/api/rooms", map[string]any{"name": "实时准备", "capacity": 4}, 200))
	path := "/api/rooms/" + v.ID
	joined := readView(t, friend.post(t, path+"/join", map[string]any{}, 200))
	res, err := friend.http.Get(srv.URL + path + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	readSnapshot := func() game.View {
		t.Helper()
		var snapshot game.View
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snapshot); err != nil {
					t.Fatal(err)
				}
			}
			if line == "\n" {
				return snapshot
			}
		}
	}
	readSnapshot()
	friend.post(t, path+"/ready", map[string]bool{"ready": true}, 200).Body.Close()
	snapshot := readSnapshot()
	if !snapshot.CanStart || !snapshot.Players[1].Ready {
		t.Fatal("SSE did not update readiness")
	}
	events := make(chan string, 1)
	go func() { data, _ := io.ReadAll(reader); events <- string(data) }()
	host.post(t, path+"/members/"+joined.MyPlayerID+"/remove", map[string]any{}, 200).Body.Close()
	select {
	case text := <-events:
		if !strings.Contains(text, "event: removed") {
			t.Fatal("missing removed event", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("removed member stream stayed open")
	}
	for _, endpoint := range []string{path, path + "/events"} {
		res, err := friend.http.Get(srv.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatal("removed member retained room access")
		}
	}
	if host.view(t, v.ID).HumanCount != 1 {
		t.Fatal("removed member remains seated")
	}
}
