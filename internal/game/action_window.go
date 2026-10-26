package game

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	playbackWait    = 120 * time.Second
	actionTime      = 60 * time.Second
	processingLimit = 100 * time.Second
)

var ErrPlaybackPending = errors.New("请先完成前置播报或切换文字参与")
var ErrStalePlayback = errors.New("回合已变化，请刷新当前状态")

// ActionWindow is persisted independently of transient voice connections.
// Key identifies an opportunity to act, not a room version or an SSE delivery.
type ActionWindow struct {
	ID         string            `json:"id"`
	Key        string            `json:"key"`
	BoundaryID string            `json:"boundaryId"`
	LogIDs     []string          `json:"logIds"`
	Players    []string          `json:"players"`
	Ready      map[string]string `json:"ready"`
	State      string            `json:"state"`
	WaitUntil  int64             `json:"waitUntil"`
	Forced     bool              `json:"forced"`
}

type ActionView struct {
	ID         string   `json:"id"`
	BoundaryID string   `json:"boundaryId"`
	LogIDs     []string `json:"logIds"`
	State      string   `json:"state"`
	WaitUntil  int64    `json:"waitUntil"`
	Required   bool     `json:"required"`
	Ready      bool     `json:"ready"`
	TextOnly   bool     `json:"textOnly"`
}

func actionKey(g *Game) string {
	return fmt.Sprintf("%s/%d/%s/%d/%s", g.ID, g.Round, g.Phase, g.Turn, strings.Join(g.Candidates, ","))
}

func actionPlayers(g *Game) []string {
	ids := []string{}
	for _, p := range g.Players {
		if p.Bot || !p.Alive {
			continue
		}
		if (g.Phase == "speaking" || g.Phase == "tiebreak") && p.ID == g.CurrentID() {
			ids = append(ids, p.ID)
		}
		if g.Phase == "voting" {
			if _, voted := g.Votes[p.ID]; !voted {
				ids = append(ids, p.ID)
			}
		}
	}
	return ids
}

func currentWindow(r *Room) *ActionWindow {
	if r.Game != nil && r.Action != nil && r.Action.Key == actionKey(r.Game) {
		return r.Action
	}
	return nil
}

func actionView(r *Room, playerID string) *ActionView {
	w := currentWindow(r)
	if w == nil {
		return nil
	}
	mode := w.Ready[playerID]
	return &ActionView{ID: w.ID, BoundaryID: w.BoundaryID, LogIDs: append([]string{}, w.LogIDs...), State: w.State,
		WaitUntil: w.WaitUntil, Required: contains(w.Players, playerID), Ready: mode != "", TextOnly: contains(w.Players, playerID) && (w.Forced || (mode != "" && mode != "played"))}
}

// Reconcile at every durable mutation so all entry points share the same clock.
func (m *Manager) syncActionLocked(r *Room, now time.Time) bool {
	g := r.Game
	if g == nil || g.Phase == "dealing" || g.Phase == "finished" {
		changed := r.Action != nil
		r.Action = nil
		return changed
	}
	players := actionPlayers(g)
	if len(players) == 0 {
		if w := currentWindow(r); w != nil && w.State == "waiting_playback" {
			w.State = "active"
			w.Players = nil
			g.Deadline = now.Add(actionTime).UnixMilli()
			return true
		}
		if g.Deadline == 0 {
			g.Deadline = now.Add(actionTime).UnixMilli()
			return true
		}
		return false // AI generation is bounded by its server session, not a listener.
	}
	w := currentWindow(r)
	changed := false
	if w == nil {
		after := ""
		if r.Action != nil {
			after = r.Action.BoundaryID
		}
		start := 0
		for i, l := range g.Logs {
			if l.ID == after {
				start = i + 1
				break
			}
		}
		ids := []string{}
		for _, l := range g.Logs[start:] {
			if l.Source != "linx_agent" {
				ids = append(ids, l.ID)
			}
		}
		boundary := ""
		if len(ids) > 0 {
			boundary = ids[len(ids)-1]
		}
		w = &ActionWindow{ID: ID(), Key: actionKey(g), BoundaryID: boundary, LogIDs: ids, Players: players,
			Ready: map[string]string{}, State: "waiting_playback", WaitUntil: now.Add(playbackWait).UnixMilli()}
		r.Action = w
		g.Deadline = 0
		changed = true
	}
	if w.State != "waiting_playback" {
		return changed
	}
	// A departure removes that player's vote from the readiness barrier.
	w.Players = players
	allReady := true
	for _, id := range players {
		if w.Ready[id] == "" {
			allReady = false
		}
	}
	if now.UnixMilli() >= w.WaitUntil {
		w.Forced = true
		allReady = true
	}
	if allReady && !m.agentRooms[r.ID] {
		w.State = "active"
		g.Deadline = now.Add(actionTime).UnixMilli()
		changed = true
	}
	return changed
}

func requireAction(r *Room) error {
	if w := currentWindow(r); w != nil && w.State != "active" {
		return ErrPlaybackPending
	}
	return nil
}

func (m *Manager) PlaybackReady(id, uid, turnID, boundaryID, result string) (View, error) {
	if result != "played" && result != "muted" && result != "unavailable" {
		return View{}, errors.New("播报完成状态不正确")
	}
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
	w := currentWindow(r)
	if w == nil || w.ID != turnID || w.BoundaryID != boundaryID {
		return View{}, ErrStalePlayback
	}
	if !contains(w.Players, p.ID) {
		return View{}, errors.New("不能代替其他玩家确认播报")
	}
	if w.State == "active" || w.Ready[p.ID] != "" {
		return view(r, uid), nil
	}
	b := backup(r)
	w.Ready[p.ID] = result
	if err := m.commitLocked(r, b); err != nil {
		return View{}, err
	}
	return view(r, uid), nil
}
