package game

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func fixture(t *testing.T) *Game {
	t.Helper()
	g, err := New([]Player{{ID: "a", UserID: "u1", Name: "甲", Seat: 1}, {ID: "b", UserID: "u2", Name: "乙", Seat: 2}, {ID: "c", UserID: "u3", Name: "丙", Seat: 3}, {ID: "d", UserID: "u4", Name: "丁", Seat: 4}})
	if err != nil {
		t.Fatal(err)
	}
	g.UndercoverID = "d"
	g.Words = map[string]string{"a": "太阳", "b": "太阳", "c": "太阳", "d": "月亮"}
	g.Begin()
	return g
}
func describeAll(t *testing.T, g *Game) {
	t.Helper()
	for g.CurrentID() != "" {
		if err := g.Speak(g.CurrentID(), "它在日常生活里有很重要的位置。", false); err != nil {
			t.Fatal(err)
		}
	}
}
func votes(t *testing.T, g *Game, pairs [][2]string) {
	t.Helper()
	for _, p := range pairs {
		if err := g.Vote(p[0], p[1]); err != nil {
			t.Fatalf("%s -> %s: %v", p[0], p[1], err)
		}
	}
}
func TestRulesRejectInvalidAndDuplicateActions(t *testing.T) {
	g := fixture(t)
	before, _ := json.Marshal(g)
	for _, err := range []error{g.Speak("b", "现在不该轮到我。", false), g.Speak("a", "我的词是太阳", false), g.Vote("a", "b")} {
		if err == nil {
			t.Fatal("accepted an invalid action")
		}
	}
	after, _ := json.Marshal(g)
	if string(before) != string(after) {
		t.Fatal("invalid actions mutated game")
	}
	describeAll(t, g)
	if err := g.Vote("a", "a"); err == nil {
		t.Fatal("self vote accepted")
	}
	if err := g.Vote("a", "missing"); err == nil {
		t.Fatal("unknown target accepted")
	}
	if err := g.Vote("a", "d"); err != nil {
		t.Fatal(err)
	}
	if err := g.Vote("a", "b"); err == nil {
		t.Fatal("duplicate vote accepted")
	}
	if len(g.Votes) != 1 || g.Votes["a"] != "d" {
		t.Fatal("duplicate vote changed tally")
	}
}
func TestCivilianVictoryAndRevealOnlyAtEnd(t *testing.T) {
	g := fixture(t)
	if len(g.Reveals()) != 0 {
		t.Fatal("early reveal")
	}
	describeAll(t, g)
	votes(t, g, [][2]string{{"a", "d"}, {"b", "d"}, {"c", "d"}, {"d", "a"}})
	if g.Phase != "finished" || g.Winner != "civilian" || g.Player("d").Alive {
		t.Fatalf("wrong outcome: %#v", g)
	}
	if len(g.Reveals()) != 4 || g.Reveals()[3].Word != "月亮" {
		t.Fatal("incorrect final reveal")
	}
	if err := g.Vote("a", "b"); err == nil {
		t.Fatal("voted after final result")
	}
}
func TestUndercoverSurvivesToTwoAndEliminatedCannotVote(t *testing.T) {
	g := fixture(t)
	describeAll(t, g)
	votes(t, g, [][2]string{{"a", "b"}, {"b", "a"}, {"c", "b"}, {"d", "b"}})
	if g.Player("b").Alive || g.Round != 2 || g.Winner != "" {
		t.Fatal("first civilian elimination failed")
	}
	describeAll(t, g)
	if err := g.Vote("b", "d"); err == nil {
		t.Fatal("eliminated player voted")
	}
	votes(t, g, [][2]string{{"a", "c"}, {"c", "a"}, {"d", "c"}})
	if g.Winner != "undercover" || g.Phase != "finished" || len(g.Alive()) != 2 {
		t.Fatal("undercover should win at two remaining")
	}
}
func TestTieRestrictsSpeakersAndTargetsThenAdvancesWithoutRandomElimination(t *testing.T) {
	g := fixture(t)
	describeAll(t, g)
	tie := [][2]string{{"a", "b"}, {"b", "a"}, {"c", "a"}, {"d", "b"}}
	votes(t, g, tie)
	if g.Phase != "tiebreak" || len(g.Candidates) != 2 {
		t.Fatal("missing tiebreak")
	}
	if err := g.Speak("c", "我不在平票名单。", false); err == nil {
		t.Fatal("non-candidate spoke in tiebreak")
	}
	describeAll(t, g)
	if err := g.Vote("a", "c"); err == nil {
		t.Fatal("non-candidate received tiebreak vote")
	}
	votes(t, g, tie)
	if g.Round != 2 || g.Phase != "speaking" || len(g.Alive()) != 4 || len(g.Candidates) != 0 {
		t.Fatal("second tie must advance without elimination")
	}
}
func TestHiddenStateViewsAndRoomPersistence(t *testing.T) {
	store := testsupport.NewRooms(true)
	m, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"u1", "u2"} {
		if _, err = m.Join("room-1", uid, uid, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.Start("room-1", "u2"); err == nil {
		t.Fatal("non-host started game")
	}
	_, _ = m.SetReady("room-1", "u2", true)
	own, err := m.Start("room-1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"u1", "u2"} {
		v, err := m.View("room-1", uid)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		for _, secret := range []string{"undercoverId", "\"words\"", "userId", "\"role\""} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("public response leaks %s: %s", secret, raw)
			}
		}
		if v.MyWord == "" {
			t.Fatal("own card missing")
		}
	}
	if _, err = m.View("room-1", "outsider"); err == nil {
		t.Fatal("outsider read private view")
	}
	m.mu.Lock()
	g := m.rooms["room-1"].Game
	input := g.BotInput(g.Players[2].ID)
	otherWord := ""
	for _, word := range g.Words {
		if word != input.Word {
			otherWord = word
		}
	}
	m.mu.Unlock()
	raw, _ := json.Marshal(input)
	if otherWord != "" && strings.Contains(string(raw), otherWord) {
		t.Fatal("bot was given another faction's word")
	}
	if strings.Contains(string(raw), "undercoverId") {
		t.Fatal("bot was given hidden role")
	}
	restored, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := restored.View("room-1", "u1")
	if err != nil || v.GameID != own.GameID || v.MyWord != own.MyWord {
		t.Fatal("restart changed card assignment")
	}
}
func TestConcurrentSeatsAndLeavingTransfersHost(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	var wg sync.WaitGroup
	for _, uid := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		wg.Add(1)
		go func(uid string) { defer wg.Done(); _, _ = m.Join("room-1", uid, uid, "") }(uid)
	}
	wg.Wait()
	list := m.List()
	if list[0].HumanCount != 4 {
		t.Fatalf("room overflow: %d", list[0].HumanCount)
	}
	m.mu.Lock()
	owner := m.rooms["room-1"].OwnerID
	m.mu.Unlock()
	if err := m.Leave("room-1", owner); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	newOwner := m.rooms["room-1"].OwnerID
	m.mu.Unlock()
	if newOwner == "" || newOwner == owner {
		t.Fatal("host not transferred")
	}
}
func TestTimeoutSkipsOnlyCurrentPlayer(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	_, _ = m.Join("room-1", "u1", "测试", "")
	_, _ = m.Start("room-1", "u1")
	m.mu.Lock()
	g := m.rooms["room-1"].Game
	g.Begin()
	m.mu.Unlock()
	acknowledgePlayback(t, m, "room-1")
	m.mu.Lock()
	g.Deadline = time.Now().Add(-time.Second).UnixMilli()
	m.mu.Unlock()
	m.tick()
	v, _ := m.View("room-1", "u1")
	if v.CurrentPlayerID == v.MyPlayerID || v.Phase != "speaking" {
		t.Fatal("expired human turn was not skipped")
	}
}
func TestLocalBotUsesOnlyItsInput(t *testing.T) {
	g := fixture(t)
	in := g.BotInput("d")
	text, err := (LocalBot{}).Describe(context.Background(), in)
	if err != nil || strings.Contains(text, in.Word) {
		t.Fatal("bot directly spoke its word")
	}
	target, err := (LocalBot{}).Vote(context.Background(), in)
	if err != nil || !contains(in.Targets, target) {
		t.Fatal("bot vote is not legal")
	}
}

func TestCreateRoomAtomicallySeatsOnlyOneOwner(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	var wg sync.WaitGroup
	var lock sync.Mutex
	successes := 0
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := m.Create("same-user", "房主", "我的房间", "jade", 4, "")
			if err == nil {
				if !v.IsHost || v.MyPlayerID == "" || v.HumanCount != 1 {
					t.Error("created room did not seat owner")
				}
				lock.Lock()
				successes++
				lock.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 || len(m.List()) != 4 {
		t.Fatal("concurrent creation left orphan rooms or multiple memberships")
	}
}
func TestSkippedTurnDoesNotFabricatePlayerSpeech(t *testing.T) {
	g := fixture(t)
	if err := g.Skip("a"); err != nil {
		t.Fatal(err)
	}
	last := g.Logs[len(g.Logs)-2]
	if last.Kind != "host" || !strings.Contains(last.Text, "跳过") {
		t.Fatal("skip should be a host event, not an invented player statement")
	}
	if !strings.Contains(g.Logs[len(g.Logs)-1].Text, "2号") {
		t.Fatal("skip must announce the next speaker")
	}
}

func TestPublicSpeakerAnnouncementsAndNarrationPacing(t *testing.T) {
	g := fixture(t)
	if !strings.Contains(g.Logs[len(g.Logs)-1].Text, "1号") {
		t.Fatal("first speaker not announced")
	}
	if err := g.Speak("a", "这是一段比较长的公开描述，其他玩家应该有时间听完后再接话。", false); err != nil {
		t.Fatal(err)
	}
	cue := g.Logs[len(g.Logs)-1]
	if cue.Kind != "host" || !strings.Contains(cue.Text, "2号") {
		t.Fatal("next speaker not announced")
	}
	for _, l := range g.Logs {
		if strings.Contains(l.Text, "太阳") || strings.Contains(l.Text, "月亮") {
			t.Fatal("host announced secret word")
		}
	}
	if narrationReady(g, time.UnixMilli(cue.At).Add(4*time.Second)) {
		t.Fatal("AI advanced before the previous speech and cue could be heard")
	}
	if !narrationReady(g, time.UnixMilli(cue.At).Add(46*time.Second)) {
		t.Fatal("narration estimate blocked game progression")
	}
}

func TestAgentConversationPausesExpiredTurnsAndReleasesControl(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	_, _ = m.Join("room-1", "u", "玩家", "")
	_, _ = m.Start("room-1", "u")
	m.mu.Lock()
	g := m.rooms["room-1"].Game
	g.Begin()
	m.mu.Unlock()
	acknowledgePlayback(t, m, "room-1")
	m.mu.Lock()
	g.Deadline = time.Now().Add(-time.Second).UnixMilli()
	before := g.CurrentID()
	deadline := g.Deadline
	m.mu.Unlock()
	release, err := m.AcquireAgent("room-1", "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AcquireAgent("room-1", "u"); err == nil {
		t.Fatal("two speakers acquired the same host")
	}
	m.tick()
	v, _ := m.View("room-1", "u")
	if v.CurrentPlayerID != before {
		t.Fatal("turn expired while Linx was responding")
	}
	release()
	v, _ = m.View("room-1", "u")
	if v.AgentActive || v.Deadline != deadline {
		t.Fatal("release reset the turn deadline")
	}
}

func TestAISpeechesCommitIndividuallyAndBallotsWaitForHumans(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	_, _ = m.Join("room-1", "u", "玩家", "")
	_, _ = m.Start("room-1", "u")
	m.mu.Lock()
	g := m.rooms["room-1"].Game
	g.Begin()
	g.UndercoverID = g.Players[3].ID
	for _, p := range g.Players {
		g.Words[p.ID] = "太阳"
	}
	g.Words[g.UndercoverID] = "月亮"
	m.mu.Unlock()
	release, _ := m.AcquireAgent("room-1", "u")
	defer release()
	_, _ = m.Speak("room-1", "u", "它在日常生活里非常常见。", nil)
	batch, err := m.NextAIBatch("room-1", "u")
	if err != nil || batch == nil || len(batch.Tasks) != 1 {
		t.Fatal("expected one AI description before narration")
	}
	actions := []AIAction{}
	for _, task := range batch.Tasks {
		actions = append(actions, AIAction{PlayerSeat: task.Seat, Text: "它在生活的许多场景里都有用处。"})
	}
	actions[0].Text = "我的词是太阳"
	before, _ := m.View("room-1", "u")
	if err = m.SubmitAIBatch("room-1", "u", batch.BatchID, actions); err == nil {
		t.Fatal("invalid last description was accepted")
	}
	after, _ := m.View("room-1", "u")
	if after.Version != before.Version || len(after.Logs) != len(before.Logs) {
		t.Fatal("partial batch leaked into authoritative game")
	}
	actions[0].Text = "它在许多人的生活当中都会出现。"
	if err = m.SubmitAIBatch("room-1", "u", batch.BatchID, actions); err != nil {
		t.Fatal(err)
	}
	if err = m.SubmitAIBatch("room-1", "u", batch.BatchID, actions); err == nil {
		t.Fatal("replayed batch executed twice")
	}
	view, _ := m.View("room-1", "u")
	speeches := 0
	for _, log := range view.Logs {
		if log.Kind == "speech" {
			speeches++
		}
	}
	if speeches != 2 || view.CurrentPlayerID != view.Players[2].ID {
		t.Fatal("later AI descriptions were committed before their own narration")
	}
	for seat := 3; seat <= 4; seat++ {
		batch, _ = m.NextAIBatch("room-1", "u")
		if batch == nil || len(batch.Tasks) != 1 || batch.Tasks[0].Seat != seat {
			t.Fatal("AI description order changed")
		}
		if err = m.SubmitAIBatch("room-1", "u", batch.BatchID, []AIAction{{PlayerSeat: seat, Text: "它在许多人的生活当中都会出现。"}}); err != nil {
			t.Fatal(err)
		}
	}
	if batch, _ = m.NextAIBatch("room-1", "u"); batch != nil {
		t.Fatal("AI ballots must wait until humans have voted")
	}
	release()
	acknowledgePlayback(t, m, "room-1")
	v, _ := m.View("room-1", "u")
	_, err = m.Vote("room-1", "u", v.Players[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, _ = m.NextAIBatch("room-1", "u")
	if batch == nil || len(batch.Tasks) != 3 || batch.Tasks[0].Phase != "voting" {
		t.Fatal("AI ballot group missing after human vote")
	}
}
