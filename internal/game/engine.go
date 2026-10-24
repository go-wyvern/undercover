// Package game owns all hidden information and authoritative game transitions.
package game

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

type Player struct {
	ID     string `json:"id"`
	UserID string `json:"userId,omitempty"`
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"`
	Seat   int    `json:"seat"`
	Bot    bool   `json:"bot"`
	Alive  bool   `json:"alive"`
}
type AudioCue struct {
	OffsetMS int64  `json:"offsetMs"`
	Text     string `json:"text"`
	LogID    string `json:"logId,omitempty"`
	PlayerID string `json:"playerId,omitempty"`
	Seat     int    `json:"seat,omitempty"`
}
type Log struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Source    string     `json:"source,omitempty"`
	PlayerID  string     `json:"playerId,omitempty"`
	Seat      int        `json:"seat,omitempty"`
	Name      string     `json:"name,omitempty"`
	Text      string     `json:"text"`
	Round     int        `json:"round"`
	At        int64      `json:"at"`
	Audio     bool       `json:"audio,omitempty"`
	AudioCues []AudioCue `json:"audioCues,omitempty"`
}
type Reveal struct {
	PlayerID string `json:"playerId"`
	Seat     int    `json:"seat"`
	Name     string `json:"name"`
	Word     string `json:"word"`
	Role     string `json:"role"`
}
type Ballot struct {
	PlayerID string `json:"playerId"`
	TargetID string `json:"targetId"`
}
type Game struct {
	ID           string            `json:"id"`
	Players      []Player          `json:"players"`
	Words        map[string]string `json:"words"`
	UndercoverID string            `json:"undercoverId"`
	Phase        string            `json:"phase"`
	Round        int               `json:"round"`
	Turn         int               `json:"turn"`
	Candidates   []string          `json:"candidates,omitempty"`
	Votes        map[string]string `json:"votes"`
	LastVotes    []Ballot          `json:"lastVotes,omitempty"`
	Logs         []Log             `json:"logs"`
	Winner       string            `json:"winner,omitempty"`
	Deadline     int64             `json:"deadline"`
}

var Pairs = [][2]string{{"豆浆", "牛奶"}, {"包子", "饺子"}, {"橙子", "柚子"}, {"咖啡", "奶茶"}, {"地铁", "公交"}, {"耳机", "音箱"}, {"雨衣", "雨伞"}, {"牙刷", "牙线"}, {"月亮", "太阳"}, {"冰箱", "空调"}, {"游泳", "潜水"}, {"电影", "电视剧"}, {"键盘", "鼠标"}, {"馒头", "面包"}, {"火锅", "麻辣烫"}, {"熊猫", "北极熊"}, {"篮球", "足球"}, {"书包", "行李箱"}, {"蛋糕", "饼干"}, {"电梯", "扶梯"}}

func ID() string { return rand.Text()[:16] }
func randomIndex(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

func New(players []Player) (*Game, error) {
	if len(players) != 4 && len(players) != 6 {
		return nil, errors.New("仅支持四人或六人局")
	}
	pair, err := randomIndex(len(Pairs))
	if err != nil {
		return nil, err
	}
	under, err := randomIndex(len(players))
	if err != nil {
		return nil, err
	}
	swap, err := randomIndex(2)
	if err != nil {
		return nil, err
	}
	g := &Game{ID: ID(), Players: append([]Player(nil), players...), Words: map[string]string{}, Phase: "dealing", Round: 1, Votes: map[string]string{}, Deadline: time.Now().Add(2500 * time.Millisecond).UnixMilli()}
	for i := range g.Players {
		p := &g.Players[i]
		p.Alive = true
		g.Words[p.ID] = Pairs[pair][swap]
		if i == under {
			g.UndercoverID = p.ID
			g.Words[p.ID] = Pairs[pair][1-swap]
		}
	}
	g.addLog("host", nil, "开始游戏。")
	return g, nil
}
func (g *Game) Player(id string) *Player {
	for i := range g.Players {
		if g.Players[i].ID == id {
			return &g.Players[i]
		}
	}
	return nil
}
func (g *Game) Alive() []Player {
	var ps []Player
	for _, p := range g.Players {
		if p.Alive {
			ps = append(ps, p)
		}
	}
	return ps
}
func (g *Game) order() []string {
	var ids []string
	for _, p := range g.Players {
		if p.Alive && (g.Phase != "tiebreak" || contains(g.Candidates, p.ID)) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}
func (g *Game) CurrentID() string {
	if g.Phase != "speaking" && g.Phase != "tiebreak" {
		return ""
	}
	ids := g.order()
	if g.Turn >= len(ids) {
		return ""
	}
	return ids[g.Turn]
}
func (g *Game) Begin() {
	if g.Phase != "dealing" {
		return
	}
	g.Phase = "speaking"
	g.Turn = 0
	g.Deadline = time.Now().Add(60 * time.Second).UnixMilli()
	g.announceSpeaker()
}
func (g *Game) Speak(id, text string, audio bool) error {
	if g.CurrentID() != id {
		return errors.New("还没轮到你发言")
	}
	text = strings.TrimSpace(text)
	if n := utf8.RuneCountInString(text); n < 1 || n > 160 {
		return errors.New("每次发言请控制在 1 至 160 字")
	}
	if strings.Contains(text, g.Words[id]) {
		return errors.New("描述里包含了你的词，换一种说法吧")
	}
	p := g.Player(id)
	g.addLog("speech", p, text)
	g.Logs[len(g.Logs)-1].Audio = audio
	g.advanceSpeaker()
	return nil
}
func (g *Game) Skip(id string) error        { return g.skip(id, "跳过。") }
func (g *Game) SkipTimeout(id string) error { return g.skip(id, "超时跳过。") }
func (g *Game) skip(id, reason string) error {
	if g.CurrentID() != id {
		return errors.New("还没轮到你发言")
	}
	g.addLog("host", nil, fmt.Sprintf("%d号%s", g.Player(id).Seat, reason))
	g.advanceSpeaker()
	return nil
}
func (g *Game) advanceSpeaker() {
	g.Turn++
	if g.Turn >= len(g.order()) {
		g.Phase = "voting"
		g.Turn = 0
		g.Votes = map[string]string{}
		g.Deadline = time.Now().Add(60 * time.Second).UnixMilli()
		g.addLog("host", nil, "请投票。")
	} else {
		g.Deadline = time.Now().Add(60 * time.Second).UnixMilli()
		g.announceSpeaker()
	}
}
func (g *Game) announceSpeaker() {
	if p := g.Player(g.CurrentID()); p != nil {
		g.addLog("host", nil, fmt.Sprintf("请%d号发言。", p.Seat))
	}
}
func (g *Game) LegalTargets(id string) []string {
	var ids []string
	for _, p := range g.Players {
		if p.Alive && p.ID != id && (len(g.Candidates) == 0 || contains(g.Candidates, p.ID)) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}
func (g *Game) Vote(id, target string) error {
	if g.Phase != "voting" {
		return errors.New("现在还不是投票阶段")
	}
	p := g.Player(id)
	if p == nil || !p.Alive {
		return errors.New("你已出局，正在观战")
	}
	if _, ok := g.Votes[id]; ok {
		return errors.New("你已经投过票了")
	}
	if target != "" && !contains(g.LegalTargets(id), target) {
		return errors.New("请选择其他存活玩家；加投时只能选择平票玩家")
	}
	g.Votes[id] = target
	if len(g.Votes) == len(g.Alive()) {
		g.resolveVotes()
	}
	return nil
}
func (g *Game) resolveVotes() {
	counts := map[string]int{}
	g.LastVotes = nil
	for _, p := range g.Players {
		if !p.Alive {
			continue
		}
		target := g.Votes[p.ID]
		g.LastVotes = append(g.LastVotes, Ballot{p.ID, target})
		if target != "" {
			counts[target]++
		}
	}
	top := 0
	var tied []string
	for _, p := range g.Players {
		if n := counts[p.ID]; n > top {
			top = n
			tied = []string{p.ID}
		} else if n == top && n > 0 {
			tied = append(tied, p.ID)
		}
	}
	parts := []string{}
	for _, p := range g.Players {
		if count := counts[p.ID]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d号%d票", p.Seat, count))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "全部弃权")
	}
	g.addLog("votes", nil, "投票结果："+strings.Join(parts, "，")+"。")
	if top == 0 {
		g.addLog("host", nil, "本轮无人出局。")
		g.nextRound()
		return
	}
	if len(tied) > 1 {
		if len(g.Candidates) > 0 {
			g.addLog("host", nil, "再次平票，无人出局。")
			g.nextRound()
			return
		}
		g.Candidates = tied
		g.Phase = "tiebreak"
		g.Turn = 0
		g.Votes = map[string]string{}
		g.Deadline = time.Now().Add(60 * time.Second).UnixMilli()
		seats := []string{}
		for _, id := range tied {
			seats = append(seats, fmt.Sprintf("%d号", g.Player(id).Seat))
		}
		g.addLog("host", nil, strings.Join(seats, "、")+"平票。")
		g.announceSpeaker()
		return
	}
	out := g.Player(tied[0])
	out.Alive = false
	if out.ID == g.UndercoverID {
		g.Winner = "civilian"
		g.finish(fmt.Sprintf("%d号%d票，是卧底，平民获胜。", out.Seat, top))
		return
	}
	g.addLog("host", nil, fmt.Sprintf("%d号%d票出局，是平民。", out.Seat, top))
	if len(g.Alive()) <= 2 {
		g.Winner = "undercover"
		g.finish("卧底获胜。")
		return
	}
	g.nextRound()
}
func (g *Game) finish(text string) {
	g.Phase = "finished"
	g.Deadline = 0
	g.addLog("host", nil, text)
}
func (g *Game) nextRound() {
	g.Round++
	g.Candidates = nil
	g.Votes = map[string]string{}
	g.Phase = "speaking"
	g.Turn = 0
	g.Deadline = time.Now().Add(60 * time.Second).UnixMilli()
	g.addLog("host", nil, fmt.Sprintf("继续游戏，请%d号发言。", g.Player(g.CurrentID()).Seat))
}
func (g *Game) Reveals() []Reveal {
	if g.Phase != "finished" {
		return nil
	}
	var out []Reveal
	for _, p := range g.Players {
		role := "civilian"
		if p.ID == g.UndercoverID {
			role = "undercover"
		}
		out = append(out, Reveal{p.ID, p.Seat, p.Name, g.Words[p.ID], role})
	}
	return out
}
func (g *Game) addLog(kind string, p *Player, text string) {
	l := Log{ID: ID(), Kind: kind, Text: text, Round: g.Round, At: time.Now().UnixMilli()}
	if p != nil {
		l.PlayerID = p.ID
		l.Seat = p.Seat
		l.Name = p.Name
	}
	g.Logs = append(g.Logs, l)
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// BotInput deliberately excludes identities, other players' words and secret votes.
type BotInput struct {
	PlayerID string         `json:"playerId"`
	Name     string         `json:"name"`
	Word     string         `json:"yourWord"`
	Round    int            `json:"round"`
	Phase    string         `json:"phase"`
	Players  []PublicPlayer `json:"players"`
	Speeches []Log          `json:"publicSpeeches"`
	Targets  []string       `json:"legalTargets"`
}

func (g *Game) BotInput(id string) BotInput {
	p := g.Player(id)
	in := BotInput{PlayerID: id, Name: p.Name, Word: g.Words[id], Round: g.Round, Phase: g.Phase, Targets: g.LegalTargets(id)}
	for _, p := range g.Players {
		in.Players = append(in.Players, PublicPlayer{ID: p.ID, Name: p.Name, Seat: p.Seat, Bot: p.Bot, Alive: p.Alive})
	}
	for _, l := range g.Logs {
		if l.Kind == "speech" {
			in.Speeches = append(in.Speeches, l)
		}
	}
	return in
}
