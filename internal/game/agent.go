package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// A voice conversation owns AI turns and pauses their deadline until the reply finishes.
func (m *Manager) AcquireAgent(id, uid string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || member(r, uid) == nil {
		return nil, errors.New("请先加入房间")
	}
	if m.agentRooms[id] {
		return nil, errors.New("主持正在回应另一位玩家，请稍后再说")
	}
	m.agentRooms[id] = true
	r.AgentActive = true
	r.Version++
	m.notifyLocked(id)
	return func() {
		m.mu.Lock()
		delete(m.agentRooms, id)
		if current := m.rooms[id]; current != nil {
			current.AgentActive = false
			if current.Game != nil && current.Game.Phase != "finished" {
				current.Game.Deadline = time.Now().Add(60 * time.Second).UnixMilli()
			}
			current.Version++
			if err := m.persistLocked(current); err != nil {
				slog.Error("save host conversation deadline", "error", err)
			}
			m.notifyLocked(id)
		}
		m.mu.Unlock()
	}, nil
}

type AITask struct {
	PlayerID string         `json:"playerId"`
	Seat     int            `json:"seat"`
	Name     string         `json:"name"`
	Word     string         `json:"yourWord"`
	Phase    string         `json:"phase"`
	Round    int            `json:"round"`
	Speeches []Log          `json:"publicSpeeches"`
	Targets  []PublicPlayer `json:"legalTargets"`
}

func aiTask(g *Game) *AITask {
	if g == nil {
		return nil
	}
	var p *Player
	if id := g.CurrentID(); id != "" && g.Player(id).Bot {
		p = g.Player(id)
	}
	if g.Phase == "voting" {
		for i := range g.Players {
			v := &g.Players[i]
			if _, voted := g.Votes[v.ID]; v.Bot && v.Alive && !voted {
				p = v
				break
			}
		}
	}
	if p == nil {
		return nil
	}
	task := &AITask{PlayerID: p.ID, Seat: p.Seat, Name: p.Name, Word: g.Words[p.ID], Phase: g.Phase, Round: g.Round, Speeches: []Log{}, Targets: []PublicPlayer{}}
	for _, l := range g.Logs {
		if l.Kind == "speech" {
			task.Speeches = append(task.Speeches, l)
		}
	}
	for _, id := range g.LegalTargets(p.ID) {
		v := g.Player(id)
		task.Targets = append(task.Targets, PublicPlayer{ID: v.ID, Name: v.Name, Seat: v.Seat, Bot: v.Bot, Alive: v.Alive})
	}
	return task
}
func (m *Manager) SafeHostText(id, uid, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || member(r, uid) == nil {
		return "", errors.New("无法访问房间")
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"aibatch", "batchid", "yourword", "currentutterance", "game.", "tools/call", "<think", "</think", "jsonrpc"} {
		if strings.Contains(lower, marker) {
			if g := r.Game; g != nil {
				if p := g.Player(g.CurrentID()); p != nil {
					return fmt.Sprintf("请%d号发言。", p.Seat), nil
				}
				if g.Phase == "voting" {
					return "请投票。", nil
				}
				if g.Phase == "finished" {
					return "本局已结束。", nil
				}
			}
			return "准备好后，说开始游戏。", nil
		}
	}
	if g := r.Game; g != nil && g.Phase != "finished" {
		for _, word := range g.Words {
			if strings.Contains(text, word) {
				return "请查看自己的词卡。", nil
			}
		}
	}
	return text, nil
}
func (m *Manager) AppendHostLog(id, uid string, l Log) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || member(r, uid) == nil {
		return errors.New("无法访问房间")
	}
	b := backup(r)
	l.Kind = "host"
	l.Source = "linx_agent"
	l.At = time.Now().UnixMilli()
	if r.Game != nil {
		l.Round = r.Game.Round
		r.Game.Logs = append(r.Game.Logs, l)
	} else {
		r.Logs = append(r.Logs, l)
		if len(r.Logs) > 50 {
			r.Logs = r.Logs[len(r.Logs)-50:]
		}
	}
	return m.commitLocked(r, b)
}

type AIBatch struct {
	BatchID string   `json:"batchId"`
	Tasks   []AITask `json:"tasks"`
}
type AIAction struct {
	PlayerSeat int    `json:"playerSeat"`
	Text       string `json:"text,omitempty"`
	TargetSeat *int   `json:"targetSeat,omitempty"`
}

func batchID(g *Game) string {
	return fmt.Sprintf("%s/%d/%s/%d/%d/%d", g.ID, g.Round, g.Phase, g.Turn, len(g.Votes), len(g.Candidates))
}
func aiBatch(g *Game) *AIBatch {
	if g == nil {
		return nil
	}
	if g.Phase == "voting" {
		for _, p := range g.Alive() {
			if _, voted := g.Votes[p.ID]; !p.Bot && !voted {
				return nil
			}
		}
	}
	copied := *g
	copied.Votes = map[string]string{}
	for k, v := range g.Votes {
		copied.Votes[k] = v
	}
	batch := &AIBatch{BatchID: batchID(g), Tasks: []AITask{}}
	for i := 0; i < 6; i++ {
		task := aiTask(&copied)
		if task == nil {
			break
		}
		batch.Tasks = append(batch.Tasks, *task)
		if copied.Phase == "voting" {
			copied.Votes[task.PlayerID] = ""
		} else {
			// Commit and narrate one AI description before requesting the next.
			break
		}
	}
	if len(batch.Tasks) == 0 {
		return nil
	}
	return batch
}
func (m *Manager) NextAIBatch(id, uid string) (*AIBatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || member(r, uid) == nil {
		return nil, errors.New("无法访问房间")
	}
	return aiBatch(r.Game), nil
}
func (m *Manager) SubmitAIBatch(id, uid, batch string, actions []AIAction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || member(r, uid) == nil || !m.agentRooms[id] {
		return errors.New("主持会话已结束")
	}
	if r.Game == nil || batchID(r.Game) != batch {
		return errors.New("AI任务已经变化，请读取新的任务组")
	}
	pending := aiBatch(r.Game)
	if pending == nil || len(actions) != len(pending.Tasks) {
		return errors.New("请按任务组顺序完整提交每位AI的操作")
	}
	// Validate the entire group on a copy. An invalid last action must not commit
	// any earlier description or ballot from the same group.
	raw, _ := json.Marshal(r.Game)
	var next Game
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	for _, action := range actions {
		task := aiTask(&next)
		if task == nil || task.Seat != action.PlayerSeat {
			return errors.New("AI座位或顺序不正确")
		}
		var err error
		if task.Phase == "voting" {
			if action.TargetSeat == nil {
				return errors.New("AI投票需明确targetSeat，0表示弃权")
			}
			target := ""
			if *action.TargetSeat != 0 {
				for _, p := range task.Targets {
					if p.Seat == *action.TargetSeat {
						target = p.ID
					}
				}
				if target == "" {
					return errors.New("AI选择了非法投票座位")
				}
			}
			err = next.Vote(task.PlayerID, target)
		} else {
			err = next.Speak(task.PlayerID, action.Text, false)
		}
		if err != nil {
			return err
		}
	}
	before := backup(r)
	r.Game = &next
	return m.commitLocked(r, before)
}

// UseExternalAgent disables demonstration workers when a Linx agent is available.
func (m *Manager) UseExternalAgent() { m.mu.Lock(); m.externalAgent = true; m.mu.Unlock() }

type AgentTurn struct{ RoomID, UserID string }

func (m *Manager) PendingAgentTurns() []AgentTurn {
	m.mu.Lock()
	defer m.mu.Unlock()
	turns := []AgentTurn{}
	for _, r := range m.rooms {
		if m.agentRooms[r.ID] || aiBatch(r.Game) == nil {
			continue
		}
		for _, p := range roomPlayers(r) {
			if !p.Bot && p.UserID != "" {
				turns = append(turns, AgentTurn{r.ID, p.UserID})
				break
			}
		}
	}
	return turns
}
