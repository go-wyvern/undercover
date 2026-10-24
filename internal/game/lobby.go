package game

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

func lobbyPhase(r *Room) bool { return phase(r) == "waiting" || phase(r) == "finished" }

// Automatically filled seats last for one game. Explicitly reserved AI seats
// remain in the lobby; humans keep their seat numbers when others leave.
func lobbyPlayers(r *Room) []Player {
	if r.Game == nil {
		return append([]Player(nil), r.Players...)
	}
	explicitBots := map[string]bool{}
	for _, p := range r.Players {
		if p.Bot {
			explicitBots[p.ID] = true
		}
	}
	players := []Player{}
	for _, p := range r.Game.Players {
		if !p.Bot || explicitBots[p.ID] {
			p.Alive = true
			players = append(players, p)
		}
	}
	return players
}

func resetReadiness(r *Room) {
	for i := range r.Players {
		r.Players[i].Ready = false
	}
}

func startBlockReason(r *Room) string {
	if !lobbyPhase(r) {
		return "游戏已经开始"
	}
	players := lobbyPlayers(r)
	waiting := []string{}
	for _, p := range players {
		if !p.Bot && p.UserID != r.OwnerID && !p.Ready {
			waiting = append(waiting, fmt.Sprintf("%d号%s", p.Seat, p.Name))
		}
	}
	if len(waiting) > 0 {
		return "等待" + strings.Join(waiting, "、") + "准备"
	}
	if !r.AutoFillAI && len(players) != r.Capacity {
		return "自动 AI 补位已关闭，请等待坐满或由房主添加 AI"
	}
	return ""
}

func (m *Manager) SetReady(id, uid string, ready bool) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil {
		return View{}, ErrRoomNotFound
	}
	p := member(r, uid)
	if p == nil {
		return View{}, ErrNotMember
	}
	if !lobbyPhase(r) {
		return View{}, errors.New("游戏中不能修改准备状态")
	}
	if phase(r) == "waiting" && p.Ready == ready {
		return view(r, uid), nil
	}
	b := backup(r)
	resetFinished(r)
	member(r, uid).Ready = ready
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}

func editableLobby(r *Room, uid string) error {
	if r == nil {
		return ErrRoomNotFound
	}
	if r.OwnerID != uid || member(r, uid) == nil {
		return ErrHostOnly
	}
	if !lobbyPhase(r) {
		return errors.New("请在等待开局时管理座位")
	}
	return nil
}

func (m *Manager) SetAutoFillAI(id, uid string, enabled bool) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if err := editableLobby(r, uid); err != nil {
		return View{}, err
	}
	if r.AutoFillAI == enabled {
		return view(r, uid), nil
	}
	b := backup(r)
	resetFinished(r)
	r.AutoFillAI = enabled
	resetReadiness(r)
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}

func botName(seat int) string {
	names := []string{"一禾", "小北", "阿岚", "大乔", "阿泽", "小满"}
	return names[seat-1]
}

func (m *Manager) SetAISeat(id, uid string, seat int, bot bool) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if err := editableLobby(r, uid); err != nil {
		return View{}, err
	}
	if seat < 1 || seat > r.Capacity {
		return View{}, errors.New("座位号不正确")
	}
	players := lobbyPlayers(r)
	index := -1
	for i, p := range players {
		if p.Seat == seat {
			index = i
			if !p.Bot {
				return View{}, errors.New("该座位已有真人，请先移除成员")
			}
		}
	}
	if (index >= 0) == bot {
		return view(r, uid), nil
	}
	b := backup(r)
	resetFinished(r)
	if bot {
		r.Players = append(r.Players, Player{ID: ID(), Name: botName(seat), Seat: seat, Bot: true, Alive: true})
		sort.Slice(r.Players, func(i, j int) bool { return r.Players[i].Seat < r.Players[j].Seat })
	} else {
		r.Players = append(r.Players[:index], r.Players[index+1:]...)
	}
	resetReadiness(r)
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}

func (m *Manager) RemoveMember(id, uid, playerID string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if err := editableLobby(r, uid); err != nil {
		return View{}, err
	}
	players := lobbyPlayers(r)
	index := -1
	for i, p := range players {
		if p.ID == playerID {
			if p.Bot || p.UserID == uid {
				return View{}, errors.New("请选择其他真人成员；房主离席请使用离开房间")
			}
			index = i
			break
		}
	}
	if index < 0 {
		return View{}, errors.New("该成员已不在房间中")
	}
	b := backup(r)
	resetFinished(r)
	r.Players = append(r.Players[:index], r.Players[index+1:]...)
	resetReadiness(r)
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}
