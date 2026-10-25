package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/gorilla/websocket"
)

func TestAutomaticLinxTurnNarratesOneCommittedPlayerBeforeTheNext(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.rooms.UseExternalAgent()
	_, _ = a.rooms.Join("room-1", "u", "玩家", "")
	_, _ = a.rooms.Start("room-1", "u")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := a.rooms.View("room-1", "u")
		if v.Phase == "speaking" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ackManagerPlayback(t, a.rooms, "room-1", "u")
	if _, err = a.rooms.Speak("room-1", "u", "生活里经常可以见到这样的东西。", nil); err != nil {
		t.Fatal(err)
	}
	batch, _ := a.rooms.NextAIBatch("room-1", "u")
	if batch == nil || len(batch.Tasks) != 1 {
		t.Fatal("missing AI batch")
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		var hello map[string]any
		if ws.ReadJSON(&hello) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "auto", "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000}})
		for {
			kind, b, e := ws.ReadMessage()
			if e != nil {
				return
			}
			if kind != websocket.TextMessage {
				continue
			}
			var v map[string]any
			_ = json.Unmarshal(b, &v)
			if v["state"] == "stop" {
				break
			}
		}
		batch, _ := a.rooms.NextAIBatch("room-1", "u")
		actions := []game.AIAction{}
		for _, task := range batch.Tasks {
			actions = append(actions, game.AIAction{PlayerSeat: task.Seat, Text: "它在日常生活当中有很多不同的用途。"})
		}
		_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "game.submit_ai_batch", "arguments": map[string]any{"batchId": batch.BatchID, "actions": actions}}}})
		var result map[string]any
		if ws.ReadJSON(&result) != nil {
			return
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), `"isError":true`) {
			t.Error("agent actions rejected")
		}

		// The model's untrusted post-tool narration must be discarded, in text and PCM.
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "start"})
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": "aiBatch为null，让我检查当前状态。"})
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte{99, 0, 99, 0})
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
		replyCommittedLines(ws)

	}))
	defer mock.Close()
	a.voice, err = linx.New(linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, WSURL: strings.Replace(mock.URL, "http", "ws", 1), Token: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	a.commandPCM = make([]byte, 640)

	for seat := 2; seat <= 4; seat++ {
		if err = a.advanceAgentTurn(context.Background(), "room-1", "u"); err != nil {
			t.Fatal(err)
		}
		view, _ := a.rooms.View("room-1", "u")
		count := 0
		for _, log := range view.Logs {
			if log.Source == "linx_agent" || strings.Contains(log.Text, "aiBatch") {
				t.Fatal("untrusted model commentary was published")
			}
			if log.Kind == "speech" {
				count++
				if log.Seat == seat && !bytes.HasPrefix(a.audioCache["room-1/"+log.ID], []byte("RIFF")) {
					t.Fatal("committed player audio was not ready")
				}
			}
		}
		if count != seat || view.AgentActive {
			t.Fatal("AI advanced another player before narrating this one")
		}
		if seat < 4 {
			next, _ := a.rooms.NextAIBatch("room-1", "u")
			if next == nil || len(next.Tasks) != 1 || next.Tasks[0].Seat != seat+1 {
				t.Fatal("wrong following AI task")
			}
		} else if view.Phase != "voting" {
			t.Fatal("AI descriptions did not reach voting")
		}
	}

}
func TestContinuationWAVResamplingAndInvalidInput(t *testing.T) {
	wav := linx.PCMToWAVAtRate(make([]byte, 4800), 24000)
	pcm, err := wavTo16K(wav)
	if err != nil || len(pcm) != 3200 {
		t.Fatal("24kHz continuation was not resampled")
	}
	if _, err = wavTo16K([]byte("OggS")); err == nil {
		t.Fatal("accepted unsupported audio")
	}
	binary.LittleEndian.PutUint16(wav[22:], 2)
	if _, err = wavTo16K(wav); err == nil {
		t.Fatal("accepted stereo continuation")
	}
}
