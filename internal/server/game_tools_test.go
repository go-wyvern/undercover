package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestGameToolsRespectActorAndDriveAIWithoutPublicSecrets(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.rooms.Join("room-1", "host", "房主", "https://account.example/host.png")
	_, _ = a.rooms.Join("room-1", "other", "同桌", "")
	release, err := a.rooms.AcquireAgent("room-1", "host")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	handler := a.gameToolHandler("room-1", "host", func() []byte { return nil }, nil)
	other := a.gameToolHandler("room-1", "other", func() []byte { return nil }, nil)
	if _, err = other(context.Background(), "game.start", json.RawMessage(`{}`)); err == nil {
		t.Fatal("non-host used start tool")
	}
	if _, err = handler(context.Background(), "game.start", json.RawMessage(`{"roomId":"other-room"}`)); err == nil {
		t.Fatal("tool accepted a client-selected room")
	}
	v, err := handler(context.Background(), "game.start", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "https://account.example/host.png") {
		t.Fatal("host model received irrelevant account avatar data")
	}
	if strings.Contains(string(raw), "myWord") || strings.Contains(string(raw), "undercoverId") || strings.Contains(string(raw), "userId") {
		t.Fatal("host tool leaked a human secret")
	}
	if _, err = other(context.Background(), "game.submit_description", json.RawMessage(`{"text":"还没有轮到我。"}`)); err == nil {
		t.Fatal("out-of-turn human used speech tool")
	}
	if _, err = handler(context.Background(), "game.submit_description", json.RawMessage(`{"text":"它在日常生活里很常见。"}`)); err != nil {
		t.Fatal(err)
	}
	_, _ = a.rooms.Skip("room-1", "other")
	batch, err := a.rooms.NextAIBatch("room-1", "host")
	if err != nil || batch == nil {
		t.Fatal("missing AI task")
	}

	firstBatch := batch
	for batch != nil {
		actions := []game.AIAction{}
		for _, task := range batch.Tasks {
			actions = append(actions, game.AIAction{PlayerSeat: task.Seat, Text: "它在生活的许多场景中都有用处。"})
		}
		args, _ := json.Marshal(map[string]any{"batchId": batch.BatchID, "actions": actions})
		if _, err = handler(context.Background(), "game.submit_ai_batch", args); err != nil {
			t.Fatal(err)
		}
		if _, err = handler(context.Background(), "game.submit_ai_batch", args); err == nil {
			t.Fatal("replayed AI task executed twice")
		}
		batch, _ = a.rooms.NextAIBatch("room-1", "host")
	}
	batch = firstBatch

	// The host may see an AI's own task, but the public audio must not reveal its word.
	safe, err := a.rooms.SafeHostText("room-1", "host", "它的词是"+batch.Tasks[0].Word)
	if err != nil || strings.Contains(safe, batch.Tasks[0].Word) {
		t.Fatal("private AI task word escaped in host narration")
	}
	view, _ := a.rooms.View("room-1", "host")
	if !view.AgentActive {
		t.Fatal("room did not expose active host conversation")
	}
}
func TestAgentReplyBeforeGame(t *testing.T) {
	m, _ := game.NewManager(testsupport.NewRooms(true), nil)
	_, _ = m.Join("room-1", "u", "玩家", "")
	if err := m.AppendHostLog("room-1", "u", game.Log{ID: "welcome", Text: "欢迎来到这一桌。"}); err != nil {
		t.Fatal(err)
	}
	l, _, err := m.Event("room-1", "u", "welcome")
	if err != nil || l.Source != "linx_agent" {
		t.Fatal("pre-game host reply unavailable")
	}
	if _, _, err = m.Event("room-1", "outsider", "welcome"); err == nil {
		t.Fatal("outsider accessed private room audio")
	}
}

func TestVoiceDescriptionUsesRecognizedOriginalAndKeepsItInThisSession(t *testing.T) {
	a, err := newTestApp(Config{Auth: auth.Config{AllowGuest: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.rooms.Join("room-1", "host", "房主", "")
	input := "它在生活里很常见，可以用来传递消息。"
	handler := a.gameToolHandler("room-1", "host", func() []byte { return nil }, func() string { return input })
	_, err = handler(context.Background(), "game.start", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	state, err := handler(context.Background(), "game.get_state", json.RawMessage(`{}`))
	if err != nil || state.(map[string]any)["currentUtterance"] != input {
		t.Fatal("model lost this utterance")
	}
	otherSession := a.gameToolHandler("room-1", "host", func() []byte { return nil }, nil)
	otherState, _ := otherSession(context.Background(), "game.get_state", json.RawMessage(`{}`))
	if otherState.(map[string]any)["currentUtterance"] != "" {
		t.Fatal("utterance leaked to another conversation")
	}
	_, err = handler(context.Background(), "game.submit_description", json.RawMessage(`{"text":"模型改写的另一句话。"}`))
	if err != nil {
		t.Fatal(err)
	}
	view, _ := a.rooms.View("room-1", "host")
	found := false
	for _, log := range view.Logs {
		if log.Kind == "speech" && log.PlayerID == view.MyPlayerID {
			found = true
			if log.Text != input {
				t.Fatalf("speech was replaced: %q", log.Text)
			}
		}
	}
	if !found {
		t.Fatal("recognized speech was not saved")
	}
}
