// Package linx implements the documented OTA and audio WebSocket protocols.
// Account API keys and device tokens never leave the Go server.
package linx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
)

var ErrBusy = errors.New("语音通道正在使用，请稍后重试")

type Config struct {
	DeviceIDs                            []string
	WSURL, Token, OTAURL, APIURL, APIKey string
}
type Provider struct {
	cfg       Config
	available chan string
	client    *http.Client
	otaMu     sync.Mutex
	otaCache  map[string]cachedOTA
}

type cachedOTA struct {
	response OTAResponse
	expires  time.Time
}
type OTAResponse struct {
	Error      string `json:"error,omitempty"`
	Activation *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"activation,omitempty"`
	WebSocket struct {
		URL   string `json:"url"`
		Token string `json:"token,omitempty"`
	} `json:"websocket"`
}
type Message struct {
	IsAborted   bool   `json:"is_aborted,omitempty"`
	Type        string `json:"type"`
	State       string `json:"state,omitempty"`
	Text        string `json:"text,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	AudioParams struct {
		Format        string `json:"format"`
		SampleRate    int    `json:"sample_rate"`
		FrameDuration int    `json:"frame_duration"`
	} `json:"audio_params,omitempty"`
}
type Session struct {
	Conn                      *websocket.Conn
	SessionID                 string
	SampleRate, FrameDuration int
	AudioFormat               string
	options                   SessionOptions
	mcpReplies                map[string]any
	release                   func()
	once                      sync.Once
	writeMu                   sync.Mutex
	done                      chan struct{}
}

func New(cfg Config) (*Provider, error) {
	if cfg.WSURL == "" {
		cfg.WSURL = "wss://xrobo-io.qiniuapi.com/v1/ws/"
	}
	if cfg.OTAURL == "" {
		cfg.OTAURL = "https://xrobo.qiniuapi.com/v1/ota/"
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://xrobo.qiniu.com"
	}
	p := &Provider{cfg: cfg, available: make(chan string, len(cfg.DeviceIDs)), client: &http.Client{Timeout: 10 * time.Second}, otaCache: map[string]cachedOTA{}}
	seen := map[string]bool{}
	for _, id := range cfg.DeviceIDs {
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if mac, err := net.ParseMAC(id); err != nil || len(mac) != 6 {
			return nil, fmt.Errorf("invalid LINX_DEVICE_IDS entry %q", id)
		}
		if !seen[id] {
			p.available <- id
			seen[id] = true
		}
	}
	return p, nil
}
func (p *Provider) Configured() bool { return len(p.cfg.DeviceIDs) > 0 }
func clientUUID(s string) string {
	b := sha256.Sum256([]byte(s))
	v := append([]byte(nil), b[:16]...)
	v[6] = (v[6] & 15) | 64
	v[8] = (v[8] & 63) | 128
	h := hex.EncodeToString(v)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (p *Provider) OTA(ctx context.Context, device, clientID string) (OTAResponse, error) {
	key := device + "/" + clientID
	p.otaMu.Lock()
	cached, ok := p.otaCache[key]
	p.otaMu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.response, nil
	}
	uuid := clientUUID(clientID)
	body, _ := json.Marshal(map[string]any{"mac_address": device, "uuid": uuid, "application": map[string]string{"name": "undercover-web", "version": "0.1.0", "elf_sha256": strings.Repeat("0", 64)}, "board": map[string]any{"type": "xiaoling-web-test", "ssid": "web", "rssi": 0}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.OTAURL, bytes.NewReader(body))
	if err != nil {
		return OTAResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Activation-Version", "1")
	req.Header.Set("Device-Id", device)
	req.Header.Set("Client-Id", uuid)
	req.Header.Set("User-Agent", "undercover-web/0.1.0")
	req.Header.Set("Accept-Language", "zh-CN")
	res, err := p.client.Do(req)
	if err != nil {
		return OTAResponse{}, fmt.Errorf("OTA connection failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return OTAResponse{}, fmt.Errorf("OTA returned HTTP %d", res.StatusCode)
	}
	var out OTAResponse
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, errors.New("OTA rejected device report")
	}
	// A short in-memory cache avoids another OTA round trip for every press.
	// Activation codes are never cached; rejected tokens are refreshed on dial.
	if out.WebSocket.Token != "" && out.Activation == nil {
		p.otaMu.Lock()
		p.otaCache[key] = cachedOTA{response: out, expires: time.Now().Add(time.Minute)}
		p.otaMu.Unlock()
	}
	return out, nil
}
func (p *Provider) Agent(ctx context.Context, id string) (json.RawMessage, error) {
	if p.cfg.APIKey == "" {
		return nil, errors.New("LINX_API_KEY is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.cfg.APIURL, "/")+"/xiaozhi/agent/search?type=agent_id&q="+url.QueryEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("agent API returned %d", res.StatusCode)
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("agent API code %d", envelope.Code)
	}
	return envelope.Data, nil
}
func (p *Provider) dialDevice(ctx context.Context, device string) (*websocket.Conn, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, wsURL := p.cfg.Token, p.cfg.WSURL
		if token == "" {
			out, err := p.OTA(ctx, device, "undercover/"+device)
			if err != nil {
				return nil, err
			}
			if out.WebSocket.Token == "" {
				return nil, errors.New("灵矽软件终端尚未绑定智能体，请先执行设备接入配置")
			}
			token = out.WebSocket.Token
			if out.WebSocket.URL != "" {
				wsURL = out.WebSocket.URL
			}
		}
		u, err := url.Parse(wsURL)
		if err != nil {
			return nil, errors.New("invalid voice URL")
		}
		if u.Scheme == "ws" && strings.HasSuffix(u.Hostname(), ".qiniuapi.com") {
			u.Scheme = "wss"
		}
		if u.Scheme != "wss" && !(u.Scheme == "ws" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
			return nil, errors.New("voice connection requires WSS")
		}
		headers := http.Header{"Authorization": {"Bearer " + token}, "Device-Id": {device}, "Client-Id": {clientUUID("undercover/" + device)}, "Protocol-Version": {"1"}}
		dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second, Proxy: http.ProxyFromEnvironment}
		conn, response, err := dialer.DialContext(ctx, u.String(), headers)
		if err == nil {
			return conn, nil
		}
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if p.cfg.Token == "" && attempt == 0 && response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			p.otaMu.Lock()
			delete(p.otaCache, device+"/undercover/"+device)
			p.otaMu.Unlock()
			continue
		}
		return nil, fmt.Errorf("voice handshake failed: %w", err)
	}
	return nil, errors.New("voice authentication failed")
}

func (p *Provider) Open(ctx context.Context, identity string) (*Session, error) {
	return p.OpenWithTools(ctx, identity, SessionOptions{})
}
func (p *Provider) OpenWithTools(ctx context.Context, identity string, options SessionOptions) (*Session, error) {
	startedAt := time.Now()
	if !p.Configured() {
		return nil, errors.New("灵矽语音尚未配置")
	}
	var device string
	select {
	case device = <-p.available:
	default:
		if len(options.Tools) == 0 {
			return nil, ErrBusy
		}
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		select {
		case device = <-p.available:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, ErrBusy
		}
	}
	release := func() { p.available <- device }
	acquiredAt := time.Now()
	conn, err := p.dialDevice(ctx, device)
	if err != nil {
		release()
		return nil, err
	}
	dialedAt := time.Now()

	s := &Session{Conn: conn, release: release, SampleRate: 24000, FrameDuration: 60, AudioFormat: "opus", done: make(chan struct{}), options: options, mcpReplies: map[string]any{}}
	conn.SetReadLimit(2 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	hello := map[string]any{"type": "hello", "version": 1, "transport": "websocket", "features": map[string]any{"mcp": len(options.Tools) > 0}, "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000, "channels": 1, "bit_depth": 16, "endianness": "little", "frame_duration": 20, "frame_size": 320, "sample_format": "signed_int16", "play_buffer_duration": 200}}
	if len(options.Prompt) > 0 {
		hello["agent_params"] = map[string]any{"custom_replace_prompt": options.Prompt}
	}
	err = s.WriteJSON(hello)
	if err != nil {
		s.Close()
		return nil, err
	}
	for i := 0; i < 12; i++ {
		kind, b, err := conn.ReadMessage()
		if err != nil {
			s.Close()
			return nil, err
		}
		if kind != websocket.TextMessage {
			continue
		}
		if handled, e := s.HandleMCP(ctx, b); handled {
			if e != nil {
				s.Close()
				return nil, e
			}
			continue
		}
		var msg Message
		if json.Unmarshal(b, &msg) == nil && msg.Type == "hello" {
			s.SessionID = msg.SessionID
			if msg.AudioParams.Format != "" {
				s.AudioFormat = msg.AudioParams.Format
			}
			if msg.AudioParams.SampleRate > 0 {
				s.SampleRate = msg.AudioParams.SampleRate
			}
			if msg.AudioParams.FrameDuration > 0 {
				s.FrameDuration = msg.AudioParams.FrameDuration
			}
			_ = conn.SetReadDeadline(time.Time{})
			go func() {
				select {
				case <-ctx.Done():
					s.Close()
				case <-s.done:
				}
			}()
			slog.Info("Linx connection stages", "queue_ms", acquiredAt.Sub(startedAt).Milliseconds(), "dial_ms", dialedAt.Sub(acquiredAt).Milliseconds(), "hello_ms", time.Since(dialedAt).Milliseconds())
			return s, nil
		}
	}
	s.Close()
	return nil, errors.New("missing voice hello response")
}
func (s *Session) WriteJSON(v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.Conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	return s.Conn.WriteJSON(v)
}
func (s *Session) WritePCM(b []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.Conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	return s.Conn.WriteMessage(websocket.BinaryMessage, b)
}
func (s *Session) Close() { s.once.Do(func() { close(s.done); _ = s.Conn.Close(); s.release() }) }
func (s *Session) Listen(state string) error {
	return s.WriteJSON(map[string]any{"type": "listen", "state": state, "mode": "manual", "session_id": s.SessionID})
}

func (p *Provider) Synthesize(ctx context.Context, identity, text string) ([]byte, error) {
	s, err := p.Open(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.synthesizeText(ctx, text, false)
}

// SynthesizeText reuses a game session after its tool has committed. Only audio
// whose sentence text matches this authoritative line can leave this path.
func (s *Session) SynthesizeText(ctx context.Context, text string) ([]byte, error) {
	return s.synthesizeText(ctx, text, true)
}
func speechText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
}
func (s *Session) synthesizeText(ctx context.Context, text string, verifyText bool) ([]byte, error) {
	deadline := time.Now().Add(30 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	_ = s.Conn.SetReadDeadline(deadline)
	defer s.Conn.SetReadDeadline(time.Time{})
	var err error
	if err = s.WriteJSON(map[string]any{"type": "abort", "session_id": s.SessionID}); err != nil {
		return nil, err
	}
	if err = s.WriteJSON(map[string]any{"type": "listen", "state": "detect", "session_id": s.SessionID, "text": "游戏播报", "text_response": text}); err != nil {
		return nil, err
	}
	var packets [][]byte
	size := 0
	var spoken strings.Builder
	allowed := !verifyText
	expected := speechText(text)
	for {
		kind, b, err := s.Conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if kind == websocket.BinaryMessage {
			if !allowed {
				continue
			}
			if len(b) > 65024 {
				return nil, errors.New("invalid Opus packet size")
			}
			size += len(b)
			if size > 4<<20 {
				return nil, errors.New("voice response too large")
			}
			packets = append(packets, append([]byte(nil), b...))
			continue
		}
		var m Message
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Type == "tts" && (m.State == "start" || m.State == "stop" && m.IsAborted) {
			packets = nil
			size = 0
			spoken.Reset()
			allowed = !verifyText
			continue
		}
		if verifyText && m.Type == "tts" && m.State == "sentence_start" {
			spoken.WriteString(speechText(m.Text))
			allowed = strings.HasPrefix(expected, spoken.String())
			if !allowed {
				packets = nil
				size = 0
			}
			continue
		}
		if verifyText && m.Type == "tts" && m.State == "stop" && spoken.String() != expected {
			packets = nil
			size = 0
			spoken.Reset()
			allowed = false
			continue
		}
		if m.Type == "tts" && m.State == "stop" && len(packets) > 0 {
			if s.AudioFormat == "pcm" {
				return PCMToWAVAtRate(bytes.Join(packets, nil), s.SampleRate), nil
			}
			if s.AudioFormat != "opus" {
				return nil, errors.New("unsupported voice response format")
			}
			return OpusOgg(packets, s.SampleRate), nil
		}
	}
}
