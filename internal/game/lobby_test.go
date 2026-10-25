package game

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func newLobby(t *testing.T, options CreateOptions) (*Manager, string, *testsupport.Rooms) {
	t.Helper()
	store := testsupport.NewRooms(false)
	m, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.CreateWithOptions("host", "房主", "准备测试", "jade", 4, "", options)
	if err != nil {
		t.Fatal(err)
	}
	return m, v.ID, store
}

func ready(t *testing.T, m *Manager, id, uid string) View {
	t.Helper()
	v, err := m.SetReady(id, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLobbyReadinessAndRosterChanges(t *testing.T) {
	m, id, _ := newLobby(t, CreateOptions{})
	friend, err := m.Join(id, "friend", "新一", "")
	if err != nil {
		t.Fatal(err)
	}
	friend.ServerNow = 0
	before, _ := json.Marshal(friend)
	if _, err = m.Start(id, "host"); err == nil || !strings.Contains(err.Error(), "2号新一") {
		t.Fatal("start must name the unready player")
	}
	after, _ := m.View(id, "friend")
	after.ServerNow = 0
	raw, _ := json.Marshal(after)
	if string(before) != string(raw) {
		t.Fatal("rejected start changed the room")
	}
	updates, cancel := m.Subscribe(id)
	defer cancel()
	if !ready(t, m, id, "friend").CanStart {
		t.Fatal("ready guests did not enable host start")
	}
	select {
	case <-updates:
	default:
		t.Fatal("readiness was not published")
	}
	v, err := m.SetReady(id, "friend", false)
	if err != nil || v.CanStart {
		t.Fatal("cancel readiness did not block start")
	}
	ready(t, m, id, "friend")
	if _, err = m.Join(id, "third", "小明", ""); err != nil {
		t.Fatal(err)
	}
	v, _ = m.View(id, "friend")
	if v.Players[1].Ready || v.CanStart {
		t.Fatal("new member reused old readiness")
	}
	ready(t, m, id, "friend")
	ready(t, m, id, "third")
	if err = m.Leave(id, "third"); err != nil {
		t.Fatal(err)
	}
	v, _ = m.View(id, "friend")
	if v.Players[1].Ready {
		t.Fatal("member departure retained readiness")
	}
	ready(t, m, id, "friend")
	if _, err = m.SetAutoFillAI(id, "host", false); err != nil {
		t.Fatal(err)
	}
	v, _ = m.View(id, "friend")
	if v.Players[1].Ready || v.AutoFillAI {
		t.Fatal("fill setting did not reset readiness")
	}
	ready(t, m, id, "friend")
	if _, err = m.Start(id, "host"); err == nil {
		t.Fatal("started with empty seats and fill disabled")
	}
	for _, seat := range []int{3, 4} {
		if _, err = m.SetAISeat(id, "host", seat, true); err != nil {
			t.Fatal(err)
		}
	}
	v = ready(t, m, id, "friend")
	if !v.CanStart || v.AvailableSeats != 0 {
		t.Fatal("explicit AI seats were not ready")
	}
	started, err := m.Start(id, "host")
	if err != nil || started.Phase != "dealing" || len(started.Players) != 4 {
		t.Fatal("valid lobby did not deal", err)
	}
	if started.Players[1].Ready || !started.Players[2].Bot || started.MyWord == "" {
		t.Fatal("bad dealt roster")
	}
}

func TestPasswordProtectionSurvivesRestartWithoutLeakingCredentials(t *testing.T) {
	m, id, store := newLobby(t, CreateOptions{Password: "secret-桌口令"})
	stored := string(store.Records[id])
	if strings.Contains(stored, "secret-桌口令") || !strings.Contains(stored, "pbkdf2-sha256") {
		t.Fatal("stored plaintext or missing salted hash")
	}
	for _, input := range []string{"", "incorrect"} {
		if _, err := m.JoinWithPassword(id, "friend", "朋友", "", input); err == nil {
			t.Fatal("joined without correct password")
		}
	}
	if _, err := m.Join(id, "friend", "朋友", ""); !errors.Is(err, ErrPasswordRequired) {
		t.Fatal("legacy join bypassed protection")
	}
	restarted, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := restarted.JoinWithPassword(id, "friend", "朋友", "", "secret-桌口令")
	if err != nil || !v.PasswordRequired {
		t.Fatal("correct password failed after restart", err)
	}
	if _, err = restarted.Join(id, "friend", "朋友", ""); err != nil {
		t.Fatal("existing member could not reconnect", err)
	}
	ready(t, restarted, id, "friend")
	restarted, err = NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ = restarted.View(id, "friend")
	if !v.CanStart || !v.Players[1].Ready {
		t.Fatal("readiness did not survive restart")
	}
	for _, data := range []any{v, restarted.List()} {
		raw, _ := json.Marshal(data)
		for _, secret := range []string{"secret-桌口令", "passwordHash", "pbkdf2", "userId"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("response leaked %s", secret)
			}
		}
	}
	for i := 0; i < 5; i++ {
		_, _ = restarted.JoinWithPassword(id, "outsider", "路人", "", "wrong")
	}
	if _, err = restarted.JoinWithPassword(id, "outsider", "路人", "", "secret-桌口令"); !errors.Is(err, ErrJoinRateLimit) {
		t.Fatal("unbounded password attempts")
	}
}

func TestLobbyPermissionsAndActiveGameProtection(t *testing.T) {
	m, id, _ := newLobby(t, CreateOptions{})
	friend, _ := m.Join(id, "friend", "朋友", "")
	for _, uid := range []string{"friend", "outsider"} {
		for _, err := range []error{
			func() error { _, err := m.SetAutoFillAI(id, uid, false); return err }(),
			func() error { _, err := m.SetAISeat(id, uid, 4, true); return err }(),
			func() error { _, err := m.RemoveMember(id, uid, friend.MyPlayerID); return err }(),
		} {
			if !errors.Is(err, ErrHostOnly) {
				t.Fatal("non-host managed lobby", uid, err)
			}
		}
	}
	if _, err := m.SetReady(id, "outsider", true); !errors.Is(err, ErrNotMember) {
		t.Fatal("outsider readied a seat")
	}
	host, _ := m.View(id, "host")
	if _, err := m.RemoveMember(id, "host", host.MyPlayerID); err == nil {
		t.Fatal("host removed self")
	}
	if _, err := m.SetAISeat(id, "host", 2, true); err == nil {
		t.Fatal("AI replaced human")
	}
	ready(t, m, id, "friend")
	if _, err := m.Start(id, "host"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetReady(id, "friend", false); err == nil {
		t.Fatal("readiness changed active game")
	}
	if _, err := m.SetAISeat(id, "host", 4, false); err == nil {
		t.Fatal("removed active AI")
	}
	if _, err := m.SetAutoFillAI(id, "host", false); err == nil {
		t.Fatal("changed fill during game")
	}
	if _, err := m.RemoveMember(id, "host", friend.MyPlayerID); err == nil {
		t.Fatal("kicked active human")
	}
}

func TestRemoveMemberAndHostTransferResetReadiness(t *testing.T) {
	m, id, _ := newLobby(t, CreateOptions{})
	friend, _ := m.Join(id, "friend", "朋友", "")
	_, _ = m.Join(id, "third", "第三位", "")
	ready(t, m, id, "friend")
	ready(t, m, id, "third")
	if _, err := m.RemoveMember(id, "host", friend.MyPlayerID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.View(id, "friend"); !errors.Is(err, ErrNotMember) {
		t.Fatal("removed member can read room")
	}
	v, _ := m.View(id, "third")
	if v.Players[1].Seat != 3 || v.Players[1].Ready {
		t.Fatal("removal changed seat numbers or kept readiness")
	}
	_, _ = m.Join(id, "friend", "朋友", "")
	ready(t, m, id, "friend")
	ready(t, m, id, "third")
	if err := m.Leave(id, "host"); err != nil {
		t.Fatal(err)
	}
	v, _ = m.View(id, "friend")
	if !v.IsHost || v.Players[1].Ready || v.CanStart {
		t.Fatal("host transfer did not require new confirmation")
	}
	if _, err := m.SetAISeat(id, "host", 1, true); !errors.Is(err, ErrHostOnly) {
		t.Fatal("former owner retained controls")
	}
	if _, err := m.SetAISeat(id, "friend", 1, true); err != nil {
		t.Fatal("new owner cannot arrange AI")
	}
	ready(t, m, id, "third")
	v, err := m.Start(id, "friend")
	if err != nil || !v.Players[0].Bot || v.Players[1].ID != v.MyPlayerID {
		t.Fatal("start failed with host at seat 2", err)
	}
}

func TestLobbyWritesRollBackWithoutNotification(t *testing.T) {
	m, id, store := newLobby(t, CreateOptions{})
	friend, _ := m.Join(id, "friend", "朋友", "")
	ready(t, m, id, "friend")
	before := string(store.Records[id])
	m.audio[id+"/previous"] = []byte("previous recording")
	updates, cancel := m.Subscribe(id)
	defer cancel()
	store.Fail = errors.New("database unavailable")
	for _, action := range []func() error{
		func() error { _, err := m.SetReady(id, "friend", false); return err },
		func() error { _, err := m.SetAISeat(id, "host", 4, true); return err },
		func() error { _, err := m.SetAutoFillAI(id, "host", false); return err },
		func() error { _, err := m.RemoveMember(id, "host", friend.MyPlayerID); return err },
		func() error { _, err := m.Start(id, "host"); return err },
	} {
		if err := action(); err == nil {
			t.Fatal("mutation succeeded without persistence")
		}
		if string(backup(m.rooms[id])) != before {
			t.Fatal("failed mutation changed room")
		}
		if string(m.audio[id+"/previous"]) != "previous recording" {
			t.Fatal("failed start discarded existing audio")
		}
		select {
		case <-updates:
			t.Fatal("failed mutation notified clients")
		default:
		}
	}
}

func TestNextGameKeepsReservedAIAndRequiresFreshReadiness(t *testing.T) {
	m, id, _ := newLobby(t, CreateOptions{})
	_, _ = m.Join(id, "friend", "朋友", "")
	_, _ = m.SetAISeat(id, "host", 4, true)
	ready(t, m, id, "friend")
	_, _ = m.Start(id, "host")
	m.rooms[id].Game.Phase = "finished"
	m.rooms[id].Game.Winner = "civilian"
	before, _ := m.View(id, "host")
	if len(before.Reveals) != 4 || len(before.LobbyPlayers) != 3 || before.AvailableSeats != 1 || before.CanStart {
		t.Fatal("invalid next-game lobby snapshot")
	}
	if _, err := m.Start(id, "host"); err == nil {
		t.Fatal("restarted without new readiness")
	}
	if m.rooms[id].Game == nil {
		t.Fatal("rejected restart hid previous result")
	}
	ready(t, m, id, "friend")
	v, err := m.Start(id, "host")
	if err != nil || v.GameID == before.GameID || v.Players[3].ID != before.Players[3].ID || v.Players[2].ID == before.Players[2].ID {
		t.Fatal("reserved and automatic AI seats were not distinguished", err)
	}
}

func TestConcurrentStartAndJoinNeverDealAnUnreadyNewcomer(t *testing.T) {
	for i := 0; i < 20; i++ {
		m, id, _ := newLobby(t, CreateOptions{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = m.Start(id, "host") }()
		go func() { defer wg.Done(); _, _ = m.Join(id, "friend", "朋友", "") }()
		wg.Wait()
		r := m.rooms[id]
		if r.Game != nil && humanCount(r) != 1 {
			t.Fatal("unready concurrent newcomer was dealt in")
		}
	}
}

func TestSixPlayerLobbyReservedSeatsAndIdempotentSettings(t *testing.T) {
	store := testsupport.NewRooms(false)
	m, _ := NewManager(store, nil)
	v, err := m.Create("host", "房主", "六人准备", "night", 6, "")
	if err != nil {
		t.Fatal(err)
	}
	id := v.ID
	_, _ = m.SetAISeat(id, "host", 3, true)
	_, _ = m.SetAISeat(id, "host", 6, true)
	friend, _ := m.Join(id, "friend", "朋友", "")
	other, _ := m.Join(id, "other", "另一位", "")
	if friend.Players[1].Seat != 2 || other.Players[3].Seat != 4 {
		t.Fatal("human displaced reserved AI")
	}
	ready(t, m, id, "friend")
	before := ready(t, m, id, "other")
	for _, mutate := range []func() (View, error){
		func() (View, error) { return m.SetReady(id, "friend", true) },
		func() (View, error) { return m.SetAISeat(id, "host", 3, true) },
		func() (View, error) { return m.SetAISeat(id, "host", 5, false) },
		func() (View, error) { return m.SetAutoFillAI(id, "host", true) },
	} {
		v, err := mutate()
		if err != nil || v.Version != before.Version || !v.CanStart {
			t.Fatal("no-op invalidated readiness", err)
		}
	}
	v, err = m.Start(id, "host")
	if err != nil || len(v.Players) != 6 {
		t.Fatal("six-player lobby failed to deal", err)
	}
	for i, p := range v.Players {
		if p.Seat != i+1 {
			t.Fatal("dealt players are out of seat order")
		}
	}
	if !v.Players[2].Bot || !v.Players[4].Bot || !v.Players[5].Bot {
		t.Fatal("incorrect AI arrangement")
	}
}

func TestPasswordValidationSaltAndRateLimitExpiry(t *testing.T) {
	for _, password := range []string{"abc", " abcdef", "abcdef ", "abc\nxyz", strings.Repeat("密", 33)} {
		if _, err := hashPassword(password); err == nil {
			t.Fatal("accepted invalid room password")
		}
	}
	m, id, _ := newLobby(t, CreateOptions{Password: "口令四字"})
	hash, err := hashPassword("口令四字")
	if err != nil || hash == m.rooms[id].PasswordHash || !checkPassword(hash, "口令四字") {
		t.Fatal("password hashes were not randomly salted")
	}
	for i := 0; i < 5; i++ {
		_, _ = m.JoinWithPassword(id, "friend", "朋友", "", "wrong")
	}
	m.joinAttempts[id+"/friend"] = joinAttempt{Count: 5, Expires: time.Now().Add(-time.Second)}
	if _, err = m.JoinWithPassword(id, "friend", "朋友", "", "口令四字"); err != nil {
		t.Fatal("expired limiter blocked valid join", err)
	}
}
