package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	view, err := a.rooms.View(id, u.ID)
	if err != nil {
		fail(w, 403, err)
		return
	}
	if !game.VoiceTurnAllowed(view) {
		fail(w, 409, errors.New("请等待自己的发言或投票回合，开局和再来一局仅房主可用"))
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
	release, err := a.rooms.AcquireHumanAgent(id, u.ID)
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
	humanVoice := recording != nil
	onTranscript := func(text string) {
		inputMu.Lock()
		input = strings.TrimSpace(text)
		inputMu.Unlock()
		if humanVoice && !readOnlyQuestion(text) && !explicitSkip(text) {
			if err := a.rooms.RememberSpeech(id, uid, text, recording()); err != nil {
				slog.Warn("save recognized speech draft", "room", id, "error", err)
			}
		}
	}
	tools := gameTools()
	if humanVoice {
		var humanTools []linx.Tool
		for _, tool := range tools {
			if tool.Name != "game.get_ai_batch" && tool.Name != "game.submit_ai_batch" {
				humanTools = append(humanTools, tool)
			}
		}
		tools = humanTools
	}
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
	roster := []string{}
	for _, player := range public.(game.View).Players {
		kind := "真人"
		if player.Bot {
			kind = "AI玩家"
		}
		roster = append(roster, fmt.Sprintf("%d号：%s（%s）", player.Seat, player.Name, kind))
	}
	instructions := hostInstructions + "本桌真实座位：" + strings.Join(roster, "；") + "。不得使用本桌以外的人名。"
	return a.voice.OpenWithTools(ctx, uid+"/assistant", linx.SessionOptions{
		Tools: tools, Call: a.gameToolHandler(id, uid, recording, getInput),
		Prompt:       map[string]string{"game_context": string(contextJSON), "game_instructions": instructions},
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
	before, err := a.rooms.View(id, uid)
	if err != nil {
		return err
	}
	startedAt := time.Now()
	rulesRequested := false
	for {
		kind, data, err := upstream.Conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Linx assistant conversation ended", "error", err)
			}
			_ = emit(map[string]string{"type": "error", "message": "灵矽对话未完成；已识别的发言会保留为草稿，可确认提交"})
			return errors.New("assistant conversation interrupted")
		}
		// Free-form model speech is never forwarded, cached or published.
		// Tool actions and read-only replies both use verified public text.
		if kind != websocket.TextMessage {
			continue
		}
		// Transcript preservation changes the version too; only compare the
		// snapshot immediately surrounding this tool call.
		before, err = a.rooms.View(id, uid)
		if err != nil {
			return err
		}
		if handled, err := upstream.HandleMCP(ctx, data); handled {
			if err != nil {
				return err
			}
			after, err := a.rooms.View(id, uid)
			if err != nil {
				return err
			}
			if after.Version != before.Version && gameActionCall(data) {
				return a.narrateAction(ctx, id, uid, upstream, before, after, output)
			}
			var call struct {
				Payload struct{ Params struct{ Name string } }
			}
			_ = json.Unmarshal(data, &call)
			if call.Payload.Params.Name == "game.get_rules" {
				rulesRequested = true
			}
			before = after
			continue
		}
		var message linx.Message
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if message.Type == "stt" && strings.TrimSpace(message.Text) != "" {
			upstream.NoteTranscript(message.Text)
			slog.Info("Linx speech recognized", "room", id, "characters", utf8.RuneCountInString(message.Text), "elapsed_ms", time.Since(startedAt).Milliseconds())
			_ = emit(map[string]string{"type": "transcript", "text": message.Text})
			continue
		}
		if message.Type != "tts" || message.State != "stop" || message.IsAborted {
			continue
		}
		text, err := a.rooms.StatusText(id, uid)
		if err != nil {
			return err
		}
		view, err := a.rooms.View(id, uid)
		if err != nil {
			return err
		}
		if view.MyDraft != "" {
			for _, player := range view.Players {
				if player.ID == view.MyPlayerID {
					text = fmt.Sprintf("请%d号确认提交发言。", player.Seat)
				}
			}
		}
		if rulesRequested {
			text = gameRules
		}
		log := game.Log{ID: game.ID(), Kind: "host", Text: text, Source: "linx_agent"}
		audio, synthesisErr := upstream.SynthesizeText(ctx, text)
		if synthesisErr == nil {
			a.cacheAudio(id+"/"+log.ID, audio)
		} else {
			slog.Warn("Linx state narration failed", "room", id, "error", synthesisErr)
		}
		if err := a.rooms.AppendHostLog(id, uid, log); err != nil {
			return err
		}
		return emit(map[string]string{"type": "assistant_done", "id": log.ID, "text": text, "streamed": "false"})
	}
}

func gameActionCall(data []byte) bool {
	var message struct {
		Payload struct {
			Method string
			Params struct{ Name string }
		}
	}
	if json.Unmarshal(data, &message) != nil || message.Payload.Method != "tools/call" {
		return false
	}
	switch message.Payload.Params.Name {
	case "game.start", "game.submit_description", "game.vote", "game.skip", "game.submit_ai_batch":
		return true
	}
	return false
}

func (a *App) narrateAction(ctx context.Context, id, uid string, upstream *linx.Session, before, after game.View, output assistantOutput) error {
	known := map[string]bool{}
	if before.GameID == after.GameID {
		for _, log := range before.Logs {
			known[log.ID] = true
		}
	}
	// Cancel the free-form follow-up. The same Linx terminal now speaks only
	// committed public lines, so internal model commentary is never published.
	for _, log := range after.Logs {
		if known[log.ID] || log.Source == "linx_agent" {
			continue
		}
		_, recording, err := a.rooms.Event(id, uid, log.ID)
		if err != nil {
			return err
		}
		// All listeners, including the speaker, replay the original.
		if len(recording) > 0 {
			continue
		}
		text := log.Text
		if log.Kind == "speech" {
			text = fmt.Sprintf("%d号：%s", log.Seat, text)
		}
		audio, err := upstream.SynthesizeText(ctx, text)
		if err != nil {
			slog.Warn("Linx committed narration failed", "room", id, "event", log.ID, "error", err)
			break // The ordered HTTP narration queue can retry this exact public line.
		}
		a.cacheAudio(id+"/"+log.ID, audio)
	}
	if output != nil {
		return output.WriteJSON(map[string]string{"type": "assistant_done", "streamed": "false"})
	}
	return nil
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
