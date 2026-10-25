package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-wyvern/undercover/internal/auth"
	"github.com/go-wyvern/undercover/internal/game"
	"github.com/go-wyvern/undercover/internal/linx"
	"github.com/go-wyvern/undercover/internal/storage"
	"github.com/go-wyvern/undercover/web"
	"github.com/gorilla/websocket"
)

type Config struct {
	Auth      auth.Config
	Linx      linx.Config
	RoomStore storage.RoomStore
}
type audioCall struct {
	done chan struct{}
	data []byte
	err  error
}

type App struct {
	auth         *auth.Auth
	rooms        *game.Manager
	voice        *linx.Provider
	cfg          Config
	handler      http.Handler
	cancel       context.CancelFunc
	ctx          context.Context
	audioMu      sync.Mutex
	audioCache   map[string][]byte
	audioBytes   int
	commandMu    sync.Mutex
	commandPCM   []byte
	audioPending map[string]*audioCall
}

func New(cfg Config) (*App, error) {
	a, err := auth.New(cfg.Auth)
	if err != nil {
		return nil, err
	}
	voice, err := linx.New(cfg.Linx)
	if err != nil {
		return nil, err
	}
	rooms, err := game.NewManager(cfg.RoomStore, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{auth: a, rooms: rooms, voice: voice, cfg: cfg, ctx: ctx, cancel: cancel, audioCache: map[string][]byte{}, audioPending: map[string]*audioCall{}}
	app.handler = app.routes()
	if voice.Configured() {
		rooms.UseExternalAgent()
		go app.runAgentTurns(ctx)
	}
	go rooms.Run(ctx)
	return app, nil
}
func (a *App) Close()                { a.cancel() }
func (a *App) Handler() http.Handler { return a.handler }
func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"voiceConfigured": a.voice.Configured(), "aiConfigured": a.voice.Configured(), "authEnabled": a.auth.Enabled(), "allowGuest": a.auth.AllowGuest()})
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.auth.Current(r)
		if err != nil {
			fail(w, 503, err)
			return
		}
		room := ""
		if u != nil {
			room = a.rooms.CurrentRoom(u.ID)
		}
		reply(w, 200, map[string]any{"user": u, "currentRoomId": room, "enabled": a.auth.Enabled(), "allowGuest": a.auth.AllowGuest()})
	})
	mux.HandleFunc("POST /api/auth/guest", a.auth.Guest)
	mux.HandleFunc("GET /auth/login", a.auth.Login)
	mux.HandleFunc("GET /auth/callback", a.auth.Callback)
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.auth.Current(r)
		if err != nil {
			fail(w, 503, err)
			return
		}
		if u != nil {
			if id := a.rooms.CurrentRoom(u.ID); id != "" {
				if err := a.rooms.Leave(id, u.ID); err != nil {
					fail(w, 400, err)
					return
				}
			}
		}
		a.auth.Logout(w, r)
	})
	mux.HandleFunc("GET /api/rooms", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"rooms": a.rooms.List()}) })
	mux.HandleFunc("POST /api/rooms", a.withUser(a.createRoom))
	mux.HandleFunc("GET /api/rooms/{id}", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		v, err := a.rooms.View(r.PathValue("id"), u.ID)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/join", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var in struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &in) {
			return
		}
		v, err := a.rooms.JoinWithPassword(r.PathValue("id"), u.ID, u.DisplayName, u.Avatar, in.Password)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/leave", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		if err := a.rooms.Leave(r.PathValue("id"), u.ID); err != nil {
			fail(w, 400, err)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("DELETE /api/rooms/{id}", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id := r.PathValue("id")
		if err := a.rooms.CloseRoom(id, u.ID); err != nil {
			respond(w, nil, err)
			return
		}
		a.audioMu.Lock()
		for key, data := range a.audioCache {
			if strings.HasPrefix(key, id+"/") {
				a.audioBytes -= len(data)
				delete(a.audioCache, key)
			}
		}
		a.audioMu.Unlock()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/rooms/{id}/playback-ready", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var in struct {
			TurnID     string `json:"turnId"`
			BoundaryID string `json:"throughLogId"`
			Result     string `json:"result"`
		}
		if !decode(w, r, &in) {
			return
		}
		v, err := a.rooms.PlaybackReady(r.PathValue("id"), u.ID, in.TurnID, in.BoundaryID, in.Result)
		respond(w, v, err)
	}))
	a.lobbyRoutes(mux)
	mux.HandleFunc("POST /api/rooms/{id}/start", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		v, err := a.rooms.Start(r.PathValue("id"), u.ID)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/speech", a.withUser(a.speech))
	mux.HandleFunc("POST /api/rooms/{id}/skip", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		v, err := a.rooms.Skip(r.PathValue("id"), u.ID)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/vote", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var in struct {
			TargetID string `json:"targetId"`
		}
		if !decode(w, r, &in) {
			return
		}
		v, err := a.rooms.Vote(r.PathValue("id"), u.ID, in.TargetID)
		respond(w, v, err)
	}))
	mux.HandleFunc("GET /api/rooms/{id}/events", a.withUser(a.events))
	mux.HandleFunc("GET /api/rooms/{id}/assistant/voice", a.withUser(a.assistantVoice))
	mux.HandleFunc("GET /api/rooms/{id}/voice", a.withUser(a.voiceSocket))
	mux.HandleFunc("GET /api/rooms/{id}/audio/{event}", a.withUser(a.audio))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, errors.New("接口不存在")) })
	assets := web.Assets()
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." {
			name = "index.html"
		}
		if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		b, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			http.Error(w, "frontend is unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if !sameOrigin(r) {
				fail(w, 403, errors.New("请求来源不受信任"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func sameOrigin(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && strings.EqualFold(u.Host, r.Host) && (u.Scheme == "http" || u.Scheme == "https")
	}
	return true
}
func (a *App) withUser(next func(http.ResponseWriter, *http.Request, *auth.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.auth.Current(r)
		if err != nil {
			fail(w, 503, err)
			return
		}
		if u == nil {
			fail(w, 401, errors.New("请先登录或以游客身份入座"))
			return
		}
		if err := a.rooms.SyncAvatar(u.ID, u.Avatar); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		next(w, r, u)
	}
}
func (a *App) createRoom(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Name       string `json:"name"`
		Theme      string `json:"theme"`
		Capacity   int    `json:"capacity"`
		Password   string `json:"password"`
		AutoFillAI *bool  `json:"autoFillAI"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := a.rooms.CreateWithOptions(u.ID, u.DisplayName, in.Name, in.Theme, in.Capacity, u.Avatar, game.CreateOptions{Password: in.Password, AutoFillAI: in.AutoFillAI})
	if err != nil {
		fail(w, 400, err)
		return
	}
	respond(w, v, nil)
}
func (a *App) speech(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Text  string `json:"text"`
		Audio string `json:"audio,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	var pcm []byte
	var err error
	if in.Audio != "" {
		pcm, err = base64.StdEncoding.DecodeString(in.Audio)
		if err != nil {
			fail(w, 400, errors.New("语音数据无效，请重新录制"))
			return
		}
	}
	v, err := a.rooms.Speak(r.PathValue("id"), u.ID, in.Text, pcm)
	respond(w, v, err)
}
func (a *App) events(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id := r.PathValue("id")
	updates, unsubscribe := a.rooms.Subscribe(id)
	defer unsubscribe()
	v, err := a.rooms.View(id, u.ID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(v game.View) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: room\nid: %d\ndata: %s\n\n", v.Version, b)
		f.Flush()
		return err == nil
	}
	if !send(v) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			if user, err := a.auth.Current(r); err != nil || user == nil {
				return
			}
			v, err := a.rooms.View(id, u.ID)
			if err != nil {
				if errors.Is(err, game.ErrRoomNotFound) {
					_, _ = fmt.Fprint(w, "event: closed\ndata: {\"message\":\"房主已关闭房间\"}\n\n")
					f.Flush()
				} else if errors.Is(err, game.ErrNotMember) {
					_, _ = fmt.Fprint(w, "event: removed\ndata: {\"message\":\"你已离开房间或被房主移除\"}\n\n")
					f.Flush()
				}
				return
			}
			if !send(v) {
				return
			}
		case <-ticker.C:
			if user, err := a.auth.Current(r); err != nil || user == nil {
				return
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			f.Flush()
		}
	}
}
func (a *App) voiceSocket(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id := r.PathValue("id")
	v, err := a.rooms.View(id, u.ID)
	if err != nil {
		fail(w, 403, err)
		return
	}
	var me game.PublicPlayer
	for _, p := range v.Players {
		if p.ID == v.MyPlayerID {
			me = p
		}
	}
	canSpeak := (v.Phase == "speaking" || v.Phase == "tiebreak") && v.CurrentPlayerID == me.ID
	canVote := v.Phase == "voting" && me.Alive && !me.HasVoted
	if (!canSpeak && !canVote) || !game.VoiceTurnAllowed(v) {
		fail(w, 409, errors.New("请等待自己的发言或投票回合"))
		return
	}
	if !a.voice.Configured() {
		fail(w, 503, errors.New("语音暂不可用，请使用文字发言"))
		return
	}
	up := websocket.Upgrader{HandshakeTimeout: 8 * time.Second, CheckOrigin: sameOrigin}
	front, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer front.Close()
	front.SetReadLimit(4096)
	ctx, cancel := a.roomContext(r.Context(), id, 45*time.Second)
	defer cancel()
	upstream, err := a.voice.Open(ctx, u.ID+"/"+game.ID())
	if err != nil {
		slog.Warn("Linx voice connection failed", "error", err)
		_ = front.WriteJSON(map[string]string{"type": "error", "message": "语音连接失败，请稍后重试或使用文字发言"})
		return
	}
	defer upstream.Close()
	_ = upstream.WriteJSON(map[string]any{"type": "abort", "session_id": upstream.SessionID})
	if err = upstream.Listen("start"); err != nil {
		return
	}
	if err = front.WriteJSON(map[string]string{"type": "ready"}); err != nil {
		return
	}
	streamErr := make(chan error, 2)
	transcript := make(chan string, 1)
	go func() {
		total := 0
		stopped := false
		for {
			kind, b, err := front.ReadMessage()
			if err != nil {
				streamErr <- err
				return
			}
			if kind == websocket.BinaryMessage {
				if stopped || len(b) > 1280 || len(b)%2 != 0 {
					streamErr <- errors.New("invalid audio frame")
					return
				}
				total += len(b)
				if total > 960000 {
					streamErr <- errors.New("recording too long")
					return
				}
				if err = upstream.WritePCM(b); err != nil {
					streamErr <- err
					return
				}
			} else {
				var msg struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(b, &msg) != nil {
					streamErr <- errors.New("invalid audio control")
					return
				}
				if msg.Type == "cancel" {
					streamErr <- context.Canceled
					return
				}
				if msg.Type == "stop" && !stopped {
					stopped = true
					if total < 640 {
						streamErr <- errors.New("recording too short")
						return
					}
					if err = upstream.Listen("stop"); err != nil {
						streamErr <- err
						return
					}
				}
			}
		}
	}()
	go func() {
		for {
			kind, b, err := upstream.Conn.ReadMessage()
			if err != nil {
				streamErr <- err
				return
			}
			if kind != websocket.TextMessage {
				continue
			}
			var msg linx.Message
			if json.Unmarshal(b, &msg) == nil && msg.Type == "stt" && strings.TrimSpace(msg.Text) != "" {
				transcript <- msg.Text
				return
			}
		}
	}()
	select {
	case text := <-transcript:
		_ = front.WriteJSON(map[string]string{"type": "transcript", "text": text})
	case <-ctx.Done():
		_ = front.WriteJSON(map[string]string{"type": "error", "message": "语音识别超时，请重新录制"})
	case err := <-streamErr:
		if !errors.Is(err, context.Canceled) {
			_ = front.WriteJSON(map[string]string{"type": "error", "message": "没能识别这段语音，请重试或使用文字发言"})
		}
	}
}
func (a *App) audio(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, event := r.PathValue("id"), r.PathValue("event")
	l, pcm, err := a.rooms.Event(id, u.ID, event)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if len(pcm) > 0 {
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(linx.PCMToWAV(pcm))
		return
	}
	if !a.voice.Configured() {
		fail(w, 503, errors.New("语音播报暂不可用"))
		return
	}
	key := id + "/" + event
	// A committed line may arrive over SSE before its same-session Linx TTS.
	// Wait for that cache entry instead of competing for the occupied terminal.
	if err := a.waitForAgentAudio(r.Context(), id, u.ID, key); err != nil {
		if r.Context().Err() == nil {
			fail(w, 404, err)
		}
		return
	}
	a.audioMu.Lock()
	data := a.audioCache[key]
	pending := a.audioPending[key]
	creator := false
	if len(data) == 0 && pending == nil {
		pending = &audioCall{done: make(chan struct{})}
		a.audioPending[key] = pending
		creator = true
	}
	a.audioMu.Unlock()
	if len(data) == 0 {
		if creator {
			text := l.Text
			if l.Kind == "speech" && !l.Audio {
				text = fmt.Sprintf("%d号：%s", l.Seat, text)
			}
			// One listener cancelling playback must not interrupt synthesis for the table.
			go a.synthesizeNarration(key, event, text, pending)
		}
		select {
		case <-r.Context().Done():
			return
		case <-pending.done:
			data, err = pending.data, pending.err
		}
		if err != nil {
			slog.Warn("Linx narration failed", "error", err)
			if errors.Is(err, linx.ErrBusy) {
				w.Header().Set("Retry-After", "1")
				fail(w, 503, errors.New("语音通道繁忙，请稍后重试"))
			} else {
				fail(w, 502, errors.New("这条语音暂时无法播放"))
			}
			return
		}
	}

	if bytes.HasPrefix(data, []byte("RIFF")) {
		w.Header().Set("Content-Type", "audio/wav")
	} else {
		w.Header().Set("Content-Type", "audio/ogg")
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}
func (a *App) waitForAgentAudio(ctx context.Context, id, uid, key string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.audioMu.Lock()
		cached := len(a.audioCache[key]) > 0
		a.audioMu.Unlock()
		if cached {
			return nil
		}
		view, err := a.rooms.View(id, uid)
		if err != nil {
			return err
		}
		if !view.AgentActive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) synthesizeNarration(key, event, text string, pending *audioCall) {
	id, _, _ := strings.Cut(key, "/")
	done := a.rooms.Done(id)
	ctx, cancel := a.roomContext(a.ctx, id, 35*time.Second)
	defer cancel()
	data, err := a.voice.Synthesize(ctx, "narration/"+event, text)
	a.audioMu.Lock()
	defer a.audioMu.Unlock()
	select {
	case <-done:
		err = context.Canceled
	default:
	}
	if err == nil {
		for a.audioBytes+len(data) > 32<<20 && len(a.audioCache) > 0 {
			for k, b := range a.audioCache {
				a.audioBytes -= len(b)
				delete(a.audioCache, k)
				break
			}
		}
		a.audioCache[key] = data
		a.audioBytes += len(data)
	}
	pending.data, pending.err = data, err
	delete(a.audioPending, key)
	close(pending.done)
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		fail(w, 400, errors.New("请求格式不正确或内容过长"))
		return false
	}
	return true
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, v)
}
func fail(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, game.ErrRoomNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, game.ErrNotOwner) || errors.Is(err, game.ErrHostOnly) || errors.Is(err, game.ErrNotMember) {
		status = http.StatusForbidden
	}
	if errors.Is(err, game.ErrPlaybackPending) || errors.Is(err, game.ErrStalePlayback) {
		status = http.StatusConflict
	}
	body := map[string]string{"error": err.Error()}
	if errors.Is(err, game.ErrPasswordRequired) || errors.Is(err, game.ErrInvalidPassword) {
		status = http.StatusForbidden
		body["code"] = "room_password_required"
		if errors.Is(err, game.ErrInvalidPassword) {
			body["code"] = "room_password_invalid"
		}
	}
	if errors.Is(err, game.ErrJoinRateLimit) {
		status = http.StatusTooManyRequests
	}
	reply(w, status, body)
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) roomContext(parent context.Context, id string, limit time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, limit)
	done := a.rooms.Done(id)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
