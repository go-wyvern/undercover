package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/linx"
)

const hostInstructions = `你是网页多人游戏“谁是卧底”的语音主持，负责完整对话、理解玩家意图并扮演空位AI。Go的game.*工具是真实房间唯一状态来源，不得自行发词、改票或虚构操作成功。连接时的game_context已给出当前房间快照，直接结合本次用户语音选择操作，仅在状态不明确、工具提示冲突或玩家询问现状时调用game.get_state。查询结果中的currentUtterance是本次用户的识别原文，不能遗漏。玩家明确说开始时调用game.start，轮到真人描述时，用户说出的描述本身就是提交意图，不需要再问是否确认；调用game.submit_description即可，Go会采用本次识别原文，说投几号调用game.vote，明确跳过调用game.skip。动作返回中的aiBatch包含当前连续AI任务：一次生成这组全部AI的合法描述或投票，调用game.submit_ai_batch，随后读取返回中的新aiBatch并继续，直到aiBatch为null再等待真人。不要逐个AI反复取任务，不要替真人操作。AI描述12至30字，不含自己的词；投票只看当前任务的词和公开描述，不利用其他AI的词。所有yourWord都是秘密，绝不能念出来；真人的词只在网页私密区域显示。最终回复逐字念出本次已提交的AI描述，每人独立一句，格式“二号：描述。”“三号：描述。”，不要改写；然后根据工具结果说明下一个真人操作。投票结果以工具返回为准。常规流程只使用短句模板：开局“开始游戏，请N号发言。”；描述逐字读“N号：原描述。”，只读本次新提交的AI描述，不重复真人刚说的话或以前的描述；进入投票只说“请投票。”；结算读“投票结果：N号X票，……。N号出局。”，只汇总得票及结果，不逐一复述谁投谁、推理或点评；下一轮说“继续游戏，请N号发言。”；平票说“N号、M号平票，请N号补充发言。”；结束只报胜方。一次回复只覆盖本次新发生的变化和下一步，正常过场最多一句，不重复报幕，不添加寒暄、鼓励、规则说明、复盘或再次开局邀请；用户主动询问时再简短解释。全部依据工具结果，不虚构座位、票数或出局。只播报实际操作结果与下一步提示，不说“先检查当前状态”“让我查询”等准备过程，不读JSON、工具名或推理过程。若补充约束与旧的“每次先查询”要求冲突，以本段为准。`

func gameTools() []linx.Tool {
	schema := func(properties map[string]any, required ...string) map[string]any {
		if properties == nil {
			properties = map[string]any{}
		}
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	seat := map[string]any{"type": "integer", "minimum": 0, "maximum": 6, "description": "座位号，0表示弃权"}
	return []linx.Tool{
		{Name: "game.get_state", Description: hostInstructions + " 返回当前用户可见的房间状态和本次识别原文currentUtterance，不返回真人词卡。", InputSchema: schema(nil)},
		{Name: "game.get_rules", Description: "获取谁是卧底的权威规则。", InputSchema: schema(nil)},
		{Name: "game.start", Description: "玩家明确要求开始或再来一局时发牌，仅房主可用。完成后检查 AI 任务。", InputSchema: schema(nil)},
		{Name: "game.submit_description", Description: "提交当前玩家本次语音描述，通常传空对象即可，Go优先保存本次识别原文；不能编造或替别人发言。完成后继续处理AI任务。", InputSchema: schema(map[string]any{"text": text("无语音识别原文时使用的描述文本")})},
		{Name: "game.vote", Description: "按当前玩家明确说出的座位号投票。不要替玩家猜测投票对象。完成后检查 AI 任务。", InputSchema: schema(map[string]any{"seat": seat}, "seat")},
		{Name: "game.skip", Description: "当前玩家明确要求跳过自己的发言时使用。", InputSchema: schema(nil)},
		{Name: "game.get_ai_batch", Description: "读取下一组连续AI发言/投票任务，yourWord严禁公开；返回null则等待真人。优先直接使用动作结果中的aiBatch，避免重复读取。", InputSchema: schema(nil)},
		{Name: "game.submit_ai_batch", Description: "一次完成aiBatch中的整组AI操作，顺序及数量须与任务组一致。发言每项填playerSeat和text；投票填playerSeat和targetSeat，0表示弃权。返回新的aiBatch，若非null继续提交，若null等待真人。", InputSchema: schema(map[string]any{
			"batchId": text("任务组ID"),
			"actions": map[string]any{"type": "array", "maxItems": 6, "items": schema(map[string]any{"playerSeat": map[string]any{"type": "integer", "minimum": 1, "maximum": 6}, "text": text("12至30字的AI公开描述，不含词语"), "targetSeat": seat}, "playerSeat")},
		}, "batchId", "actions")},
	}
}
func parseToolArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return errors.New("工具参数格式不正确")
	}
	return nil
}
func (a *App) publicAgentState(id, uid string) (any, error) {
	v, err := a.rooms.View(id, uid)
	if err != nil {
		return nil, err
	}
	v.MyWord = ""
	for i := range v.Players {
		v.Players[i].Avatar = ""
	}
	return v, nil
}
func (a *App) gameToolHandler(id, uid string, recording func() []byte, utterance func() string) linx.ToolHandler {
	return func(ctx context.Context, name string, raw json.RawMessage) (result any, err error) {
		defer func() {
			if err != nil {
				slog.Warn("Linx game tool rejected", "tool", name, "error", err)
			}
		}()
		slog.Info("Linx game tool", "tool", name, "room", id)
		var args struct {
			Text    string          `json:"text"`
			Seat    *int            `json:"seat"`
			BatchID string          `json:"batchId"`
			Actions []game.AIAction `json:"actions"`
		}
		if err := parseToolArgs(raw, &args); err != nil {
			return nil, err
		}
		switch name {
		case "game.get_state":
			state, err := a.publicAgentState(id, uid)
			if err != nil {
				return nil, err
			}
			input := ""
			if utterance != nil {
				input = utterance()
			}
			return map[string]any{"room": state, "currentUtterance": input}, nil
		case "game.get_rules":
			return "4人局3平民1卧底，6人局5平民1卧底；词卡只给本人。按座位依次描述，不能直接说出自己的词。所有存活者投票，不能投自己，可弃权。平票者补充描述后加投，再平票则无人出局并开始下一轮。卧底出局平民胜；卧底存活到只剩2人则卧底胜。", nil
		case "game.start":
			_, err = a.rooms.Start(id, uid)
			if err == nil {
				for i := 0; i < 35; i++ {
					v, e := a.rooms.View(id, uid)
					if e != nil {
						return nil, e
					}
					if v.Phase != "dealing" {
						break
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
		case "game.submit_description":
			text := args.Text
			if utterance != nil {
				text = strings.TrimSpace(utterance())
				if text == "" {
					return nil, errors.New("尚未收到本次语音识别原文，不能编造描述，请稍后重试")
				}
			}
			_, err = a.rooms.Speak(id, uid, text, recording())
		case "game.vote":
			if args.Seat == nil {
				return nil, errors.New("必须明确指定座位号")
			}
			v, e := a.rooms.View(id, uid)
			if e != nil {
				return nil, e
			}
			target := ""
			if *args.Seat != 0 {
				for _, p := range v.Players {
					if p.Seat == *args.Seat {
						target = p.ID
					}
				}
				if target == "" {
					return nil, errors.New("没有这个座位")
				}
			}
			_, err = a.rooms.Vote(id, uid, target)
		case "game.skip":
			_, err = a.rooms.Skip(id, uid)
		case "game.get_ai_batch":
			return a.rooms.NextAIBatch(id, uid)
		case "game.submit_ai_batch":
			err = a.rooms.SubmitAIBatch(id, uid, args.BatchID, args.Actions)
		default:
			return nil, errors.New("未知游戏工具")
		}
		if err != nil {
			return nil, err
		}
		state, e := a.publicAgentState(id, uid)
		if e != nil {
			return nil, e
		}
		batch, e := a.rooms.NextAIBatch(id, uid)
		if e != nil {
			return nil, e
		}
		return map[string]any{"room": state, "aiBatch": batch}, nil
	}
}
