package game

import (
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestExternalAgentSchedulesOnlyPendingAITurns(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	m.UseExternalAgent()
	_, _ = m.Join("room-1", "u", "玩家", "")
	_, _ = m.Start("room-1", "u")
	m.mu.Lock()
	g := m.rooms["room-1"].Game
	g.Begin()
	m.mu.Unlock()
	if len(m.PendingAgentTurns()) != 0 {
		t.Fatal("scheduled an agent for human turn")
	}
	_, _ = m.Speak("room-1", "u", "日常生活中有时候会看到它。", nil)
	turns := m.PendingAgentTurns()
	if len(turns) != 1 || turns[0].RoomID != "room-1" || turns[0].UserID != "u" {
		t.Fatal("AI was not scheduled for room member")
	}
	m.mu.Lock()
	g = m.rooms["room-1"].Game
	g.Logs[len(g.Logs)-1].At = time.Now().Add(-time.Minute).UnixMilli()
	m.mu.Unlock()
	m.tick()
	if len(m.jobs) != 0 {
		t.Fatal("external agent mode scheduled a demo bot")
	}
	release, err := m.AcquireAgent("room-1", "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.PendingAgentTurns()) != 0 {
		t.Fatal("scheduled duplicate active agent")
	}
	release()
	if len(m.PendingAgentTurns()) != 1 {
		t.Fatal("lost pending agent after release")
	}
}
