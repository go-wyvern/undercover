package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/go-wyvern/undercover/internal/testsupport"
	"github.com/gorilla/websocket"
)

type client struct {
	http *http.Client
	base string
}

func newClient(t *testing.T, base, name string) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &client{&http.Client{Jar: jar, Timeout: 5 * time.Second}, base}
	res := c.post(t, "/api/auth/guest", map[string]string{"displayName": name}, 200)
	res.Body.Close()
	return c
}
func (c *client) post(t *testing.T, path string, body any, status int) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	r, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != status {
		all, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("POST %s: status %d, expected %d: %s", path, r.StatusCode, status, all)
	}
	return r
}
func readView(t *testing.T, res *http.Response) game.View {
	t.Helper()
	defer res.Body.Close()
	var v game.View
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (c *client) view(t *testing.T, id string) game.View {
	t.Helper()
	res, err := c.http.Get(c.base + "/api/rooms/" + id)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatal("room unavailable")
	}
	return readView(t, res)
}
func waitSpeaking(t *testing.T, c *client, id string) game.View {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v := c.view(t, id)
		if v.Phase == "speaking" {
			c.playbackReady(t, id)
			return c.view(t, id)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("dealing did not finish")
	return game.View{}
}
func TestEmbeddedFrontendAndProtectedGameFlow(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "卧底俱乐部") {
		t.Fatal("embedded frontend missing")
	}
	bad, _ := http.Post(srv.URL+"/api/rooms/room-1/start", "application/json", strings.NewReader(`{}`))
	bad.Body.Close()
	if bad.StatusCode != 401 {
		t.Fatal("anonymous start was allowed")
	}
	clients := []*client{}
	for _, name := range []string{"甲", "乙", "丙", "丁"} {
		c := newClient(t, srv.URL, name)
		clients = append(clients, c)
		c.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	}
	for _, guest := range clients[1:] {
		guest.post(t, "/api/rooms/room-1/ready", map[string]bool{"ready": true}, 200).Body.Close()
	}
	c := clients[0]
	clients[1].post(t, "/api/rooms/room-1/start", map[string]string{}, 400).Body.Close()
	v := readView(t, c.post(t, "/api/rooms/room-1/start", map[string]string{}, 200))
	c.post(t, "/api/rooms/room-1/start", map[string]string{}, 400).Body.Close()
	v = waitSpeaking(t, c, v.ID)
	for _, other := range clients {
		view := other.view(t, v.ID)
		raw, _ := json.Marshal(view)
		if view.MyWord == "" || strings.Contains(string(raw), "undercoverId") || strings.Contains(string(raw), "userId") || len(view.Reveals) != 0 {
			t.Fatal("invalid private view")
		}
	}
	clients[1].post(t, "/api/rooms/room-1/speech", map[string]string{"text": "不应该轮到我。"}, 400).Body.Close()
	for _, other := range clients {
		other.playbackReady(t, "room-1")
		other.post(t, "/api/rooms/room-1/speech", map[string]string{"text": "它在日常生活中非常常见。"}, 200).Body.Close()
	}
	v = c.view(t, "room-1")
	if v.Phase != "voting" {
		t.Fatal("description phase did not end")
	}
	for _, player := range clients {
		player.playbackReady(t, "room-1")
	}
	c.post(t, "/api/rooms/room-1/vote", map[string]string{"targetId": v.MyPlayerID}, 400).Body.Close()
	target := v.Players[3].ID
	c.post(t, "/api/rooms/room-1/vote", map[string]string{"targetId": target}, 200).Body.Close()
	c.post(t, "/api/rooms/room-1/vote", map[string]string{"targetId": v.Players[1].ID}, 400).Body.Close()
	for i := 1; i < 4; i++ {
		choice := target
		if i == 3 {
			choice = v.Players[0].ID
		}
		clients[i].post(t, "/api/rooms/room-1/vote", map[string]string{"targetId": choice}, 200).Body.Close()
	}
	after := c.view(t, "room-1")
	if after.Players[3].Alive {
		t.Fatal("authoritative tally failed")
	}
	outsider := newClient(t, srv.URL, "旁观者")
	response, err := outsider.http.Get(srv.URL + "/api/rooms/room-1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode == 200 {
		t.Fatal("outsider accessed hidden card")
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/rooms/room-1/vote", strings.NewReader(`{"targetId":"x"}`))
	req.Header.Set("Origin", "https://evil.test")
	response, err = c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
}
func TestVoiceGatewayWithMockLinxAndRecordedPlayback(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-device-token" || r.Header.Get("Device-Id") == "" || r.Header.Get("Client-Id") == "" {
			t.Error("gateway omitted server credentials")
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var msg map[string]any
		if ws.ReadJSON(&msg) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "voice-1", "audio_params": map[string]any{"format": "opus", "sample_rate": 24000, "frame_duration": 20}})
		size := 0
		for {
			kind, b, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.BinaryMessage {
				size += len(b)
				continue
			}
			_ = json.Unmarshal(b, &msg)
			if msg["state"] == "stop" {
				if size != 640 {
					t.Errorf("wrong PCM size: %d", size)
				}
				_ = ws.WriteJSON(map[string]string{"type": "stt", "text": "它在日常生活中非常常见。"})
				return
			}
		}
	}))
	defer mock.Close()
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, Linx: linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, Token: "test-device-token", WSURL: strings.Replace(mock.URL, "http", "ws", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c := newClient(t, srv.URL, "语音测试")
	c.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	c.post(t, "/api/rooms/room-1/start", map[string]string{}, 200).Body.Close()
	v := waitSpeaking(t, c, "room-1")
	u, _ := url.Parse(srv.URL)
	cookies := c.http.Jar.Cookies(u)
	header := http.Header{}
	for _, cookie := range cookies {
		header.Add("Cookie", cookie.String())
	}
	ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1)+"/api/rooms/room-1/voice", header)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var message map[string]string
	if err = ws.ReadJSON(&message); err != nil || message["type"] != "ready" {
		t.Fatalf("gateway not ready: %v %#v", err, message)
	}
	pcm := make([]byte, 640)
	_ = ws.WriteMessage(websocket.BinaryMessage, pcm)
	_ = ws.WriteJSON(map[string]string{"type": "stop"})
	if err = ws.ReadJSON(&message); err != nil || message["type"] != "transcript" {
		t.Fatalf("transcription failed: %v %#v", err, message)
	}
	v = readView(t, c.post(t, "/api/rooms/room-1/speech", map[string]string{"text": message["text"], "audio": base64.StdEncoding.EncodeToString(pcm)}, 200))
	event := ""
	for _, l := range v.Logs {
		if l.Kind == "speech" && l.PlayerID == v.MyPlayerID {
			event = l.ID
		}
	}
	response, err := c.http.Get(srv.URL + "/api/rooms/room-1/audio/" + event)
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatal("recorded speech did not reach playback route")
	}
	response, err = c.http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(raw), "test-device-token") {
		t.Fatal("voice credentials leaked to frontend")
	}
}

func TestConcurrentNarrationUsesOneUpstreamConnection(t *testing.T) {
	var requests atomic.Int64
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var message map[string]any
		if ws.ReadJSON(&message) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "tts-1", "audio_params": map[string]any{"format": "opus", "sample_rate": 24000}})
		for {
			if ws.ReadJSON(&message) != nil {
				return
			}
			if message["state"] == "detect" {
				time.Sleep(100 * time.Millisecond)
				_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "start"})
				_ = ws.WriteMessage(websocket.BinaryMessage, []byte{0xf8, 0xff, 0xfe})
				_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
				return
			}
		}
	}))
	defer mock.Close()
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, Linx: linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, Token: "test", WSURL: strings.Replace(mock.URL, "http", "ws", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c := newClient(t, srv.URL, "同桌")
	c.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	v := readView(t, c.post(t, "/api/rooms/room-1/start", map[string]string{}, 200))
	event := v.Logs[0].ID
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := c.http.Get(srv.URL + "/api/rooms/room-1/audio/" + event)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			audio, _ := io.ReadAll(response.Body)
			if response.StatusCode != 200 || !bytes.HasPrefix(audio, []byte("OggS")) {
				t.Errorf("shared narration failed: %d %s", response.StatusCode, audio)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("same narration opened %d device connections", requests.Load())
	}
}

func TestCancellingOneListenerPreservesSharedPCMNarration(t *testing.T) {
	started := make(chan struct{})
	continueAudio := make(chan struct{})
	var connections atomic.Int64
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var msg map[string]any
		if ws.ReadJSON(&msg) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "pcm", "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000}})
		for ws.ReadJSON(&msg) == nil {
			if msg["state"] == "detect" {
				close(started)
				<-continueAudio
				_ = ws.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0})
				_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
				return
			}
		}
	}))
	defer mock.Close()
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, Linx: linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, Token: "test", WSURL: strings.Replace(mock.URL, "http", "ws", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c := newClient(t, srv.URL, "听众")
	c.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	v := readView(t, c.post(t, "/api/rooms/room-1/start", map[string]string{}, 200))
	path := srv.URL + "/api/rooms/room-1/audio/" + v.Logs[0].ID
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, "GET", path, nil)
	first := make(chan struct{})
	go func() {
		defer close(first)
		res, _ := c.http.Do(request)
		if res != nil {
			res.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("synthesis did not start")
	}
	cancel()
	<-first
	close(continueAudio)
	response, err := c.http.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	b, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(b, []byte("RIFF")) {
		t.Fatalf("second listener lost audio: HTTP %d", response.StatusCode)
	}
	if connections.Load() != 1 {
		t.Fatal("cancelling a listener restarted shared synthesis")
	}
}

func TestFullAssistantUsesModelToolsAndNarratesCommittedEventsAfterTranscription(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var message map[string]any
		if ws.ReadJSON(&message) != nil {
			return
		}
		if message["features"].(map[string]any)["mcp"] != true {
			t.Error("assistant omitted MCP capability")
		}
		rpc := func(id int, method string, params any) map[string]any {
			_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}})
			var reply map[string]any
			if err := ws.ReadJSON(&reply); err != nil {
				t.Error(err)
			}
			return reply
		}
		rpc(1, "initialize", map[string]any{})
		listed := rpc(2, "tools/list", nil)
		raw, _ := json.Marshal(listed)
		if !strings.Contains(string(raw), "game.start") {
			t.Error("game tools not discovered")
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "assistant", "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000}})
		for {
			kind, b, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.TextMessage {
				continue
			}
			_ = json.Unmarshal(b, &message)
			if message["state"] == "stop" {
				break
			}
		}
		_ = ws.WriteJSON(map[string]string{"type": "stt", "text": "开始游戏"})
		state := rpc(6, "tools/call", map[string]any{"name": "game.get_state", "arguments": map[string]any{}})
		stateJSON, _ := json.Marshal(state)
		if !strings.Contains(string(stateJSON), "currentUtterance") || !strings.Contains(string(stateJSON), "开始游戏") {
			t.Error("recognized input did not reach the model tool context")
		}
		result := rpc(3, "tools/call", map[string]any{"name": "game.start", "arguments": map[string]any{}})
		raw, _ = json.Marshal(result)
		if strings.Contains(string(raw), "myWord") {
			t.Error("tool exposed a human card")
		}

		replyCommittedLines(ws)

	}))
	defer mock.Close()
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, Linx: linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, Token: "test", WSURL: strings.Replace(mock.URL, "http", "ws", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c := newClient(t, srv.URL, "主持测试")
	c.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	u, _ := url.Parse(srv.URL)
	headers := http.Header{}
	for _, cookie := range c.http.Jar.Cookies(u) {
		headers.Add("Cookie", cookie.String())
	}
	ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1)+"/api/rooms/room-1/assistant/voice", headers)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(7 * time.Second))
	var message map[string]string
	if err = ws.ReadJSON(&message); err != nil || message["type"] != "ready" {
		t.Fatal("assistant not ready")
	}
	_ = ws.WriteMessage(websocket.BinaryMessage, make([]byte, 640))
	_ = ws.WriteJSON(map[string]string{"type": "stop"})
	if err = ws.ReadJSON(&message); err != nil || message["type"] != "transcript" {
		t.Fatal("missing transcript")
	}
	sawAudio := false
	for {
		kind, data, e := ws.ReadMessage()
		if e != nil {
			t.Fatal(e)
		}
		if kind == websocket.BinaryMessage {
			sawAudio = true
			continue
		}
		if e = json.Unmarshal(data, &message); e != nil {
			t.Fatal(e)
		}
		if message["type"] == "assistant_done" {
			break
		}
	}
	if sawAudio {
		t.Fatal("game action bypassed the ordered room narration queue")
	}

	v := c.view(t, "room-1")
	if v.Phase != "speaking" || v.GameID == "" {
		t.Fatal("voice start did not mutate game")
	}
	last := v.Logs[len(v.Logs)-1]
	if last.Source == "linx_agent" || last.Text != "请1号发言。" {
		t.Fatal("a model summary replaced the committed game prompt")
	}
	response, err := c.http.Get(srv.URL + "/api/rooms/room-1/audio/" + last.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	audio, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatal("actual model audio not playable")
	}
}

func newTestApp(cfg Config) (*App, error) {
	if cfg.RoomStore == nil {
		cfg.RoomStore = testsupport.NewRooms(true)
	}
	if cfg.Auth.SessionStore == nil {
		cfg.Auth.SessionStore = testsupport.NewSessions()
	}
	return New(cfg)
}
