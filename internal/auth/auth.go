// Package auth implements XBuilder Account PAR + PKCE and opaque, server-side sessions.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-wyvern/undercover/internal/storage"
)

const sessionCookie = "undercover_session"
const flowCookie = "undercover_oauth_flow"

type Config struct {
	BaseURL, ClientID, ClientSecret, RedirectURL, SessionSecret string
	SessionStore                                                storage.SessionStore
	AllowGuest                                                  bool
}

type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Avatar      string `json:"avatar"`
	Guest       bool   `json:"guest"`
}

type session struct {
	User      User      `json:"user"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type flow struct {
	State, Verifier, ReturnTo string
	ExpiresAt                 int64
}

type Auth struct {
	cfg    Config
	client *http.Client
	secure bool
}

func New(cfg Config) (*Auth, error) {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.ClientID != "" {
		if cfg.BaseURL == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" || len(cfg.SessionSecret) < 32 {
			return nil, errors.New("XBuilder Account requires base URL, client secret, redirect URL and a 32+ character session secret")
		}
		for _, raw := range []string{cfg.BaseURL, cfg.RedirectURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
				return nil, errors.New("account URLs require HTTPS (HTTP is allowed for localhost)")
			}
		}
	}
	if cfg.SessionStore == nil {
		return nil, errors.New("session store is required")
	}
	return &Auth{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, secure: strings.HasPrefix(cfg.RedirectURL, "https://")}, nil
}
func (a *Auth) Enabled() bool    { return a.cfg.ClientID != "" }
func (a *Auth) AllowGuest() bool { return a.cfg.AllowGuest }
func (a *Auth) Current(r *http.Request) (*User, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	raw, err := a.cfg.SessionStore.Get(ctx, hash(cookie.Value))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("登录服务暂时不可用，请稍后重试")
	}
	var s session
	if json.Unmarshal(raw, &s) != nil {
		return nil, errors.New("登录会话无效，请重新登录")
	}
	if !s.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	return &s.User, nil
}
func (a *Auth) Me(w http.ResponseWriter, r *http.Request) {
	u, err := a.Current(r)
	if err != nil {
		reply(w, 503, map[string]string{"error": err.Error()})
		return
	}
	reply(w, 200, map[string]any{"enabled": a.Enabled(), "allowGuest": a.AllowGuest(), "user": u})
}

func (a *Auth) Guest(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.AllowGuest {
		reply(w, 403, map[string]string{"error": "请使用账号登录"})
		return
	}
	var in struct {
		DisplayName string `json:"displayName"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&in) != nil {
		reply(w, 400, map[string]string{"error": "昵称格式不正确"})
		return
	}
	name := strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 20 || strings.ContainsFunc(name, unicode.IsControl) {
		reply(w, 400, map[string]string{"error": "请输入 1 至 20 字的昵称"})
		return
	}
	id, err := randomToken(18)
	if err != nil {
		reply(w, 500, map[string]string{"error": "创建会话失败"})
		return
	}
	u := User{ID: "guest_" + id, Username: "guest", DisplayName: name, Guest: true}
	old, err := a.Current(r)
	if err != nil {
		reply(w, 503, map[string]string{"error": err.Error()})
		return
	}
	if old != nil && old.Guest {
		u.ID = old.ID
	}
	if err := a.createSession(r.Context(), w, u); err != nil {
		reply(w, 500, map[string]string{"error": "保存会话失败"})
		return
	}
	reply(w, 200, map[string]any{"user": u})
}

const SessionLifetime = 7 * 24 * time.Hour

func (a *Auth) createSession(parent context.Context, w http.ResponseWriter, u User) error {
	raw, err := randomToken(48)
	if err != nil {
		return err
	}
	value, err := json.Marshal(session{u, time.Now().Add(SessionLifetime)})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if err = a.cfg.SessionStore.Set(ctx, hash(raw), value, SessionLifetime); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/", MaxAge: int(SessionLifetime.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
	return nil
}
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err = a.cfg.SessionStore.Delete(ctx, hash(cookie.Value)); err != nil {
			reply(w, 503, map[string]string{"error": "退出失败，请重试"})
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
	reply(w, 200, map[string]bool{"ok": true})
}

func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled() {
		http.Error(w, "账号登录尚未配置", 503)
		return
	}
	state, err := randomToken(32)
	if err != nil {
		http.Error(w, "创建登录状态失败", 500)
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		http.Error(w, "创建登录凭证失败", 500)
		return
	}
	f := flow{state, verifier, SafeReturnTo(r.URL.Query().Get("return_to")), time.Now().Add(10 * time.Minute).Unix()}
	signed, err := a.sign(f)
	if err != nil {
		http.Error(w, "创建登录状态失败", 500)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	form := url.Values{"response_type": {"code"}, "client_id": {a.cfg.ClientID}, "redirect_uri": {a.cfg.RedirectURL}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "scope": {"account:user:read"}, "ui_locales": {"zh-CN zh en"}}
	var par struct {
		RequestURI string `json:"request_uri"`
	}
	if err := a.accountForm(r.Context(), "/oauth/par", form, &par); err != nil || par.RequestURI == "" {
		http.Error(w, "账号服务暂时不可用", 502)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: signed, Path: "/auth", MaxAge: 600, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, a.cfg.BaseURL+"/oauth/authorize?client_id="+url.QueryEscape(a.cfg.ClientID)+"&request_uri="+url.QueryEscape(par.RequestURI), http.StatusFound)
}

func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled() {
		http.Error(w, "账号登录尚未配置", 503)
		return
	}
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "登录已取消，请重试", 401)
		return
	}
	c, err := r.Cookie(flowCookie)
	if err != nil {
		http.Error(w, "登录流程已过期，请重新登录", 400)
		return
	}
	f, err := a.verify(c.Value)
	if err != nil || f.ExpiresAt < time.Now().Unix() || !hmac.Equal([]byte(f.State), []byte(r.URL.Query().Get("state"))) {
		http.Error(w, "登录状态校验失败", 400)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "缺少授权码", 400)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Path: "/auth", MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
	var token struct {
		AccessToken string `json:"access_token"`
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {a.cfg.RedirectURL}, "code_verifier": {f.Verifier}}
	if err := a.accountForm(r.Context(), "/oauth/token", form, &token); err != nil || token.AccessToken == "" {
		http.Error(w, "账号授权失败，请重试", 502)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.cfg.BaseURL+"/user", nil)
	if err != nil {
		http.Error(w, "账号配置错误", 500)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	res, err := a.client.Do(req)
	if err != nil {
		http.Error(w, "获取账号信息失败", 502)
		return
	}
	defer res.Body.Close()
	var u User
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&u) != nil || u.ID == "" || u.Username == "" {
		http.Error(w, "账号信息无效", 502)
		return
	}
	u.Guest = false
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	if err := a.createSession(r.Context(), w, u); err != nil {
		http.Error(w, "保存登录会话失败", 500)
		return
	}
	http.Redirect(w, r, f.ReturnTo, http.StatusFound)
}

func (a *Auth) accountForm(ctx context.Context, path string, form url.Values, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(a.cfg.ClientID, a.cfg.ClientSecret)
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("account status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst)
}
func (a *Auth) sign(f flow) (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	mac.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (a *Auth) verify(s string) (flow, error) {
	p := strings.Split(s, ".")
	if len(p) != 2 {
		return flow{}, errors.New("invalid flow")
	}
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	mac.Write([]byte(p[0]))
	sig, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return flow{}, errors.New("invalid signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(p[0])
	if err != nil {
		return flow{}, err
	}
	var f flow
	err = json.Unmarshal(b, &f)
	return f, err
}
func SafeReturnTo(s string) string {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n") {
		return "/"
	}
	return s
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func hash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
