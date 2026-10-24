package linx

import (
	"context"
	"encoding/json"
	"errors"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}
type ToolHandler func(context.Context, string, json.RawMessage) (any, error)
type SessionOptions struct {
	Tools  []Tool
	Call   ToolHandler
	Prompt map[string]string
}

// HandleMCP answers device-side JSON-RPC requests on the same authenticated socket.
// Replies are cached per request ID so a transport retry cannot repeat a game action.
func (s *Session) HandleMCP(ctx context.Context, data []byte) (bool, error) {
	var message struct {
		Type    string `json:"type"`
		Payload struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &message) != nil || message.Type != "mcp" {
		return false, nil
	}
	rpc := message.Payload
	if len(rpc.ID) == 0 || string(rpc.ID) == "null" {
		return true, nil
	}
	key := string(rpc.ID) + "/" + rpc.Method + "/" + string(rpc.Params)
	if cached := s.mcpReplies[key]; cached != nil {
		return true, s.WriteJSON(cached)
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": rpc.ID}
	switch rpc.Method {
	case "initialize":
		payload["result"] = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "undercover-game", "version": "0.2.0"}}
	case "tools/list":
		payload["result"] = map[string]any{"tools": s.options.Tools}
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		err := json.Unmarshal(rpc.Params, &call)
		allowed := false
		for _, tool := range s.options.Tools {
			if tool.Name == call.Name {
				allowed = true
				break
			}
		}
		var result any
		if err == nil && allowed && s.options.Call != nil {
			result, err = s.options.Call(ctx, call.Name, call.Arguments)
		} else {
			err = errors.New("未知游戏工具")
		}
		content := ""
		if err != nil {
			content = err.Error()
		} else {
			b, e := json.Marshal(result)
			if e != nil {
				err = e
				content = "工具结果编码失败"
			} else {
				content = string(b)
			}
		}
		payload["result"] = map[string]any{"content": []map[string]string{{"type": "text", "text": content}}, "isError": err != nil}
	default:
		payload["error"] = map[string]any{"code": -32601, "message": "未知 MCP 方法"}
	}
	response := map[string]any{"type": "mcp", "session_id": s.SessionID, "payload": payload}
	if len(s.mcpReplies) >= 256 {
		return true, errors.New("too many MCP requests in one conversation")
	}
	s.mcpReplies[key] = response
	return true, s.WriteJSON(response)
}
