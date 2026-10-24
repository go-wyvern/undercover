package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/linx"
)

const hostInstructions = `你是网页多人游戏“谁是卧底”的语音主持，理解玩家语音并扮演空位AI。Go的game.*工具是真实房间唯一状态来源，不得自行发词、改票或虚构操作成功。连接时的game_context已给出当前房间快照，座位、姓名、真人或AI身份只以该快照players为准，禁止套用单机模式的固定姓名。只说普通话中文，不输出英文思考。仅在状态不明确、工具冲突或玩家询问现状时调用game.get_state；其中currentUtterance是本次识别原文，不能遗漏。玩家明确说开始时调用game.start；轮到真人描述时，描述本身就是提交意图，调用game.submit_description即可，Go采用本次识别原文，不需要确认；说投几号调用game.vote；明确跳过调用game.skip。真人语音会话不提供AI任务工具；轮到真人描述就提交其识别原文，绝不能查询AI任务后遗漏真人发言。一次会话只提交一次游戏动作，成功后结束，不再处理后续玩家。Go会立即把本次已提交发言与下一步提示按顺序交给灵矽TTS播报，包含真人自己的发言；不要再生成总结、重述或额外主播播报。后续AI由后台自动继续，不能等待多位AI都提交完才播报。自动AI回合调用game.get_ai_batch读取任务：描述阶段只有一位AI，生成其12至30字描述，调用game.submit_ai_batch提交一次；投票阶段可按任务组完整提交多位AI的投票。提交后结束当前会话，不继续读取下一组任务。不要替真人操作。AI只利用自己的yourWord与公开描述推理，禁止利用其他AI的词或隐藏身份；投票只能选择legalTargets。所有yourWord都是秘密，真人的词只在网页私密区域显示，任何回复都不能念出。若没有AI任务，等待真人，不解释任务为何为空。用户询问状态时查询game.get_state，询问规则时查询game.get_rules。没有执行游戏动作时，由Go按实际房间状态返回中文提示或规则；不要自由生成报幕、改动座位或说玩家跳过。任何情况下都不输出工具名、JSON、变量、aiBatch、null、思考分析、准备查询或自我纠正过程，不添加寒暄、鼓励、规则复述、复盘或再次开局邀请。播报票数、出局和胜负必须依据真实工具结果。以上网页模式优先于后文单机模式。`

const gameRules = "4人局3平民1卧底，6人局5平民1卧底；词卡只给本人。按座位依次描述，不能直接说出自己的词。所有存活者投票，不能投自己，可弃权。平票者补充描述后加投，再平票则无人出局并开始下一轮。卧底出局平民胜；卧底存活到只剩2人则卧底胜。"

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
		{Name: "game.start", Description: "玩家明确要求开始或再来一局时发牌，仅房主可用。成功后结束当前会话，由Go播报并自动继续。", InputSchema: schema(nil)},
		{Name: "game.submit_description", Description: "提交当前玩家本次语音描述，通常传空对象即可，Go优先保存本次识别原文；不能编造或替别人发言。成功后结束当前会话，由Go播报并自动继续。", InputSchema: schema(map[string]any{"text": text("无语音识别原文时使用的描述文本")})},
		{Name: "game.vote", Description: "按当前玩家明确说出的座位号投票。不要替玩家猜测投票对象。成功后结束当前会话，由Go播报并自动继续。", InputSchema: schema(map[string]any{"seat": seat}, "seat")},
		{Name: "game.skip", Description: "当前玩家明确要求跳过自己的发言时使用。", InputSchema: schema(nil)},
		{Name: "game.get_ai_batch", Description: "读取当前AI任务，描述阶段只有一位AI，投票阶段可包含多位；yourWord严禁公开。返回null时等待真人，不解释内部任务状态。", InputSchema: schema(nil)},
		{Name: "game.submit_ai_batch", Description: "一次完成aiBatch中的整组AI操作，顺序及数量须与任务组一致。发言每项填playerSeat和text；投票填playerSeat和targetSeat，0表示弃权。提交成功后结束本次会话，Go播报后自动继续下一位。", InputSchema: schema(map[string]any{
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
	v.MyDraft = ""
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
			return gameRules, nil
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
			if utterance != nil && !explicitSkip(utterance()) {
				return nil, errors.New("玩家没有明确要求跳过，不能把已识别的发言当作跳过")
			}
			_, err = a.rooms.Skip(id, uid)
		case "game.get_ai_batch":
			batch, e := a.rooms.NextAIBatch(id, uid)
			if e != nil {
				return nil, e
			}
			state, e := a.publicAgentState(id, uid)
			if e != nil {
				return nil, e
			}
			input := ""
			if utterance != nil {
				input = utterance()
			}
			return map[string]any{"room": state, "aiBatch": batch, "currentUtterance": input}, nil
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
		return map[string]any{"room": state, "nextAction": "本次动作已提交，Go将依序播报；结束当前会话，后续AI由后台自动继续。"}, nil
	}
}

func explicitSkip(text string) bool {
	switch compactInput(text) {
	case "跳过", "我跳过", "跳过发言", "这轮跳过", "这轮我跳过", "我不想发言", "我不想说", "过", "pass":
		return true
	}
	return false
}
func compactInput(text string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text))
}
func readOnlyQuestion(text string) bool {
	normalized := compactInput(text)
	for _, question := range []string{"轮到谁", "该谁发言", "谁发言", "现在什么情况", "现在什么状态", "我已经说了", "我已经发言", "我说过了", "当前状态", "游戏规则", "怎么玩", "怎么投票", "怎么发言", "怎么开始", "还有谁活着", "谁还活着", "我的词是什么", "我的词是啥", "再说一遍规则"} {
		if strings.Contains(normalized, question) {
			return true
		}
	}
	return false
}
