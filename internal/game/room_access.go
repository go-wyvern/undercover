package game

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotMember        = errors.New("你已离开房间或被房主移除，请重新加入")
	ErrHostOnly         = errors.New("只有房主可以管理房间")
	ErrPasswordRequired = errors.New("请输入房间口令")
	ErrInvalidPassword  = errors.New("房间口令不正确")
	ErrJoinRateLimit    = errors.New("口令尝试过于频繁，请一分钟后重试")
)

const passwordIterations = 600000

type CreateOptions struct {
	Password   string
	AutoFillAI *bool
}

type joinAttempt struct {
	Count   int
	Expires time.Time
}

func validPassword(password string) bool {
	n := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && n >= 4 && n <= 32 &&
		strings.TrimSpace(password) == password && !strings.ContainsFunc(password, unicode.IsControl)
}

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	if !validPassword(password) {
		return "", errors.New("口令需要 4 至 32 字，首尾不能有空格")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), nil
}

func checkPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 3 || parts[0] != "pbkdf2-sha256" || !validPassword(password) {
		return false
	}
	salt, e1 := hex.DecodeString(parts[1])
	want, e2 := hex.DecodeString(parts[2])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return err == nil && subtle.ConstantTimeCompare(key, want) == 1
}

// Password derivation runs outside the room lock so a join cannot stall turns.
// The caller rechecks the credential snapshot before actually taking a seat.
func (m *Manager) authorizeJoin(id, uid, password string) (string, error) {
	m.mu.Lock()
	r := m.rooms[id]
	if r == nil {
		m.mu.Unlock()
		return "", ErrRoomNotFound
	}
	hash := r.PasswordHash
	if member(r, uid) != nil {
		m.mu.Unlock()
		// A membership-based reconnect may not become a new admission if that
		// membership is revoked before JoinWithPassword reacquires the lock.
		return "", nil
	}
	if hash == "" {
		m.mu.Unlock()
		return "", nil
	}
	if password == "" {
		m.mu.Unlock()
		return "", ErrPasswordRequired
	}
	now := time.Now()
	for key, attempt := range m.joinAttempts {
		if !now.Before(attempt.Expires) {
			delete(m.joinAttempts, key)
		}
	}
	key := id + "/" + uid
	attempt := m.joinAttempts[key]
	if attempt.Count >= 5 || (attempt.Count == 0 && len(m.joinAttempts) >= 4096) {
		m.mu.Unlock()
		return "", ErrJoinRateLimit
	}
	if attempt.Count == 0 {
		attempt.Expires = now.Add(time.Minute)
	}
	attempt.Count++
	m.joinAttempts[key] = attempt
	m.mu.Unlock()
	if !checkPassword(hash, password) {
		return "", ErrInvalidPassword
	}
	return hash, nil
}
