package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/gorilla/websocket"
)

// Emulate the documented direct-TTS path on an existing device connection.
func replyCommittedLines(ws *websocket.Conn) {
	for {
		var request map[string]any
		if ws.ReadJSON(&request) != nil {
			return
		}
		text, _ := request["text_response"].(string)
		if request["state"] != "detect" || text == "" {
			continue
		}
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "start"})
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": text})
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte{1, 0, 2, 0, 3, 0})
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
	}
}

func TestHumanRecordingIsPlayableBeforeNextPlayerPromptAndNeverPublishesReasoning(t *testing.T) {
	promptRequested := make(chan struct{})
	allowPrompt := make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var request map[string]any
		if ws.ReadJSON(&request) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "human", "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000}})
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.TextMessage {
				continue
			}
			_ = json.Unmarshal(data, &request)
			if request["state"] == "stop" {
				break
			}
		}
		_ = ws.WriteJSON(map[string]string{"type": "stt", "text": "这是一个吃的东西。"})
		_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"id": 1, "method": "tools/call", "params": map[string]any{"name": "game.submit_description", "arguments": map[string]any{}}}})
		if ws.ReadJSON(&request) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "start"})
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": "aiBatch为null，但当前轮到2号新一（真人），等待其发言。开始游戏，请2号发言。"})
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte{99, 0, 99, 0})
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
		for {
			if ws.ReadJSON(&request) != nil {
				return
			}
			if request["state"] == "detect" {
				if request["text_response"] != "请2号发言。" {
					t.Errorf("unexpected narration: %v", request["text_response"])
				}
				close(promptRequested)
				select {
				case <-allowPrompt:
				case <-r.Context().Done():
					return
				}
				_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "start"})
				_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": "请2号发言。"})
				_ = ws.WriteMessage(websocket.BinaryMessage, []byte{1, 0, 2, 0})
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
	host := newClient(t, srv.URL, "1号房主")
	other := newClient(t, srv.URL, "新一")
	host.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	other.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	other.post(t, "/api/rooms/room-1/ready", map[string]bool{"ready": true}, 200).Body.Close()
	host.post(t, "/api/rooms/room-1/start", map[string]string{}, 200).Body.Close()
	waitSpeaking(t, host, "room-1")
	base, _ := url.Parse(srv.URL)
	headers := http.Header{}
	for _, cookie := range host.http.Jar.Cookies(base) {
		headers.Add("Cookie", cookie.String())
	}
	ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1)+"/api/rooms/room-1/assistant/voice", headers)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
	var message map[string]string
	if ws.ReadJSON(&message) != nil || message["type"] != "ready" {
		t.Fatal("assistant not ready")
	}
	pcm := bytes.Repeat([]byte{7, 0}, 320)
	_ = ws.WriteMessage(websocket.BinaryMessage, pcm)
	_ = ws.WriteJSON(map[string]string{"type": "stop"})
	select {
	case <-promptRequested:
	case <-time.After(5 * time.Second):
		t.Fatal("committed narration was not requested")
	}
	view := host.view(t, "room-1")
	if view.CurrentPlayerID != view.Players[1].ID {
		t.Fatal("did not advance to human 2")
	}
	var speechID, promptID string
	for _, log := range view.Logs {
		if strings.Contains(log.Text, "aiBatch") || log.Source == "linx_agent" {
			t.Fatal("internal model reasoning escaped into the room")
		}
		if log.Kind == "speech" {
			speechID = log.ID
		}
		if log.Text == "请2号发言。" {
			promptID = log.ID
		}
	}
	if speechID == "" || promptID == "" {
		t.Fatal("missing committed description or next-player prompt")
	}
	// Original audio is available to both players while the prompt is still being synthesized.
	for _, listener := range []*client{host, other} {
		res, err := listener.http.Get(srv.URL + "/api/rooms/room-1/audio/" + speechID)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || len(body) < 44 || !bytes.Equal(body[44:], pcm) {
			t.Fatal("submitted player recording did not play immediately")
		}
	}
	received := make(chan []byte, 1)
	go func() {
		res, err := other.http.Get(srv.URL + "/api/rooms/room-1/audio/" + promptID)
		if err != nil {
			received <- nil
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		received <- body
	}()
	select {
	case <-received:
		t.Fatal("next-player prompt did not wait for canonical TTS")
	case <-time.After(40 * time.Millisecond):
	}
	close(allowPrompt)
	select {
	case data := <-received:
		if len(data) < 44 || !bytes.HasPrefix(data, []byte("RIFF")) || !bytes.Equal(data[44:], []byte{1, 0, 2, 0}) {
			t.Fatal("prompt contains stale model audio")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canonical prompt never became playable")
	}
	for {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.BinaryMessage {
			t.Fatal("unordered model audio reached the initiating browser")
		}
		_ = json.Unmarshal(data, &message)
		if message["type"] == "assistant_done" {
			break
		}
	}
}

func TestFreeModelReplyCannotInventNamesOrEnglishAndMissedSpeechIsRecoverable(t *testing.T) {
	for _, testCase := range []struct {
		name, input, prompt, tool string
		pending                   bool
	}{
		{"status", "轮到谁了", "请1号发言。", "game.get_ai_batch", false},
		{"missed description", "这是在水面下面进行的活动。", "请1号确认提交发言。", "game.get_ai_batch", true},
		{"unauthorized skip", "这是在水面下面进行的活动。", "请1号确认提交发言。", "game.skip", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				var request map[string]any
				if ws.ReadJSON(&request) != nil {
					return
				}
				hello, _ := json.Marshal(request)
				if !strings.Contains(string(hello), "2号：新一（真人）") {
					t.Error("real roster was not injected")
				}
				_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"id": 1, "method": "tools/list"}})
				if ws.ReadJSON(&request) != nil {
					return
				}
				listed, _ := json.Marshal(request)
				if strings.Contains(string(listed), `"name":"game.get_ai_batch"`) || strings.Contains(string(listed), `"name":"game.submit_ai_batch"`) {
					t.Error("AI actions were advertised to a human voice session")
				}
				_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "missed", "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000}})
				for {
					kind, data, err := ws.ReadMessage()
					if err != nil {
						return
					}
					if kind != websocket.TextMessage {
						continue
					}
					_ = json.Unmarshal(data, &request)
					if request["state"] == "stop" {
						break
					}
				}
				_ = ws.WriteJSON(map[string]string{"type": "stt", "text": testCase.input})
				// A legacy prompt may even attempt a tool absent from this session.
				_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"id": 2, "method": "tools/call", "params": map[string]any{"name": testCase.tool, "arguments": map[string]any{}}}})
				if ws.ReadJSON(&request) != nil {
					return
				}
				rejected, _ := json.Marshal(request)
				if !strings.Contains(string(rejected), `"isError":true`) {
					t.Error("human was allowed to drive AI turns")
				}
				for _, text := range []string{"I'll check the current game state and AI tasks", "请二号小北发言。"} {
					_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": text})
					_ = ws.WriteMessage(websocket.BinaryMessage, []byte{99, 0, 99, 0})
				}
				_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
				for {
					if ws.ReadJSON(&request) != nil {
						return
					}
					if request["state"] == "detect" {
						if request["text_response"] != testCase.prompt {
							t.Errorf("guessed status: %v", request["text_response"])
						}
						_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "start"})
						_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": testCase.prompt})
						_ = ws.WriteMessage(websocket.BinaryMessage, []byte{1, 0, 2, 0})
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
			host := newClient(t, srv.URL, "房主")
			other := newClient(t, srv.URL, "新一")
			host.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
			other.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
			other.post(t, "/api/rooms/room-1/ready", map[string]bool{"ready": true}, 200).Body.Close()
			host.post(t, "/api/rooms/room-1/start", map[string]string{}, 200).Body.Close()
			waitSpeaking(t, host, "room-1")
			base, _ := url.Parse(srv.URL)
			headers := http.Header{}
			for _, cookie := range host.http.Jar.Cookies(base) {
				headers.Add("Cookie", cookie.String())
			}
			ws, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1)+"/api/rooms/room-1/assistant/voice", headers)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
			var message map[string]string
			if ws.ReadJSON(&message) != nil || message["type"] != "ready" {
				t.Fatal("not ready")
			}
			pcm := bytes.Repeat([]byte{7, 0}, 320)
			_ = ws.WriteMessage(websocket.BinaryMessage, pcm)
			_ = ws.WriteJSON(map[string]string{"type": "stop"})
			for {
				kind, data, err := ws.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if kind == websocket.BinaryMessage {
					t.Fatal("unverified English/name audio was streamed")
				}
				_ = json.Unmarshal(data, &message)
				if message["type"] == "assistant_done" {
					break
				}
			}
			view := host.view(t, "room-1")
			last := view.Logs[len(view.Logs)-1]
			if last.Text != testCase.prompt {
				t.Fatalf("unverified model text was published: %s", last.Text)
			}
			res, err := host.http.Get(srv.URL + "/api/rooms/room-1/audio/" + last.ID)
			if err != nil {
				t.Fatal(err)
			}
			audio, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if len(audio) < 44 || !bytes.Equal(audio[44:], []byte{1, 0, 2, 0}) {
				t.Fatal("cached audio contains stale model output")
			}
			if view.CurrentPlayerID != view.MyPlayerID {
				t.Fatal("model response fabricated a submitted turn")
			}
			if testCase.pending {
				if view.MyDraft != testCase.input || view.Deadline != 0 {
					t.Fatal("missed speech was not recoverable")
				}
				if other.view(t, "room-1").MyDraft != "" {
					t.Fatal("unsubmitted speech leaked")
				}
				submitted := readView(t, host.post(t, "/api/rooms/room-1/speech", map[string]string{"text": view.MyDraft}, 200))
				count := 0
				for _, log := range submitted.Logs {
					if log.Kind == "speech" {
						count++
						if log.Text != testCase.input {
							t.Fatal("recovery changed original")
						}
					}
				}
				if count != 1 || submitted.MyDraft != "" || submitted.CurrentPlayerID != submitted.Players[1].ID {
					t.Fatal("confirmation did not preserve exactly one original speech")
				}
			} else if view.MyDraft != "" {
				t.Fatal("status question was treated as a player description")
			}
		})
	}
}

func TestOutOfTurnVoiceIsRejectedBeforeUpgradeOrLinxConnection(t *testing.T) {
	var connections atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		http.Error(w, "unexpected upstream", 500)
	}))
	defer mock.Close()
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}, Linx: linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, Token: "test", WSURL: strings.Replace(mock.URL, "http", "ws", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	host := newClient(t, srv.URL, "房主")
	second := newClient(t, srv.URL, "新一")
	host.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	second.post(t, "/api/rooms/room-1/join", map[string]string{}, 200).Body.Close()
	second.post(t, "/api/rooms/room-1/ready", map[string]bool{"ready": true}, 200).Body.Close()
	host.post(t, "/api/rooms/room-1/start", map[string]string{}, 200).Body.Close()
	waitSpeaking(t, host, "room-1")
	host.post(t, "/api/rooms/room-1/speech", map[string]string{"text": "这是在日常生活中可以见到的东西。"}, 200).Body.Close()
	base, _ := url.Parse(srv.URL)
	headers := http.Header{}
	for _, cookie := range host.http.Jar.Cookies(base) {
		headers.Add("Cookie", cookie.String())
	}
	conn, res, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1)+"/api/rooms/room-1/assistant/voice", headers)
	if conn != nil {
		conn.Close()
		t.Fatal("out-of-turn player opened a voice socket")
	}
	if err == nil || res == nil || res.StatusCode != http.StatusConflict {
		t.Fatal("out-of-turn request was not rejected before upgrade")
	}
	res.Body.Close()
	if connections.Load() != 0 || host.view(t, "room-1").AgentActive {
		t.Fatal("rejected request occupied the room or Linx terminal")
	}
}
