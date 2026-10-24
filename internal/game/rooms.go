package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-wyvern/undercover/internal/storage"
)

type Room struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Theme       string   `json:"theme"`
	Capacity    int      `json:"capacity"`
	OwnerID     string   `json:"ownerId"`
	Players     []Player `json:"players"`
	Game        *Game    `json:"game,omitempty"`
	Version     int      `json:"version"`
	CreatedAt   int64    `json:"createdAt"`
	Logs        []Log    `json:"logs,omitempty"`
	AgentActive bool     `json:"-"`
	Seeded      bool     `json:"seeded,omitempty"`
}
type PublicPlayer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar,omitempty"`
	Seat     int    `json:"seat"`
	Bot      bool   `json:"bot"`
	Alive    bool   `json:"alive"`
	HasVoted bool   `json:"hasVoted"`
}
type Summary struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Theme      string         `json:"theme"`
	Capacity   int            `json:"capacity"`
	HumanCount int            `json:"humanCount"`
	Phase      string         `json:"phase"`
	Players    []PublicPlayer `json:"players"`
}
type View struct {
	Summary
	AgentActive     bool     `json:"agentActive"`
	GameID          string   `json:"gameId"`
	Version         int      `json:"version"`
	IsHost          bool     `json:"isHost"`
	MyPlayerID      string   `json:"myPlayerId"`
	MyWord          string   `json:"myWord,omitempty"`
	Round           int      `json:"round"`
	CurrentPlayerID string   `json:"currentPlayerId"`
	CandidateIDs    []string `json:"candidateIds"`
	LegalTargets    []string `json:"legalTargets"`
	Logs            []Log    `json:"logs"`
	Winner          string   `json:"winner,omitempty"`
	Reveals         []Reveal `json:"reveals"`
	LastVotes       []Ballot `json:"lastVotes"`
	Deadline        int64    `json:"deadline"`
}
type Manager struct {
	mu            sync.Mutex
	rooms         map[string]*Room
	subs          map[string]map[chan struct{}]struct{}
	audio         map[string][]byte
	store         storage.RoomStore
	done          map[string]chan struct{}
	bot           Bot
	busy          map[string]bool
	jobs          chan botJob
	externalAgent bool
	agentRooms    map[string]bool
}
type botJob struct {
	RoomID, GameID string
	Input          BotInput
	key            string
}

var ErrRoomNotFound = errors.New("房间已关闭或不存在")
var ErrNotOwner = errors.New("只有房主可以关闭房间")

func NewManager(store storage.RoomStore, bot Bot) (*Manager, error) {
	if store == nil {
		return nil, errors.New("room store is required")
	}
	if bot == nil {
		bot = LocalBot{}
	}
	m := &Manager{rooms: map[string]*Room{}, subs: map[string]map[chan struct{}]struct{}{}, audio: map[string][]byte{}, store: store, done: map[string]chan struct{}{}, bot: bot, busy: map[string]bool{}, jobs: make(chan botJob, 128), agentRooms: map[string]bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	records, err := store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load rooms: %w", err)
	}
	for id, raw := range records {
		var room Room
		if err = json.Unmarshal(raw, &room); err != nil {
			return nil, fmt.Errorf("decode room %s: %w", id, err)
		}
		if room.ID != id || id == "" || (room.Capacity != 4 && room.Capacity != 6) {
			return nil, fmt.Errorf("invalid stored room %s", id)
		}
		m.rooms[id] = &room
		m.done[id] = make(chan struct{})
	}
	return m, nil
}
func (m *Manager) persistLocked(r *Room) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return m.store.Save(ctx, r.ID, b)
}

// Done cancels voice and automatic host work when a room is permanently closed.
func (m *Manager) Done(id string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if done := m.done[id]; done != nil {
		return done
	}
	done := make(chan struct{})
	close(done)
	return done
}
func (m *Manager) CloseRoom(id, uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil {
		return ErrRoomNotFound
	}
	if r.OwnerID != uid {
		return ErrNotOwner
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.store.Delete(ctx, id); err != nil && !errors.Is(err, storage.ErrNotFound) {
		slog.Error("delete room", "room", id, "error", err)
		return errors.New("关闭房间失败，请重试")
	}
	delete(m.rooms, id)
	if done := m.done[id]; done != nil {
		close(done)
		delete(m.done, id)
	}
	delete(m.agentRooms, id)
	for key := range m.audio {
		if strings.HasPrefix(key, id+"/") {
			delete(m.audio, key)
		}
	}
	// Existing subscribers receive one last notification and emit a closed event.
	m.notifyLocked(id)
	return nil
}
func (m *Manager) notifyLocked(id string) {
	for c := range m.subs[id] {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}
func (m *Manager) commitLocked(r *Room, backup []byte) error {
	r.Version++
	if err := m.persistLocked(r); err != nil {
		var restored Room
		_ = json.Unmarshal(backup, &restored)
		restored.AgentActive = m.agentRooms[r.ID]
		m.rooms[r.ID] = &restored
		slog.Error("save room", "room", r.ID, "error", err)
		return errors.New("房间状态保存失败，请重试")
	}
	m.notifyLocked(r.ID)
	return nil
}
func backup(r *Room) []byte { b, _ := json.Marshal(r); return b }
func (m *Manager) Subscribe(id string) (<-chan struct{}, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := make(chan struct{}, 1)
	if m.subs[id] == nil {
		m.subs[id] = map[chan struct{}]struct{}{}
	}
	m.subs[id][c] = struct{}{}
	return c, func() {
		m.mu.Lock()
		delete(m.subs[id], c)
		if len(m.subs[id]) == 0 {
			delete(m.subs, id)
		}
		m.mu.Unlock()
	}
}
func roomPlayers(r *Room) []Player {
	if r.Game != nil {
		return r.Game.Players
	}
	return r.Players
}
func member(r *Room, uid string) *Player {
	ps := roomPlayers(r)
	for i := range ps {
		if ps[i].UserID == uid && !ps[i].Bot {
			return &ps[i]
		}
	}
	return nil
}
func humanCount(r *Room) int {
	n := 0
	for _, p := range roomPlayers(r) {
		if !p.Bot {
			n++
		}
	}
	return n
}
func phase(r *Room) string {
	if r.Game != nil {
		return r.Game.Phase
	}
	return "waiting"
}
func summary(r *Room) Summary {
	s := Summary{ID: r.ID, Name: r.Name, Theme: r.Theme, Capacity: r.Capacity, HumanCount: humanCount(r), Phase: phase(r), Players: []PublicPlayer{}}
	for _, p := range roomPlayers(r) {
		s.Players = append(s.Players, PublicPlayer{ID: p.ID, Name: p.Name, Avatar: p.Avatar, Seat: p.Seat, Bot: p.Bot, Alive: p.Alive})
	}
	return s
}
func (m *Manager) List() []Summary {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs := []*Room{}
	for _, r := range m.rooms {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].CreatedAt < rs[j].CreatedAt })
	out := []Summary{}
	for _, r := range rs {
		out = append(out, summary(r))
	}
	return out
}
func (m *Manager) CurrentRoom(uid string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rooms {
		if member(r, uid) != nil {
			return r.ID
		}
	}
	return ""
}
func (m *Manager) View(id, uid string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok {
		return View{}, ErrRoomNotFound
	}
	if member(r, uid) == nil {
		return View{}, errors.New("请先加入房间")
	}
	return view(r, uid), nil
}
func view(r *Room, uid string) View {
	v := View{Summary: summary(r), Version: r.Version, IsHost: r.OwnerID == uid, Logs: []Log{}, CandidateIDs: []string{}, LegalTargets: []string{}, Reveals: []Reveal{}, LastVotes: []Ballot{}}
	p := member(r, uid)
	if p == nil {
		return v
	}
	v.AgentActive = r.AgentActive
	v.MyPlayerID = p.ID
	v.Logs = append(v.Logs, r.Logs...)
	if g := r.Game; g != nil {
		v.GameID = g.ID
		v.MyWord = g.Words[p.ID]
		v.Round = g.Round
		v.CurrentPlayerID = g.CurrentID()
		v.CandidateIDs = append(v.CandidateIDs, g.Candidates...)
		v.LegalTargets = append(v.LegalTargets, g.LegalTargets(p.ID)...)
		v.Logs = append(v.Logs, g.Logs...)
		v.Winner = g.Winner
		v.Reveals = append(v.Reveals, g.Reveals()...)
		v.LastVotes = append(v.LastVotes, g.LastVotes...)
		v.Deadline = g.Deadline
		for i := range v.Players {
			_, v.Players[i].HasVoted = g.Votes[v.Players[i].ID]
		}
	}
	return v
}
func (m *Manager) Create(uid, displayName, name, theme string, capacity int, avatar string) (View, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 24 || strings.ContainsFunc(name, unicode.IsControl) {
		return View{}, errors.New("房间名需要 1 至 24 字")
	}
	if capacity != 4 && capacity != 6 {
		return View{}, errors.New("请选择四人或六人局")
	}
	if theme != "jade" && theme != "peach" && theme != "night" {
		theme = "jade"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.rooms) >= 128 {
		return View{}, errors.New("房间已满，请加入已有房间")
	}
	owned := 0
	for _, r := range m.rooms {
		if member(r, uid) != nil {
			return View{}, errors.New("请先离开当前房间")
		}
		if r.OwnerID == uid {
			owned++
		}
	}
	if owned >= 5 {
		return View{}, errors.New("最多创建五个房间")
	}
	r := &Room{ID: ID(), Name: name, Theme: theme, Capacity: capacity, OwnerID: uid, Players: []Player{{ID: ID(), UserID: uid, Name: displayName, Avatar: avatar, Seat: 1, Alive: true}}, Version: 1, CreatedAt: time.Now().UnixMilli()}
	m.rooms[r.ID] = r
	if err := m.persistLocked(r); err != nil {
		delete(m.rooms, r.ID)
		return View{}, err
	}
	m.done[r.ID] = make(chan struct{})
	return view(r, uid), nil
}
func resetFinished(r *Room) {
	if r.Game == nil {
		return
	}
	r.Players = nil
	for _, p := range r.Game.Players {
		if !p.Bot {
			p.Alive = true
			p.Seat = len(r.Players) + 1
			r.Players = append(r.Players, p)
		}
	}
	r.Game = nil
}
func (m *Manager) Join(id, uid, name, avatar string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok {
		return View{}, ErrRoomNotFound
	}
	if member(r, uid) != nil {
		if err := m.syncAvatarLocked(r, uid, avatar); err != nil {
			return View{}, err
		}
		return view(r, uid), nil
	}
	for _, other := range m.rooms {
		if member(other, uid) != nil {
			return View{}, errors.New("请先离开当前房间")
		}
	}
	if phase(r) != "waiting" && phase(r) != "finished" {
		return View{}, errors.New("这桌正在游戏，请稍后再来")
	}
	if humanCount(r) >= r.Capacity {
		return View{}, errors.New("这桌已经坐满了")
	}
	b := backup(r)
	resetFinished(r)
	r.Players = append(r.Players, Player{ID: ID(), UserID: uid, Name: name, Avatar: avatar, Seat: len(r.Players) + 1, Alive: true})
	if r.OwnerID == "" {
		r.OwnerID = uid
	}
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}
func (m *Manager) Leave(id, uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok {
		return ErrRoomNotFound
	}
	p := member(r, uid)
	if p == nil {
		return nil
	}
	b := backup(r)
	if r.Game != nil && r.Game.Phase != "finished" {
		p.Bot = true
		p.UserID = ""
		p.Avatar = ""
		p.Name = "AI 代玩"
		r.Game.addLog("host", nil, fmt.Sprintf("%d号暂时离席，由AI接续这一局。", p.Seat))
	} else {
		resetFinished(r)
		ps := r.Players[:0]
		for _, p := range r.Players {
			if p.UserID != uid {
				p.Seat = len(ps) + 1
				ps = append(ps, p)
			}
		}
		r.Players = ps
	}
	if r.OwnerID == uid {
		r.OwnerID = ""
		for _, p := range roomPlayers(r) {
			if !p.Bot {
				r.OwnerID = p.UserID
				break
			}
		}
	}
	if humanCount(r) == 0 {
		r.Game = nil
		r.Players = []Player{}
		r.OwnerID = ""
		for key := range m.audio {
			if strings.HasPrefix(key, id+"/") {
				delete(m.audio, key)
			}
		}
	}
	return m.commitLocked(r, b)
}
func (m *Manager) Start(id, uid string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok || member(r, uid) == nil {
		return View{}, errors.New("请先加入房间")
	}
	if r.OwnerID != uid {
		return View{}, errors.New("请等待房主开始发牌")
	}
	if phase(r) != "waiting" && phase(r) != "finished" {
		return View{}, errors.New("游戏已经开始")
	}
	b := backup(r)
	resetFinished(r)
	names := []string{"小北", "阿岚", "大乔", "阿泽", "小满", "一禾"}
	ps := append([]Player(nil), r.Players...)
	for len(ps) < r.Capacity {
		i := len(ps)
		ps = append(ps, Player{ID: ID(), Name: names[i-1], Seat: i + 1, Bot: true, Alive: true})
	}
	g, err := New(ps)
	if err != nil {
		return View{}, err
	}
	r.Game = g
	r.Logs = nil
	for key := range m.audio {
		if strings.HasPrefix(key, id+"/") {
			delete(m.audio, key)
		}
	}
	if err = m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}
func (m *Manager) Speak(id, uid, text string, pcm []byte) (View, error) {
	if len(pcm) > 960000 || len(pcm)%2 != 0 {
		return View{}, errors.New("语音最长为30秒，请重新录制")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok || member(r, uid) == nil || r.Game == nil {
		return View{}, errors.New("请先开始游戏")
	}
	p := member(r, uid)
	b := backup(r)
	if err := r.Game.Speak(p.ID, text, len(pcm) > 0); err != nil {
		return View{}, err
	}
	logID := ""
	for i := len(r.Game.Logs) - 1; i >= 0; i-- {
		if r.Game.Logs[i].Kind == "speech" {
			logID = r.Game.Logs[i].ID
			break
		}
	}
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	if len(pcm) > 0 {
		m.audio[id+"/"+logID] = append([]byte(nil), pcm...)
	}
	return view(r, uid), nil
}
func (m *Manager) Vote(id, uid, target string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok || member(r, uid) == nil || r.Game == nil {
		return View{}, errors.New("请先开始游戏")
	}
	b := backup(r)
	if err := r.Game.Vote(member(r, uid).ID, target); err != nil {
		return View{}, err
	}
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}
func (m *Manager) Skip(id, uid string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok || member(r, uid) == nil || r.Game == nil {
		return View{}, errors.New("请先开始游戏")
	}
	b := backup(r)
	if err := r.Game.Skip(member(r, uid).ID); err != nil {
		return View{}, err
	}
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}
func (m *Manager) Event(id, uid, logID string) (Log, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok || member(r, uid) == nil {
		return Log{}, nil, errors.New("无法访问这条发言")
	}
	logs := r.Logs
	if r.Game != nil {
		logs = append(append([]Log(nil), logs...), r.Game.Logs...)
	}
	for _, l := range logs {
		if l.ID == logID {
			return l, append([]byte(nil), m.audio[id+"/"+logID]...), nil
		}
	}
	return Log{}, nil, errors.New("这条发言已过期")
}

func (m *Manager) Run(ctx context.Context) {
	for i := 0; i < 4; i++ {
		go m.worker(ctx)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick()
		}
	}
}
func (m *Manager) tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rooms {
		g := r.Game
		if g == nil || g.Phase == "finished" {
			continue
		}
		if m.agentRooms[r.ID] && g.Phase != "dealing" {
			continue
		}
		b := backup(r)
		changed := false
		if g.Phase == "dealing" && time.Now().UnixMilli() >= g.Deadline {
			g.Begin()
			changed = true
		}
		if (g.Phase == "speaking" || g.Phase == "tiebreak") && time.Now().UnixMilli() >= g.Deadline {
			_ = g.Skip(g.CurrentID())
			changed = true
		}
		if g.Phase == "voting" && time.Now().UnixMilli() >= g.Deadline {
			ids := []string{}
			for _, p := range g.Alive() {
				if _, ok := g.Votes[p.ID]; !ok {
					ids = append(ids, p.ID)
				}
			}
			round := g.Round
			for _, id := range ids {
				if g.Phase == "voting" && g.Round == round {
					_ = g.Vote(id, "")
					changed = true
				}
			}
		}
		if changed {
			if err := m.commitLocked(r, b); err != nil {
				slog.Error("save room tick", "error", err)
				continue
			}
		}
		g = r.Game
		if m.agentRooms[r.ID] || m.externalAgent {
			continue
		}
		ids := []string{}
		if id := g.CurrentID(); id != "" && g.Player(id).Bot {
			if narrationReady(g, time.Now()) {
				ids = append(ids, id)
			}
		}
		if g.Phase == "voting" && narrationReady(g, time.Now()) {
			for _, p := range g.Alive() {
				if _, ok := g.Votes[p.ID]; p.Bot && !ok {
					ids = append(ids, p.ID)
				}
			}
		}
		for _, id := range ids {
			key := fmt.Sprintf("%s/%s/%d/%s/%s/%d", r.ID, g.ID, g.Round, g.Phase, id, len(g.Candidates))
			if m.busy[key] {
				continue
			}
			j := botJob{RoomID: r.ID, GameID: g.ID, Input: g.BotInput(id), key: key}
			select {
			case m.jobs <- j:
				m.busy[key] = true
			default:
			}
		}
	}
}

// Estimate the current batch (a description/tally plus its host announcements),
// so AI actions leave enough time to hear it. Human actions remain immediate.
func narrationReady(g *Game, now time.Time) bool {
	if len(g.Logs) == 0 {
		return true
	}
	characters := 0
	for i := len(g.Logs) - 1; i >= 0; i-- {
		l := g.Logs[i]
		characters += utf8.RuneCountInString(l.Text)
		if l.Kind == "speech" || l.Kind == "votes" {
			break
		}
	}
	delay := 1800*time.Millisecond + time.Duration(characters)*260*time.Millisecond
	if delay > 45*time.Second {
		delay = 45 * time.Second
	}
	return now.UnixMilli()-g.Logs[len(g.Logs)-1].At >= delay.Milliseconds()
}
func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-m.jobs:
			// A short pause keeps the round table readable and makes bot turns visible.
			timer := time.NewTimer(1100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			jobCtx, cancel := context.WithTimeout(ctx, 14*time.Second)
			var text string
			var err error
			if j.Input.Phase == "voting" {
				text, err = m.bot.Vote(jobCtx, j.Input)
			} else {
				text, err = m.bot.Describe(jobCtx, j.Input)
			}
			cancel()
			if err != nil {
				slog.Warn("AI player using local fallback", "error", err)
				if j.Input.Phase == "voting" {
					text, _ = (LocalBot{}).Vote(ctx, j.Input)
				} else {
					text, _ = (LocalBot{}).Describe(ctx, j.Input)
				}
			}
			m.mu.Lock()
			delete(m.busy, j.key)
			r := m.rooms[j.RoomID]
			if r == nil || r.Game == nil || r.Game.ID != j.GameID || r.Game.Round != j.Input.Round || r.Game.Phase != j.Input.Phase {
				m.mu.Unlock()
				continue
			}
			g := r.Game
			if m.agentRooms[r.ID] {
				m.mu.Unlock()
				continue
			}
			p := g.Player(j.Input.PlayerID)
			if p == nil || !p.Bot || !p.Alive {
				m.mu.Unlock()
				continue
			}
			b := backup(r)
			if j.Input.Phase == "voting" {
				err = g.Vote(p.ID, text)
			} else {
				err = g.Speak(p.ID, text, false)
				if err != nil && g.CurrentID() == p.ID {
					fallback, _ := (LocalBot{}).Describe(ctx, j.Input)
					err = g.Speak(p.ID, fallback, false)
				}
			}
			if err == nil {
				if err = m.commitLocked(r, b); err != nil {
					slog.Error("save bot action", "error", err)
				}
			}
			m.mu.Unlock()
		}
	}
}

// SyncAvatar upgrades an existing seat from the authenticated account profile.
// Update both the lobby roster and the active game so a new round keeps it.
func (m *Manager) SyncAvatar(uid, avatar string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rooms {
		if member(r, uid) != nil {
			return m.syncAvatarLocked(r, uid, avatar)
		}
	}
	return nil
}
func (m *Manager) syncAvatarLocked(r *Room, uid, avatar string) error {
	targets := []*Player{}
	for i := range r.Players {
		if !r.Players[i].Bot && r.Players[i].UserID == uid && r.Players[i].Avatar != avatar {
			targets = append(targets, &r.Players[i])
		}
	}
	if r.Game != nil {
		for i := range r.Game.Players {
			if !r.Game.Players[i].Bot && r.Game.Players[i].UserID == uid && r.Game.Players[i].Avatar != avatar {
				targets = append(targets, &r.Game.Players[i])
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	before := backup(r)
	for _, p := range targets {
		p.Avatar = avatar
	}
	return m.commitLocked(r, before)
}
