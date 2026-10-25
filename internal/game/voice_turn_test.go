package game

import (
	"testing"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestHumanVoiceLeaseChecksCurrentTurnWithoutBlockingAutomaticAI(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	_, _ = m.Join("room-1", "host", "房主", "")
	_, _ = m.Join("room-1", "second", "新一", "")
	if _, err := m.AcquireHumanAgent("room-1", "second"); err == nil {
		t.Fatal("non-host could use the start microphone")
	}
	_, _ = m.SetReady("room-1", "second", true)
	_, _ = m.Start("room-1", "host")
	m.mu.Lock()
	m.rooms["room-1"].Game.Begin()
	m.mu.Unlock()
	if _, err := m.AcquireHumanAgent("room-1", "second"); err == nil {
		t.Fatal("player 2 could interrupt player 1")
	}
	release, err := m.AcquireHumanAgent("room-1", "host")
	if err != nil {
		t.Fatal(err)
	}
	release()
	acknowledgePlayback(t, m, "room-1")
	_, _ = m.Speak("room-1", "host", "这是在日常生活中可以见到的东西。", nil)
	if _, err := m.AcquireHumanAgent("room-1", "host"); err == nil {
		t.Fatal("player 1 could interrupt player 2")
	}
	acknowledgePlayback(t, m, "room-1")
	release, err = m.AcquireHumanAgent("room-1", "second")
	if err != nil {
		t.Fatal("player 2 could not use their own turn")
	}
	release()
	// Background AI leases are independent of the selected human user's turn.
	release, err = m.AcquireAgent("room-1", "host")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestVoiceTurnAccessForVotingEliminationAndHostRestart(t *testing.T) {
	view := View{Summary: Summary{Phase: "voting", Players: []PublicPlayer{{ID: "me", Alive: true}}}, MyPlayerID: "me"}
	if !VoiceTurnAllowed(view) {
		t.Fatal("eligible voter could not use microphone")
	}
	view.Players[0].HasVoted = true
	if VoiceTurnAllowed(view) {
		t.Fatal("already-voted player could interrupt")
	}
	view.Players[0].HasVoted = false
	view.Players[0].Alive = false
	if VoiceTurnAllowed(view) {
		t.Fatal("eliminated player could vote")
	}
	view.Phase = "finished"
	view.IsHost = true
	if !VoiceTurnAllowed(view) {
		t.Fatal("eliminated host could not restart")
	}
	view.IsHost = false
	if VoiceTurnAllowed(view) {
		t.Fatal("non-host could restart")
	}
	view.Phase = "dealing"
	view.IsHost = true
	if VoiceTurnAllowed(view) {
		t.Fatal("player could interrupt dealing")
	}
}
