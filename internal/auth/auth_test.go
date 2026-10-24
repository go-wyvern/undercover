package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestSafeReturnToAndFlowIntegrity(t *testing.T) {
	for in, want := range map[string]string{"/": "/", "/room/abc": "/room/abc", "//evil.test": "/", "https://evil.test": "/", "/\\evil.test": "/", "/%5cevil.test": "/", "/%2f%2fevil.test": "/"} {
		if got := SafeReturnTo(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	a, _ := newTestAuth(Config{})
	a.cfg.SessionSecret = strings.Repeat("x", 32)
	f := flow{"nonce", "verifier", "/", time.Now().Add(time.Minute).Unix()}
	signed, err := a.sign(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.verify(signed)
	if err != nil || got != f {
		t.Fatal("flow roundtrip failed")
	}
	if _, err = a.verify(signed + "x"); err == nil {
		t.Fatal("tampered flow accepted")
	}
}
func TestGuestSessionIsOpaquePersistentAndRevocable(t *testing.T) {
	store := testsupport.NewSessions()
	a, err := newTestAuth(Config{AllowGuest: true, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Guest(w, httptest.NewRequest("POST", "/api/auth/guest", strings.NewReader(`{"displayName":"小明"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe session cookie")
	}
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(cookie)
	if currentUser(t, a, req) == nil {
		t.Fatal("missing session")
	}
	b, _ := json.Marshal(store.Records)
	if strings.Contains(string(b), cookie.Value) {
		t.Fatal("raw session stored in session store")
	}
	a2, err := newTestAuth(Config{SessionStore: store})
	if err != nil || currentUser(t, a2, req) == nil {
		t.Fatal("session did not survive restart")
	}
	w = httptest.NewRecorder()
	a2.Logout(w, req)
	if currentUser(t, a2, req) != nil {
		t.Fatal("session survived logout")
	}
}
func TestXBuilderPARPKCEAndCallback(t *testing.T) {
	var state, challenge string
	var tokenExchange bool
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/par":
			id, secret, ok := r.BasicAuth()
			if !ok || id != "client" || secret != "test-secret" {
				t.Error("missing client authentication")
			}
			_ = r.ParseForm()
			state = r.Form.Get("state")
			challenge = r.Form.Get("code_challenge")
			if r.Form.Get("code_challenge_method") != "S256" || r.Form.Get("scope") != "account:user:read" {
				t.Error("missing PKCE or scope")
			}
			io.WriteString(w, `{"request_uri":"urn:par:test"}`)
		case "/oauth/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				t.Error("PKCE challenge mismatch")
			}
			tokenExchange = true
			io.WriteString(w, `{"access_token":"upstream-only-token"}`)
		case "/user":
			if r.Header.Get("Authorization") != "Bearer upstream-only-token" {
				t.Error("missing user bearer token")
			}
			io.WriteString(w, `{"id":"42","username":"alice","displayName":"Alice"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer account.Close()
	a, err := newTestAuth(Config{BaseURL: account.URL, ClientID: "client", ClientSecret: "test-secret", RedirectURL: "http://127.0.0.1/auth/callback", SessionSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Login(w, httptest.NewRequest("GET", "/auth/login?return_to=%2Froom%2Froom-1", nil))
	if w.Code != 302 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	target, _ := url.Parse(w.Header().Get("Location"))
	if target.Query().Get("request_uri") != "urn:par:test" {
		t.Fatal("did not use pushed authorization request")
	}
	cookies := w.Result().Cookies()
	req := httptest.NewRequest("GET", "/auth/callback?code=valid&state="+url.QueryEscape(state), nil)
	req.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	a.Callback(w, req)
	if !tokenExchange || w.Code != 302 || w.Header().Get("Location") != "/room/room-1" {
		t.Fatalf("callback failed: %d %s", w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || strings.Contains(w.Header().Get("Set-Cookie"), "upstream-only-token") {
		t.Fatal("invalid browser session")
	}
	meReq := httptest.NewRequest("GET", "/api/auth/me", nil)
	meReq.AddCookie(session)
	me := httptest.NewRecorder()
	a.Me(me, meReq)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(me.Body.Bytes(), &body)
	if !strings.Contains(string(body["user"]), "Alice") {
		t.Fatal("user not authenticated")
	}
	bad := httptest.NewRequest("GET", "/auth/callback?code=valid&state=wrong", nil)
	bad.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	a.Callback(w, bad)
	if w.Code != 400 {
		t.Fatal("invalid state accepted")
	}
}

func newTestAuth(cfg Config) (*Auth, error) {
	if cfg.SessionStore == nil {
		cfg.SessionStore = testsupport.NewSessions()
	}
	return New(cfg)
}
func currentUser(t *testing.T, a *Auth, r *http.Request) *User {
	t.Helper()
	u, err := a.Current(r)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRedisFailureDoesNotIssueSessionsOrClearCookies(t *testing.T) {
	store := testsupport.NewSessions()
	a, err := New(Config{AllowGuest: true, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Guest(w, httptest.NewRequest("POST", "/api/auth/guest", strings.NewReader(`{"displayName":"小明"}`)))
	cookie := w.Result().Cookies()[0]
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(cookie)
	key := hash(cookie.Value)
	if len(key) != 64 || len(store.Records) != 1 || store.Expires[key].Sub(time.Now()) < 6*24*time.Hour {
		t.Fatal("session key or TTL invalid")
	}
	store.Fail = errors.New("Redis unavailable")
	if u, err := a.Current(req); err == nil || u != nil {
		t.Fatal("Redis outage was treated as an anonymous/valid session")
	}
	w = httptest.NewRecorder()
	a.Me(w, req)
	if w.Code != 503 {
		t.Fatal("Redis outage did not return 503")
	}
	w = httptest.NewRecorder()
	a.Logout(w, req)
	if w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatal("failed revocation cleared the browser cookie")
	}
	w = httptest.NewRecorder()
	a.Guest(w, httptest.NewRequest("POST", "/api/auth/guest", strings.NewReader(`{"displayName":"小红"}`)))
	if w.Code == 200 || len(w.Result().Cookies()) != 0 {
		t.Fatal("session was issued without Redis")
	}
	store.Fail = nil
	store.Expires[key] = time.Now().Add(-time.Second)
	if u, err := a.Current(req); err != nil || u != nil {
		t.Fatal("expired session was accepted")
	}
}
