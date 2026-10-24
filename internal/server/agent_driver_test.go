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

func TestAutomaticLinxTurnUsesGameToolsAndCachesTimedAudio(t *testing.T) {
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
	if _, err = a.rooms.Speak("room-1", "u", "生活里经常可以见到这样的东西。", nil); err != nil {
		t.Fatal(err)
	}
	batch, _ := a.rooms.NextAIBatch("room-1", "u")
	if batch == nil || len(batch.Tasks) != 3 {
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
		for i, seat := range []string{"二", "三", "四"} {
			_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "sentence_start", "text": seat + "号：它在日常生活当中有很多不同的用途。"})
			_ = ws.WriteMessage(websocket.BinaryMessage, make([]byte, 3200*(i+1)))
		}
		_ = ws.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
	}))
	defer mock.Close()
	a.voice, err = linx.New(linx.Config{DeviceIDs: []string{"02:00:00:00:00:01"}, WSURL: strings.Replace(mock.URL, "http", "ws", 1), Token: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	a.commandPCM = make([]byte, 640)
	if err = a.advanceAgentTurn(context.Background(), "room-1", "u"); err != nil {
		t.Fatal(err)
	}
	v, _ := a.rooms.View("room-1", "u")
	if v.Phase != "voting" || v.AgentActive {
		t.Fatal("AI turn did not finish")
	}
	last := v.Logs[len(v.Logs)-1]
	if last.Source != "linx_agent" || len(last.AudioCues) != 3 {
		t.Fatal("missing actual agent audio cues")
	}
	for i, cue := range last.AudioCues {
		if cue.Seat != i+2 || cue.PlayerID == "" || cue.LogID == "" {
			t.Fatalf("cue has wrong speaker: %+v", cue)
		}
		expected := []int64{0, 100, 300}[i]
		if cue.OffsetMS != expected {
			t.Fatalf("offset %d instead of %d", cue.OffsetMS, expected)
		}
	}
	if !bytes.HasPrefix(a.audioCache["room-1/"+last.ID], []byte("RIFF")) {
		t.Fatal("agent voice not cached")
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
