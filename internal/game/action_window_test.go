package game

import (
	"errors"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

// Existing rule fixtures explicitly finish the new playback protocol before
// exercising an action. Tests below also cover the deliberately unready state.
func acknowledgePlayback(t *testing.T, m *Manager, id string) {
	t.Helper()
	m.mu.Lock()
	r := m.rooms[id]
	b := backup(r)
	if err := m.commitLocked(r, b); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	uids := []string{}
	for _, p := range roomPlayers(r) {
		if !p.Bot {
			uids = append(uids, p.UserID)
		}
	}
	m.mu.Unlock()
	for _, uid := range uids {
		v, err := m.View(id, uid)
		if err != nil {
			t.Fatal(err)
		}
		if a := v.Action; a != nil && a.Required && !a.Ready && a.State == "waiting_playback" {
			if _, err = m.PlaybackReady(id, uid, a.ID, a.BoundaryID, "played"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func waitingTurn(t *testing.T) (*Manager, string, *testsupport.Rooms) {
	t.Helper()
	m, id, store := newLobby(t, CreateOptions{})
	_, _ = m.Join(id, "second", "新一", "")
	ready(t, m, id, "second")
	if _, err := m.Start(id, "host"); err != nil {
		t.Fatal(err)
	}
	m.tickAt(time.Now().Add(3 * time.Second))
	return m, id, store
}

func TestPlaybackWaitProtectsTheFullActionWindow(t *testing.T) {
	m, id, _ := waitingTurn(t)
	v, _ := m.View(id, "host")
	if v.Action == nil || v.Action.State != "waiting_playback" || v.Deadline != 0 {
		t.Fatal("human clock started before playback")
	}
	if _, err := m.Speak(id, "host", "它很常见。", nil); !errors.Is(err, ErrPlaybackPending) {
		t.Fatal("text bypassed playback gate", err)
	}
	if _, err := m.Skip(id, "host"); !errors.Is(err, ErrPlaybackPending) {
		t.Fatal("skip bypassed playback gate")
	}
	if _, err := m.AcquireHumanAgent(id, "host"); err == nil {
		t.Fatal("microphone bypassed playback gate")
	}
	m.tickAt(time.UnixMilli(v.Action.WaitUntil).Add(-time.Second))
	after, _ := m.View(id, "host")
	if after.CurrentPlayerID != v.CurrentPlayerID || after.Deadline != 0 {
		t.Fatal("slow playback consumed turn time")
	}
	started, err := m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "played")
	if err != nil || started.Action.State != "active" || started.Deadline < time.Now().Add(59*time.Second).UnixMilli() {
		t.Fatal("missing full action window", err)
	}
	duplicate, _ := m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "played")
	if duplicate.Deadline != started.Deadline {
		t.Fatal("duplicate acknowledgement reset deadline")
	}
	_, _ = m.Speak(id, "host", "它在日常生活里很常见。", nil)
	if _, err = m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "played"); !errors.Is(err, ErrStalePlayback) {
		t.Fatal("stale acknowledgement accepted")
	}
	if _, err = m.PlaybackReady(id, "second", v.Action.ID, v.Action.BoundaryID, "played"); !errors.Is(err, ErrStalePlayback) {
		t.Fatal("old action affected next player")
	}
}

func TestPlaybackTimeoutFallsBackWithoutSkippingThePlayer(t *testing.T) {
	m, id, _ := waitingTurn(t)
	v, _ := m.View(id, "host")
	now := time.UnixMilli(v.Action.WaitUntil).Add(time.Second)
	m.tickAt(now)
	after, _ := m.View(id, "host")
	if after.Action.State != "active" || !after.Action.TextOnly || after.CurrentPlayerID != v.CurrentPlayerID || after.Deadline != now.Add(actionTime).UnixMilli() {
		t.Fatal("timeout did not open a full text action window")
	}
	m.tickAt(now.Add(61 * time.Second))
	after, _ = m.View(id, "second")
	if after.CurrentPlayerID == v.CurrentPlayerID || after.Action.State != "waiting_playback" {
		t.Fatal("action timeout did not advance to protected next turn")
	}
}

func TestPlaybackAcknowledgementIsBoundToActorAndBoundary(t *testing.T) {
	m, id, _ := waitingTurn(t)
	v, _ := m.View(id, "host")
	if _, err := m.PlaybackReady(id, "second", v.Action.ID, v.Action.BoundaryID, "played"); err == nil {
		t.Fatal("wrong actor acknowledged")
	}
	if _, err := m.PlaybackReady(id, "host", v.Action.ID, "old-log", "played"); !errors.Is(err, ErrStalePlayback) {
		t.Fatal("wrong boundary accepted")
	}
	if _, err := m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "paused"); err == nil {
		t.Fatal("unknown completion mode accepted")
	}
	release, err := m.AcquireAgent(id, "host")
	if err != nil {
		t.Fatal(err)
	}
	v, err = m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "muted")
	if err != nil || v.Deadline != 0 || !v.Action.Ready {
		t.Fatal("clock began while server was busy")
	}
	release()
	v, _ = m.View(id, "host")
	if v.Action.State != "active" || !v.Action.TextOnly {
		t.Fatal("ready turn did not open on processing completion")
	}
	deadline := v.Deadline
	release, err = m.AcquireHumanAgent(id, "host")
	if err != nil {
		t.Fatal(err)
	}
	release()
	v, _ = m.View(id, "host")
	if v.Deadline != deadline {
		t.Fatal("read-only voice session reset deadline")
	}
}

func TestVotingWaitsForEligibleHumansAndDepartureCannotDeadlock(t *testing.T) {
	m, id, _ := waitingTurn(t)
	m.mu.Lock()
	r := m.rooms[id]
	b := backup(r)
	r.Game.Phase = "voting"
	r.Game.Votes = map[string]string{}
	r.Game.addLog("host", nil, "请投票。")
	if err := m.commitLocked(r, b); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	v, _ := m.View(id, "host")
	_, _ = m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "played")
	if _, err := m.Vote(id, "host", ""); !errors.Is(err, ErrPlaybackPending) {
		t.Fatal("voted before shared window opened")
	}
	if err := m.Leave(id, "second"); err != nil {
		t.Fatal(err)
	}
	v, _ = m.View(id, "host")
	if v.Action.State != "active" || v.Deadline == 0 {
		t.Fatal("departed voter blocked the barrier")
	}
}

func TestPlaybackWindowSurvivesRestartAndWriteFailures(t *testing.T) {
	m, id, store := waitingTurn(t)
	v, _ := m.View(id, "host")
	store.Fail = errors.New("database down")
	if _, err := m.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "played"); err == nil {
		t.Fatal("unpersisted acknowledgement succeeded")
	}
	store.Fail = nil
	restored, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := restored.View(id, "host")
	if after.Action.ID != v.Action.ID || after.Action.WaitUntil != v.Action.WaitUntil || after.Action.Ready || after.Deadline != 0 {
		t.Fatal("restart reset or activated waiting window")
	}
	active, _ := restored.PlaybackReady(id, "host", v.Action.ID, v.Action.BoundaryID, "unavailable")
	restored, err = NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ = restored.View(id, "host")
	if after.Deadline != active.Deadline || !after.Action.TextOnly {
		t.Fatal("restart changed active deadline")
	}
}

func TestProcessingGraceCannotExtendTheTurnIndefinitely(t *testing.T) {
	m, id, _ := waitingTurn(t)
	acknowledgePlayback(t, m, id)
	v, _ := m.View(id, "host")
	release, err := m.AcquireHumanAgent(id, "host")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	m.tickAt(time.UnixMilli(v.Deadline).Add(processingLimit + time.Second))
	after, _ := m.View(id, "host")
	if after.CurrentPlayerID == v.CurrentPlayerID {
		t.Fatal("hung processing blocked turn forever")
	}
}

func TestPlaybackBoundariesCoverAIChainVotingAndTiebreak(t *testing.T) {
	m, id, _ := waitingTurn(t)
	acknowledgePlayback(t, m, id)
	first, err := m.Speak(id, "host", "日常生活中很常见。", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := m.View(id, "second")
	if second.Action == nil || len(second.Action.LogIDs) != 2 || second.Action.LogIDs[0] != first.Logs[len(first.Logs)-2].ID {
		t.Fatal("next speaker did not wait for preceding human speech and prompt")
	}
	acknowledgePlayback(t, m, id)
	beforeAI, err := m.Speak(id, "second", "给人一种熟悉的感觉。", nil)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(action func(*Game) error) {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		r := m.rooms[id]
		b := backup(r)
		if err := action(r.Game); err != nil {
			t.Fatal(err)
		}
		if err := m.commitLocked(r, b); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		apply(func(g *Game) error { return g.Speak(g.CurrentID(), "它有自己的特点。", false) })
	}
	voting, _ := m.View(id, "host")
	if voting.Phase != "voting" || voting.Deadline != 0 || len(voting.Action.LogIDs) != 6 || voting.Action.LogIDs[0] != beforeAI.Logs[len(beforeAI.Logs)-2].ID {
		t.Fatal("voting lost preceding human/AI descriptions or started too soon")
	}
	if _, err = m.PlaybackReady(id, "host", voting.Action.ID, voting.Action.BoundaryID, "played"); err != nil {
		t.Fatal(err)
	}
	stillWaiting, _ := m.View(id, "host")
	if stillWaiting.Deadline != 0 || stillWaiting.Action.State != "waiting_playback" {
		t.Fatal("one listener opened the shared voting window")
	}
	active, err := m.PlaybackReady(id, "second", voting.Action.ID, voting.Action.BoundaryID, "unavailable")
	if err != nil || active.Deadline < time.Now().Add(59*time.Second).UnixMilli() {
		t.Fatal("last listener did not open a full voting window", err)
	}
	if _, err = m.Vote(id, "host", active.Players[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Vote(id, "second", active.Players[0].ID); err != nil {
		t.Fatal(err)
	}
	apply(func(g *Game) error { return g.Vote(g.Players[2].ID, g.Players[0].ID) })
	apply(func(g *Game) error { return g.Vote(g.Players[3].ID, g.Players[1].ID) })
	tiebreak, _ := m.View(id, "host")
	if tiebreak.Phase != "tiebreak" || tiebreak.Deadline != 0 || tiebreak.Action.ID == active.Action.ID || len(tiebreak.Action.LogIDs) != 3 {
		t.Fatal("tiebreak did not wait for tally, tie announcement and speaker prompt")
	}
	if _, err = m.Speak(id, "host", "补充一个特点。", nil); !errors.Is(err, ErrPlaybackPending) {
		t.Fatal("tiebreak bypassed playback")
	}
	acknowledgePlayback(t, m, id)
	if _, err = m.Speak(id, "host", "补充一个特点。", nil); err != nil {
		t.Fatal(err)
	}
	acknowledgePlayback(t, m, id)
	if _, err = m.Speak(id, "second", "我也补充一个特点。", nil); err != nil {
		t.Fatal(err)
	}
	acknowledgePlayback(t, m, id)
	_, _ = m.Vote(id, "host", "")
	_, _ = m.Vote(id, "second", "")
	apply(func(g *Game) error { return g.Vote(g.Players[2].ID, "") })
	apply(func(g *Game) error { return g.Vote(g.Players[3].ID, "") })
	nextRound, _ := m.View(id, "host")
	if nextRound.Round != 2 || nextRound.Deadline != 0 || len(nextRound.Action.LogIDs) != 3 {
		t.Fatal("next round started before tally and continuing-game prompt")
	}
}
