package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
)

func ackManagerPlayback(t *testing.T, m *game.Manager, id, uid string) {
	t.Helper()
	v, err := m.View(id, uid)
	if err != nil {
		t.Fatal(err)
	}
	if w := v.Action; w != nil && w.Required && !w.Ready && w.State == "waiting_playback" {
		if _, err = m.PlaybackReady(id, uid, w.ID, w.BoundaryID, "played"); err != nil {
			t.Fatal(err)
		}
	}
}

func (c *client) playbackReady(t *testing.T, id string) {
	t.Helper()
	v := c.view(t, id)
	if w := v.Action; w != nil && w.Required && !w.Ready && w.State == "waiting_playback" {
		c.post(t, "/api/rooms/"+id+"/playback-ready", map[string]string{"turnId": w.ID, "throughLogId": w.BoundaryID, "result": "played"}, 200).Body.Close()
	}
}

func TestPlaybackHTTPRejectsEarlyActionsAndStaleAcknowledgements(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	host := newClient(t, srv.URL, "房主")
	other := newClient(t, srv.URL, "新一")
	host.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	other.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	other.post(t, "/api/rooms/room-1/ready", map[string]bool{"ready": true}, 200).Body.Close()
	host.post(t, "/api/rooms/room-1/start", map[string]string{}, 200).Body.Close()
	var v game.View
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		v = host.view(t, "room-1")
		if v.Action != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v.Action == nil || v.Deadline != 0 {
		t.Fatal("no protected playback window")
	}
	body := map[string]string{"turnId": v.Action.ID, "throughLogId": v.Action.BoundaryID, "result": "played"}
	other.post(t, "/api/rooms/room-1/playback-ready", body, 400).Body.Close()
	host.post(t, "/api/rooms/room-1/speech", map[string]string{"text": "它很常见。"}, 409).Body.Close()
	host.post(t, "/api/rooms/room-1/skip", map[string]string{}, 409).Body.Close()
	host.post(t, "/api/rooms/room-1/playback-ready", body, 200).Body.Close()
	active := host.view(t, "room-1")
	host.post(t, "/api/rooms/room-1/playback-ready", body, 200).Body.Close()
	if host.view(t, "room-1").Deadline != active.Deadline {
		t.Fatal("HTTP retry reset countdown")
	}
	host.post(t, "/api/rooms/room-1/speech", map[string]string{"text": "它很常见。"}, 200).Body.Close()
	host.post(t, "/api/rooms/room-1/playback-ready", body, 409).Body.Close()
}
