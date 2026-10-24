package server

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/go-wyvern/undercover/internal/linx"
)

// Automatic turns use the same Linx voice agent and game tools as a human
// conversation. A cached spoken continuation cue is used because the published
// device protocol accepts audio, rather than a chat-completions HTTP endpoint.
func (a *App) runAgentTurns(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	pending := map[string]bool{}
	retryAfter := map[string]time.Time{}
	var mu sync.Mutex
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for _, turn := range a.rooms.PendingAgentTurns() {
			mu.Lock()
			if pending[turn.RoomID] || time.Now().Before(retryAfter[turn.RoomID]) {
				mu.Unlock()
				continue
			}
			pending[turn.RoomID] = true
			mu.Unlock()
			go func(id, uid string) {
				err := a.advanceAgentTurn(ctx, id, uid)
				mu.Lock()
				delete(pending, id)
				if err != nil {
					retryAfter[id] = time.Now().Add(10 * time.Second)
				}
				mu.Unlock()
				if err != nil && ctx.Err() == nil {
					slog.Warn("Linx automatic turn failed", "room", id, "error", err)
				}
			}(turn.RoomID, turn.UserID)
		}
	}
}
func (a *App) continuationAudio(ctx context.Context) ([]byte, error) {
	a.commandMu.Lock()
	defer a.commandMu.Unlock()
	if len(a.commandPCM) > 0 {
		return a.commandPCM, nil
	}
	var audio []byte
	var err error
	for {
		audio, err = a.voice.Synthesize(ctx, "continue-game", "继续主持，请处理当前的AI回合，等待真人操作。")
		if !errors.Is(err, linx.ErrBusy) {
			break
		}
		if err = waitAgentChannel(ctx); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	pcm, err := wavTo16K(audio)
	if err != nil {
		return nil, err
	}
	a.commandPCM = pcm
	return pcm, nil
}
func (a *App) advanceAgentTurn(parent context.Context, id, uid string) error {
	release, err := a.rooms.AcquireAgent(id, uid)
	if err != nil {
		return nil
	}
	defer release()
	ctx, cancel := a.roomContext(parent, id, 100*time.Second)
	defer cancel()
	pcm, err := a.continuationAudio(ctx)
	if err != nil {
		return err
	}
	var session *linx.Session
	for {
		session, err = a.openGameAgent(ctx, id, uid, func() []byte { return nil })
		if !errors.Is(err, linx.ErrBusy) {
			break
		}
		if err = waitAgentChannel(ctx); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	defer session.Close()
	_ = session.WriteJSON(map[string]any{"type": "abort", "session_id": session.SessionID})
	if err = session.Listen("start"); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for pos := 0; pos < len(pcm); pos += 640 {
			end := pos + 640
			if end > len(pcm) {
				end = len(pcm)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if session.WritePCM(pcm[pos:end]) != nil {
				cancel()
				return
			}
		}
		if session.Listen("stop") != nil {
			cancel()
		}
	}()
	return a.receiveAssistant(ctx, id, uid, session, nil)
}
func wavTo16K(b []byte) ([]byte, error) {
	if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("Linx continuation requires PCM WAV output")
	}
	rate, channels, bits, format := 0, 0, 0, 0
	var pcm []byte
	for pos := 12; pos+8 <= len(b); {
		size := int(binary.LittleEndian.Uint32(b[pos+4:]))
		start := pos + 8
		end := start + size
		if end > len(b) {
			return nil, errors.New("invalid WAV size")
		}
		switch string(b[pos : pos+4]) {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("invalid WAV format")
			}
			format = int(binary.LittleEndian.Uint16(b[start:]))
			channels = int(binary.LittleEndian.Uint16(b[start+2:]))
			rate = int(binary.LittleEndian.Uint32(b[start+4:]))
			bits = int(binary.LittleEndian.Uint16(b[start+14:]))
		case "data":
			pcm = b[start:end]
		}
		pos = end + (size % 2)
	}
	if format != 1 || channels != 1 || bits != 16 || rate < 8000 || rate > 96000 || len(pcm)%2 != 0 {
		return nil, errors.New("unsupported continuation audio format")
	}
	if rate == 16000 {
		return append([]byte(nil), pcm...), nil
	}
	samples := len(pcm) / 2
	count := samples * 16000 / rate
	out := make([]byte, count*2)
	for i := 0; i < count; i++ {
		index := i * rate / 16000
		if index >= samples {
			index = samples - 1
		}
		copy(out[i*2:i*2+2], pcm[index*2:index*2+2])
	}
	return out, nil
}

func waitAgentChannel(ctx context.Context) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
