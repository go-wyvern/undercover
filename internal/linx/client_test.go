package linx

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func mockVoice(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-secret" || r.Header.Get("Device-Id") != "02:00:00:00:00:01" || r.Header.Get("Client-Id") == "" || r.Header.Get("Protocol-Version") != "1" {
			t.Error("missing Linx authentication headers")
			http.Error(w, "bad headers", 401)
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var hello map[string]any
		if c.ReadJSON(&hello) != nil {
			return
		}
		audio, ok := hello["audio_params"].(map[string]any)
		if !ok || audio["format"] != "pcm" || audio["sample_rate"] != float64(16000) {
			t.Error("wrong input audio format")
		}
		_ = c.WriteJSON(map[string]any{"type": "hello", "session_id": "session-test", "audio_params": map[string]any{"format": "opus", "sample_rate": 24000, "frame_duration": 20}})
		for {
			kind, b, err := c.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.BinaryMessage {
				continue
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m["type"] == "listen" && m["state"] == "detect" {
				if m["text_response"] != "下一位请发言。" {
					t.Error("wrong explicit narration text")
				}
				_ = c.WriteJSON(map[string]string{"type": "tts", "state": "start"})
				_ = c.WriteMessage(websocket.BinaryMessage, []byte{0xf8, 0xff, 0xfe})
				_ = c.WriteJSON(map[string]string{"type": "tts", "state": "stop"})
				return
			}
			if m["type"] == "listen" && m["state"] == "stop" {
				_ = c.WriteJSON(map[string]string{"type": "stt", "text": "我觉得它很常见。"})
				return
			}
		}
	}))
}
func TestDeviceHandshakeLeaseAndExplicitNarration(t *testing.T) {
	srv := mockVoice(t)
	defer srv.Close()
	p, err := New(Config{DeviceIDs: []string{"02:00:00:00:00:01"}, WSURL: strings.Replace(srv.URL, "http", "ws", 1), Token: "device-secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := p.Open(ctx, "user-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Open(ctx, "user-two"); err != ErrBusy {
		t.Fatalf("device concurrently reused: %v", err)
	}
	s.Close()
	data, err := p.Synthesize(ctx, "public-log", "下一位请发言。")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "OggS") || !strings.Contains(string(data), "OpusHead") {
		t.Fatal("raw Opus was not packaged")
	}
	// The final page is a 20ms packet: 960 samples at the required 48kHz clock.
	off := 0
	var lastGranule uint64
	pages := 0
	for off < len(data) {
		if off+27 > len(data) {
			t.Fatal("truncated page")
		}
		n := int(data[off+26])
		if off+27+n > len(data) {
			t.Fatal("truncated lacing")
		}
		size := 0
		for _, v := range data[off+27 : off+27+n] {
			size += int(v)
		}
		end := off + 27 + n + size
		if end > len(data) {
			t.Fatal("page overflows")
		}
		lastGranule = binary.LittleEndian.Uint64(data[off+6:])
		pages++
		off = end
	}
	if pages != 3 || lastGranule != 960 {
		t.Fatalf("invalid stream: pages=%d granule=%d", pages, lastGranule)
	}
}
func TestOTAHandlesActivationAndBoundToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Device-Id") == "" || r.Header.Get("Client-Id") == "" || r.Header.Get("Activation-Version") != "1" {
			t.Error("OTA report omitted required headers")
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["mac_address"] != r.Header.Get("Device-Id") {
			t.Error("OTA MAC mismatch")
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"activation":{"code":"123456"},"websocket":{"url":"wss://example.test"}}`))
		} else {
			_, _ = w.Write([]byte(`{"websocket":{"url":"wss://example.test","token":"private"}}`))
		}
	}))
	defer srv.Close()
	p, _ := New(Config{DeviceIDs: []string{"02:00:00:00:00:01"}, OTAURL: srv.URL})
	out, err := p.OTA(context.Background(), "02:00:00:00:00:01", "test")
	if err != nil || out.Activation == nil || out.Activation.Code != "123456" {
		t.Fatal("activation flow failed")
	}
	out, err = p.OTA(context.Background(), "02:00:00:00:00:01", "test")
	if err != nil || out.WebSocket.Token != "private" {
		t.Fatal("bound token missing")
	}
}
func TestAudioContainers(t *testing.T) {
	pcm := []byte{0, 0, 1, 0, 255, 127}
	wav := PCMToWAV(pcm)
	if string(wav[:4]) != "RIFF" || binary.LittleEndian.Uint32(wav[40:]) != uint32(len(pcm)) || len(wav) != 44+len(pcm) {
		t.Fatal("invalid PCM container")
	}
	if opusSamples([]byte{0xf8, 0xff, 0xfe}) != 960 {
		t.Fatal("Opus TOC duration is incorrect")
	}
}

func TestPCMNarrationPreservesNegotiatedFormatAndSampleRate(t *testing.T) {
	pcm := []byte{0, 0, 1, 0, 255, 127, 0, 128}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var msg map[string]any
		if c.ReadJSON(&msg) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "hello", "session_id": "pcm", "audio_params": map[string]any{"format": "pcm", "sample_rate": 24000}})
		for c.ReadJSON(&msg) == nil {
			if msg["state"] == "detect" {
				_ = c.WriteMessage(websocket.BinaryMessage, pcm)
				_ = c.WriteJSON(map[string]any{"type": "tts", "state": "stop"})
				return
			}
		}
	}))
	defer srv.Close()
	p, _ := New(Config{DeviceIDs: []string{"02:00:00:00:00:01"}, Token: "test", WSURL: strings.Replace(srv.URL, "http", "ws", 1)})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	audio, err := p.Synthesize(ctx, "pcm", "下一位请发言。")
	if err != nil {
		t.Fatal(err)
	}
	if string(audio[:4]) != "RIFF" || binary.LittleEndian.Uint32(audio[24:]) != 24000 || binary.LittleEndian.Uint32(audio[28:]) != 48000 || string(audio[44:]) != string(pcm) {
		t.Fatal("PCM must be emitted as WAV with its actual sample rate, not wrapped as Opus")
	}
}
