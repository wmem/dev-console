package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"devconsole/internal/contracts"
	"devconsole/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash,omitempty"`
	Role         string    `json:"role"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	CSRF      string    `json:"csrf"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Token struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Digest    string     `json:"digest,omitempty"`
	Scopes    []string   `json:"scopes"`
	Enabled   bool       `json:"enabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}
type Principal struct {
	ID, Kind, Role, ProjectID, CSRF string
	Scopes                          []string
}
type attempt struct {
	Count int
	Until time.Time
}
type Auth struct {
	Store    *storage.Store
	mu       sync.Mutex
	attempts map[string]attempt
	dummy    []byte
}

func New(s *storage.Store) *Auth {
	h, _ := bcrypt.GenerateFromPassword([]byte(Random()), bcrypt.DefaultCost)
	return &Auth{Store: s, attempts: map[string]attempt{}, dummy: h}
}
func Random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func ID() string             { return Random()[:24] }
func Digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func Equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func publicUser(u User) User { u.PasswordHash = ""; return u }
func (a *Auth) Users() ([]User, error) {
	us, e := storage.List[User](a.Store, "users")
	for i := range us {
		us[i] = publicUser(us[i])
	}
	return us, e
}
func hashPassword(p string) (string, error) {
	if utf8.RuneCountInString(p) < 10 || len(p) > 72 {
		return "", contracts.Fail("INVALID_ARGUMENT", "密码至少10个字符且不超过72字节")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), e
}
func (a *Auth) Bootstrap(name, password string) (User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	us, e := storage.List[User](a.Store, "users")
	if e != nil {
		return User{}, e
	}
	if len(us) > 0 {
		return User{}, contracts.Fail("CONFIG_CONFLICT", "管理员已初始化")
	}
	return a.create(name, password, "admin")
}
func (a *Auth) create(name, password, role string) (User, error) {
	if name != strings.TrimSpace(name) || len(name) < 2 || len(name) > 64 {
		return User{}, contracts.Fail("INVALID_ARGUMENT", "用户名需为2至64字节且不含首尾空格")
	}
	if role != "admin" && role != "member" {
		return User{}, contracts.Fail("INVALID_ARGUMENT", "角色无效")
	}
	us, e := storage.List[User](a.Store, "users")
	if e != nil {
		return User{}, e
	}
	for _, u := range us {
		if strings.EqualFold(u.Username, name) {
			return User{}, contracts.Fail("CONFIG_CONFLICT", "用户名已存在")
		}
	}
	h, e := hashPassword(password)
	if e != nil {
		return User{}, e
	}
	u := User{ID: ID(), Username: name, PasswordHash: h, Role: role, Enabled: true, CreatedAt: time.Now().UTC()}
	e = a.Store.Put("users", u.ID, u)
	return publicUser(u), e
}
func (a *Auth) Create(name, password, role string) (User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.create(name, password, role)
}
func (a *Auth) Login(name, password, remote string) (string, User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	at := a.attempts[remote]
	if at.Until.After(now) && at.Count >= 8 {
		return "", User{}, contracts.Fail("RATE_LIMITED", "登录尝试过于频繁，请稍后重试")
	}
	if !at.Until.After(now) {
		at = attempt{Until: now.Add(time.Minute)}
	}
	at.Count++
	a.attempts[remote] = at
	if len(a.attempts) > 10000 {
		for k, v := range a.attempts {
			if !v.Until.After(now) {
				delete(a.attempts, k)
			}
		}
	}
	us, e := storage.List[User](a.Store, "users")
	if e != nil {
		return "", User{}, e
	}
	var found *User
	hash := a.dummy
	for _, u := range us {
		if strings.EqualFold(u.Username, name) {
			x := u
			found = &x
			hash = []byte(u.PasswordHash)
			break
		}
	}
	e = bcrypt.CompareHashAndPassword(hash, []byte(password))
	if e != nil || found == nil || !found.Enabled {
		return "", User{}, contracts.Fail("UNAUTHENTICATED", "用户名或密码错误")
	}
	delete(a.attempts, remote)
	raw := Random()
	s := Session{Digest(raw), found.ID, Random(), now.Add(12 * time.Hour)}
	if e = a.Store.Put("sessions", s.ID, s); e != nil {
		return "", User{}, e
	}
	return raw, publicUser(*found), nil
}
func (a *Auth) Cookie(raw string) (Principal, error) {
	var s Session
	if raw == "" || a.Store.Get("sessions", Digest(raw), &s) != nil || !s.ExpiresAt.After(time.Now()) {
		return Principal{}, contracts.Fail("UNAUTHENTICATED", "请先登录")
	}
	var u User
	if a.Store.Get("users", s.UserID, &u) != nil || !u.Enabled {
		return Principal{}, contracts.Fail("UNAUTHENTICATED", "会话已失效")
	}
	return Principal{ID: u.ID, Kind: "user", Role: u.Role, CSRF: s.CSRF}, nil
}
func (a *Auth) Logout(raw string) error { return a.Store.Delete("sessions", Digest(raw)) }
func (a *Auth) invalidate(userID string) []storage.Change {
	ss, _ := storage.List[Session](a.Store, "sessions")
	out := []storage.Change{}
	for _, s := range ss {
		if s.UserID == userID {
			out = append(out, storage.Change{Table: "sessions", ID: s.ID, Delete: true})
		}
	}
	return out
}
func (a *Auth) Password(id, old, password string, admin bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var u User
	if e := a.Store.Get("users", id, &u); e != nil {
		return contracts.Fail("USER_NOT_FOUND", "用户不存在")
	}
	if !admin && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(old)) != nil {
		return contracts.Fail("INVALID_ARGUMENT", "原密码错误")
	}
	h, e := hashPassword(password)
	if e != nil {
		return e
	}
	u.PasswordHash = h
	cs := a.invalidate(id)
	cs = append(cs, storage.Change{Table: "users", ID: id, Value: u})
	return a.Store.Batch(cs...)
}
func (a *Auth) Update(id, role string, enabled bool) (User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var u User
	if e := a.Store.Get("users", id, &u); e != nil {
		return u, contracts.Fail("USER_NOT_FOUND", "用户不存在")
	}
	if role != "admin" && role != "member" {
		return u, contracts.Fail("INVALID_ARGUMENT", "角色无效")
	}
	us, e := storage.List[User](a.Store, "users")
	if e != nil {
		return u, e
	}
	if u.Enabled && u.Role == "admin" && (!enabled || role != "admin") {
		count := 0
		for _, v := range us {
			if v.Enabled && v.Role == "admin" {
				count++
			}
		}
		if count <= 1 {
			return u, contracts.Fail("CONFIG_CONFLICT", "不能禁用或降级最后一个管理员")
		}
	}
	u.Role = role
	u.Enabled = enabled
	cs := a.invalidate(id)
	cs = append(cs, storage.Change{Table: "users", ID: id, Value: u})
	e = a.Store.Batch(cs...)
	return publicUser(u), e
}
func (a *Auth) NewToken(project, name string, scopes []string, expires *time.Time) (Token, string, error) {
	if strings.TrimSpace(name) == "" {
		return Token{}, "", contracts.Fail("INVALID_ARGUMENT", "请输入Token名称")
	}
	if len(scopes) == 0 {
		scopes = []string{"docs:read", "services:read", "services:invoke"}
	}
	for _, s := range scopes {
		if s != "docs:read" && s != "services:read" && s != "services:invoke" {
			return Token{}, "", contracts.Fail("INVALID_ARGUMENT", "Token作用域无效")
		}
	}
	if expires != nil && !expires.After(time.Now()) {
		return Token{}, "", contracts.Fail("INVALID_ARGUMENT", "有效期必须在未来")
	}
	raw := "dc_" + Random()
	t := Token{ID: ID(), ProjectID: project, Name: name, Prefix: raw[:11], Digest: Digest(raw), Scopes: scopes, Enabled: true, ExpiresAt: expires, CreatedAt: time.Now().UTC()}
	e := a.Store.Put("access_tokens", t.ID, t)
	t.Digest = ""
	return t, raw, e
}
func (a *Auth) Bearer(raw string) (Principal, error) {
	ts, e := storage.List[Token](a.Store, "access_tokens")
	if e != nil {
		return Principal{}, e
	}
	d := Digest(raw)
	for _, t := range ts {
		if Equal(d, t.Digest) && t.Enabled && (t.ExpiresAt == nil || t.ExpiresAt.After(time.Now())) {
			return Principal{ID: t.ID, Kind: "token", ProjectID: t.ProjectID, Scopes: t.Scopes}, nil
		}
	}
	return Principal{}, contracts.Fail("UNAUTHENTICATED", "Token无效、已撤销或过期")
}
func (a *Auth) Tokens(project string) ([]Token, error) {
	ts, e := storage.List[Token](a.Store, "access_tokens")
	out := []Token{}
	for _, t := range ts {
		if t.ProjectID == project {
			t.Digest = ""
			out = append(out, t)
		}
	}
	return out, e
}
func (a *Auth) Revoke(project, id string) error {
	var t Token
	e := a.Store.Get("access_tokens", id, &t)
	if errors.Is(e, sql.ErrNoRows) || t.ProjectID != project {
		return contracts.Fail("SERVICE_NOT_FOUND", "Token不存在")
	}
	if e != nil {
		return e
	}
	t.Enabled = false
	return a.Store.Put("access_tokens", id, t)
}
