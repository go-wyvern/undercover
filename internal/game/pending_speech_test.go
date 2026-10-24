package game

import (
	"bytes"
	"encoding/json"
	"github.com/go-wyvern/undercover/internal/testsupport"
	"strings"
	"testing"
	"time"
)

func TestRecognizedSpeechStaysPrivateSurvivesReloadAndCannotTimeoutIntoSkip(t *testing.T) {
	store := testsupport.NewRooms(true)
	m, _ := NewManager(store, nil)
	_, _ = m.Join("room-1", "host", "房主", "")
	_, _ = m.Join("room-1", "other", "新一", "")
	_, _ = m.Start("room-1", "host")
	m.mu.Lock()
	m.rooms["room-1"].Game.Begin()
	m.mu.Unlock()
	text := "这是在水面下面进行的活动。"
	pcm := bytes.Repeat([]byte{7, 0}, 320)
	if err := m.RememberSpeech("room-1", "host", text, pcm); err != nil {
		t.Fatal(err)
	}
	own, _ := m.View("room-1", "host")
	other, _ := m.View("room-1", "other")
	if own.MyDraft != text || own.Deadline != 0 || other.MyDraft != "" {
		t.Fatal("missing private pending description")
	}
	encoded, _ := json.Marshal(other)
	if strings.Contains(string(encoded), text) {
		t.Fatal("unsubmitted original leaked to another player")
	}
	m.mu.Lock()
	m.rooms["room-1"].Game.Deadline = time.Now().Add(-time.Minute).UnixMilli()
	m.mu.Unlock()
	m.tick()
	own, _ = m.View("room-1", "host")
	if own.CurrentPlayerID != own.MyPlayerID {
		t.Fatal("recognized speech timed out into skip")
	}
	for _, log := range own.Logs {
		if strings.Contains(log.Text, "跳过") {
			t.Fatal("pending description was skipped")
		}
	}
	restored, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	reload, _ := restored.View("room-1", "host")
	if reload.MyDraft != text {
		t.Fatal("pending original lost after restart")
	}
	submitted, err := m.Speak("room-1", "host", text, nil)
	if err != nil {
		t.Fatal(err)
	}
	if submitted.MyDraft != "" || submitted.CurrentPlayerID != submitted.Players[1].ID {
		t.Fatal("confirmation did not commit exactly one turn")
	}
	for _, log := range submitted.Logs {
		if log.Kind == "speech" {
			_, recording, _ := m.Event("room-1", "host", log.ID)
			if !bytes.Equal(recording, pcm) {
				t.Fatal("confirmation lost original recording")
			}
			return
		}
	}
	t.Fatal("confirmation produced no player description")
}

func TestAnEmptyHumanTurnStillExpiresWithAnExplicitTimeoutReason(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	_, _ = m.Join("room-1", "host", "房主", "")
	_, _ = m.Start("room-1", "host")
	m.mu.Lock()
	g := m.rooms["room-1"].Game
	g.Begin()
	g.Deadline = time.Now().Add(-time.Minute).UnixMilli()
	m.mu.Unlock()
	m.tick()
	view, _ := m.View("room-1", "host")
	found := false
	for _, log := range view.Logs {
		if log.Text == "1号超时跳过。" {
			found = true
		}
	}
	if !found {
		t.Fatal("timeout was indistinguishable from a requested skip")
	}
}
