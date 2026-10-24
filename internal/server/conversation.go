package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/gorilla/websocket"
)

// assistantVoice keeps the entire Linx ASR -> model -> MCP -> TTS exchange alive.
// All tools are bound to this room and authenticated human; the browser never
// supplies an upstream address, a user ID, device credentials or arbitrary tools.
func (a *App) assistantVoice(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id := r.PathValue("id")
	if _, err := a.rooms.View(id, u.ID); err != nil {
		fail(w, 403, err)
		return
	}
	if !a.voice.Configured() {
		fail(w, 503, errors.New("请先绑定灵矽软件终端"))
		return
	}
	upgrader := websocket.Upgrader{HandshakeTimeout: 8 * time.Second, CheckOrigin: sameOrigin}
	front, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer front.Close()
	front.SetReadLimit(4096)
	ctx, cancel := a.roomContext(r.Context(), id, 100*time.Second)
	defer cancel()
	release, err := a.rooms.AcquireAgent(id, u.ID)
	if err != nil {
		_ = front.WriteJSON(map[string]string{"type": "error", "message": err.Error()})
		return
	}
	defer release()
	var audioMu sync.Mutex
	var recording []byte
	getRecording := func() []byte { audioMu.Lock(); defer audioMu.Unlock(); return append([]byte(nil), recording...) }
	connectedAt := time.Now()
	upstream, err := a.openGameAgent(ctx, id, u.ID, getRecording)
	if err != nil {
		slog.Warn("Linx assistant connection failed", "error", err)
		_ = front.WriteJSON(map[string]string{"type": "error", "message": "灵矽主持连接失败，稍后重试"})
		return
	}
	defer upstream.Close()
	slog.Info("Linx assistant ready", "room", id, "connect_ms", time.Since(connectedAt).Milliseconds())
	_ = upstream.WriteJSON(map[string]any{"type": "abort", "session_id": upstream.SessionID})
	if upstream.Listen("start") != nil {
		return
	}
	if front.WriteJSON(map[string]string{"type": "ready", "audioFormat": upstream.AudioFormat, "sampleRate": strconv.Itoa(upstream.SampleRate)}) != nil {
		return
	}
	inputErr := make(chan error, 1)
	go func() {
		defer cancel()
		stopped := false
		for {
			kind, b, e := front.ReadMessage()
			if e != nil {
				inputErr <- e
				return
			}
			if kind == websocket.BinaryMessage {
				if stopped || len(b) > 1280 || len(b)%2 != 0 {
					inputErr <- errors.New("invalid audio frame")
					return
				}
				audioMu.Lock()
				recording = append(recording, b...)
				size := len(recording)
				audioMu.Unlock()
				if size > 960000 {
					inputErr <- errors.New("recording too long")
					return
				}
				if e = upstream.WritePCM(b); e != nil {
					inputErr <- e
					return
				}
			} else {
				var control struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(b, &control) != nil {
					inputErr <- errors.New("invalid control")
					return
				}
				if control.Type == "cancel" {
					inputErr <- context.Canceled
					return
				}
				if control.Type == "stop" && !stopped {
					stopped = true
					if len(getRecording()) < 640 {
						inputErr <- errors.New("recording too short")
						return
					}
					if e = upstream.Listen("stop"); e != nil {
						inputErr <- e
						return
					}
				}
			}
		}
	}()
	_ = a.receiveAssistant(ctx, id, u.ID, upstream, front)
}

type assistantOutput interface {
	WriteJSON(any) error
	WriteMessage(int, []byte) error
}

func (a *App) openGameAgent(ctx context.Context, id, uid string, recording func() []byte) (*linx.Session, error) {
	public, err := a.publicAgentState(id, uid)
	if err != nil {
		return nil, err
	}
	contextJSON, _ := json.Marshal(public)
	// This input belongs to this connection and authenticated actor only.
	var inputMu sync.Mutex
	var input string
	getInput := func() string { inputMu.Lock(); defer inputMu.Unlock(); return input }
	onTranscript := func(text string) { inputMu.Lock(); input = strings.TrimSpace(text); inputMu.Unlock() }
	tools := gameTools()
	if recording == nil {
		// A synthetic continuation is not a human utterance or authorization.
		getInput = nil
		onTranscript = nil
		recording = func() []byte { return nil }
		var automatic []linx.Tool
		for _, tool := range tools {
			switch tool.Name {
			case "game.get_state", "game.get_rules", "game.get_ai_batch", "game.submit_ai_batch":
				automatic = append(automatic, tool)
			}
		}
		tools = automatic
	}
	return a.voice.OpenWithTools(ctx, uid+"/assistant", linx.SessionOptions{
		Tools: tools, Call: a.gameToolHandler(id, uid, recording, getInput),
		Prompt:       map[string]string{"game_context": string(contextJSON), "game_instructions": hostInstructions},
		OnTranscript: onTranscript,
	})
}
func (a *App) receiveAssistant(ctx context.Context, id, uid string, upstream *linx.Session, output assistantOutput) error {
	emit := func(value any) error {
		if output != nil {
			return output.WriteJSON(value)
		}
		return nil
	}
	startedAt := time.Now()
	var packets [][]byte
	var response strings.Builder
	size := 0
	streamed := false
	sentenceAllowed := false
	cues := []game.AudioCue{}
	durationMS := int64(0)
	for {
		kind, b, e := upstream.Conn.ReadMessage()
		if e != nil {
			if ctx.Err() == nil {
				slog.Warn("Linx assistant conversation ended", "error", e)
			}
			_ = emit(map[string]string{"type": "error", "message": "灵矽对话未完成，已执行的游戏操作可在圆桌查看，请重试"})
			return errors.New("assistant conversation interrupted")
		}
		if kind == websocket.BinaryMessage {
			size += len(b)
			if size > 8<<20 {
				return errors.New("assistant conversation interrupted")
			}
			packets = append(packets, append([]byte(nil), b...))
			if upstream.AudioFormat == "pcm" {
				durationMS = int64(size) * 1000 / int64(upstream.SampleRate*2)
			} else {
				durationMS += linx.OpusPacketDurationMS(b)
			}
			if output != nil && upstream.AudioFormat == "pcm" && sentenceAllowed {
				if output.WriteMessage(websocket.BinaryMessage, b) != nil {
					return errors.New("assistant conversation interrupted")
				}
				streamed = true
			}
			continue
		}
		if handled, e := upstream.HandleMCP(ctx, b); handled {
			if e != nil {
				return errors.New("assistant conversation interrupted")
			}
			continue
		}
		var msg linx.Message
		if json.Unmarshal(b, &msg) != nil {
			continue
		}
		if msg.Type == "stt" && strings.TrimSpace(msg.Text) != "" {
			upstream.NoteTranscript(msg.Text)
			slog.Info("Linx speech recognized", "room", id, "characters", utf8.RuneCountInString(msg.Text), "elapsed_ms", time.Since(startedAt).Milliseconds())
			_ = emit(map[string]string{"type": "transcript", "text": msg.Text})
			continue
		}
		if msg.Type == "tts" && msg.State == "sentence_start" {
			if response.Len() < 6000 {
				response.WriteString(msg.Text)
			}
			safe, e := a.rooms.SafeHostText(id, uid, msg.Text)
			sentenceAllowed = e == nil && safe == msg.Text
			if sentenceAllowed {
				cue := a.sentenceCue(id, uid, msg.Text, durationMS)
				cues = append(cues, cue)
				_ = emit(map[string]string{"type": "assistant_sentence", "text": cue.Text, "logId": cue.LogID, "playerId": cue.PlayerID, "seat": strconv.Itoa(cue.Seat), "offsetMs": strconv.FormatInt(cue.OffsetMS, 10)})
			}
			continue
		}
		if msg.Type == "tts" && msg.State == "stop" && msg.IsAborted {
			packets = nil
			size = 0
			response.Reset()
			cues = nil
			durationMS = 0
			sentenceAllowed = false
			if streamed {
				_ = emit(map[string]string{"type": "reset_audio"})
				streamed = false
			}
			continue
		}
		if msg.Type != "tts" || msg.State != "stop" {
			continue
		}
		if len(packets) == 0 && response.Len() == 0 {
			continue
		}
		text := strings.TrimSpace(response.String())
		if text == "" {
			text = "灵矽主持已回复，请收听语音。"
		}
		safe, e := a.rooms.SafeHostText(id, uid, text)
		if e != nil {
			return errors.New("assistant conversation interrupted")
		}
		upstream.Close()
		var audio []byte
		if safe != text {
			_ = emit(map[string]string{"type": "reset_audio"})
			streamed = false
			cues = nil
			audio, e = a.voice.Synthesize(ctx, "safe-host-reply", safe)
			if e != nil {
				audio = nil
			}
			text = safe
		} else if upstream.AudioFormat == "pcm" {
			audio = linx.PCMToWAVAtRate(bytes.Join(packets, nil), upstream.SampleRate)
		} else if upstream.AudioFormat == "opus" {
			audio = linx.OpusOgg(packets, upstream.SampleRate)
		}
		l := game.Log{ID: game.ID(), Kind: "host", Text: text, Source: "linx_agent", AudioCues: cues}
		if len(audio) > 0 {
			a.cacheAudio(id+"/"+l.ID, audio)
		}
		if streamed {
			_ = emit(map[string]string{"type": "assistant_reply_id", "id": l.ID})
		}
		if e = a.rooms.AppendHostLog(id, uid, l); e != nil {
			_ = emit(map[string]string{"type": "error", "message": "主持回复保存失败，请重试"})
			return errors.New("assistant conversation interrupted")
		}
		_ = emit(map[string]string{"type": "assistant_done", "text": text, "id": l.ID, "streamed": strconv.FormatBool(streamed)})
		return nil
	}
}

func (a *App) cacheAudio(key string, data []byte) {
	id, _, _ := strings.Cut(key, "/")
	done := a.rooms.Done(id)
	a.audioMu.Lock()
	defer a.audioMu.Unlock()
	select {
	case <-done:
		return
	default:
	}
	for a.audioBytes+len(data) > 32<<20 && len(a.audioCache) > 0 {
		for k, b := range a.audioCache {
			a.audioBytes -= len(b)
			delete(a.audioCache, k)
			break
		}
	}
	if previous := a.audioCache[key]; previous != nil {
		a.audioBytes -= len(previous)
	}
	a.audioCache[key] = data
	a.audioBytes += len(data)
}
