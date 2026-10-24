package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestHostClosesRoomAndMembersReceiveClosedEvent(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, RoomStore: testsupport.NewRooms(false)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	owner := newClient(t, srv.URL, "房主")
	friend := newClient(t, srv.URL, "同桌")
	outsider := newClient(t, srv.URL, "路人")
	created := readView(t, owner.post(t, "/api/rooms", map[string]any{"name": "关闭测试", "capacity": 4, "theme": "jade"}, 200))
	path := "/api/rooms/" + created.ID
	friend.post(t, path+"/join", map[string]string{}, 200).Body.Close()
	deleteRoom := func(c *client, status int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		response, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("DELETE status %d, want %d: %s", response.StatusCode, status, data)
		}
	}
	deleteRoom(friend, 403)
	deleteRoom(outsider, 403)
	response, err := friend.http.Get(srv.URL + path + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	// Consume the initial snapshot before invoking the host's DELETE.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
	}
	events := make(chan string, 1)
	go func() { b, _ := io.ReadAll(reader); events <- string(b) }()
	deleteRoom(owner, 200)
	select {
	case event := <-events:
		if !strings.Contains(event, "event: closed") {
			t.Fatalf("no closed event: %s", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("member event stream did not close")
	}
	for _, c := range []*client{owner, friend} {
		res, err := c.http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatal("deleted room remains readable")
		}
		res, err = c.http.Get(srv.URL + "/api/auth/me")
		if err != nil {
			t.Fatal(err)
		}
		var account struct {
			CurrentRoomID string `json:"currentRoomId"`
		}
		json.NewDecoder(res.Body).Decode(&account)
		res.Body.Close()
		if account.CurrentRoomID != "" {
			t.Fatal("account still points to deleted room")
		}
	}
	if _, err = a.rooms.View(created.ID, "owner"); !errors.Is(err, game.ErrRoomNotFound) {
		t.Fatal("deleted room remained cached")
	}
	deleteRoom(owner, 404)
}
