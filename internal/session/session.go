// Package session 实现基于 SQLite sessions 表的服务端会话。
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"work-ledger/internal/db"
	"work-ledger/internal/timeutil"
)

type Session struct {
	ID    string
	Data  map[string]any
	dirty bool
	isNew bool
	mu    sync.Mutex
	old   string // 旧 ID（regenerate 时删除）
	dead  bool
}

func (s *Session) Get(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Data[key]
}

func (s *Session) GetString(key string) string {
	v, _ := s.Get(key).(string)
	return v
}

func (s *Session) GetInt(key string) int64 {
	switch t := s.Get(key).(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	}
	return 0
}

func (s *Session) Set(key string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Data[key] = v
	s.dirty = true
}

func (s *Session) Delete(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if _, ok := s.Data[k]; ok {
			delete(s.Data, k)
			s.dirty = true
		}
	}
}

// Regenerate 更换会话 ID，防止会话固定攻击。
func (s *Session) Regenerate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isNew && s.old == "" {
		s.old = s.ID
	}
	s.ID = newID()
	s.isNew = true
	s.dirty = true
}

// Destroy 清空会话。
func (s *Session) Destroy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Data = map[string]any{}
	s.dead = true
	s.dirty = true
}

type Store struct {
	DB         *db.DB
	CookieName string
	Lifetime   time.Duration
	lastGC     time.Time
	gcMu       sync.Mutex
}

func newID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Load 读取请求中的会话；不存在或过期时创建新会话（尚未写库）。
func (st *Store) Load(r *http.Request) *Session {
	st.gc()
	if c, err := r.Cookie(st.CookieName); err == nil && len(c.Value) == 64 {
		row, err := db.One(st.DB, "SELECT data, expires_at FROM sessions WHERE id = ?", c.Value)
		if err == nil && row != nil && row.Str("expires_at") > timeutil.NowString() {
			data := map[string]any{}
			_ = json.Unmarshal([]byte(row.Str("data")), &data)
			return &Session{ID: c.Value, Data: data}
		}
	}
	return &Session{ID: newID(), Data: map[string]any{}, isNew: true}
}

// Save 持久化会话并下发 Cookie。未修改且非新建的会话只刷新过期时间（每分钟最多一次）。
func (st *Store) Save(w http.ResponseWriter, r *http.Request, s *Session, secure bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.old != "" {
		_, _ = db.Exec(st.DB, "DELETE FROM sessions WHERE id = ?", s.old)
		s.old = ""
	}
	if s.dead {
		_, _ = db.Exec(st.DB, "DELETE FROM sessions WHERE id = ?", s.ID)
		http.SetCookie(w, &http.Cookie{Name: st.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
		return
	}
	if s.isNew && len(s.Data) == 0 {
		return // 空会话不落库
	}

	now := timeutil.Now()
	expires := now.Add(st.Lifetime).Format(timeutil.DateTime)
	if s.dirty || s.isNew {
		raw, _ := json.Marshal(s.Data)
		var userID any
		if v, ok := s.Data["user_id"].(float64); ok {
			userID = int64(v)
		} else if v, ok := s.Data["user_id"].(int64); ok {
			userID = v
		}
		_, _ = db.Exec(st.DB, `INSERT INTO sessions (id, user_id, data, created_at, updated_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET user_id = excluded.user_id, data = excluded.data,
			updated_at = excluded.updated_at, expires_at = excluded.expires_at`,
			s.ID, userID, string(raw), now.Format(timeutil.DateTime), now.Format(timeutil.DateTime), expires)
	} else {
		_, _ = db.Exec(st.DB, "UPDATE sessions SET expires_at = ?, updated_at = ? WHERE id = ? AND updated_at < ?",
			expires, now.Format(timeutil.DateTime), s.ID, now.Add(-time.Minute).Format(timeutil.DateTime))
	}
	if s.isNew {
		http.SetCookie(w, &http.Cookie{Name: st.CookieName, Value: s.ID, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	}
	s.isNew = false
	s.dirty = false
}

func (st *Store) gc() {
	st.gcMu.Lock()
	defer st.gcMu.Unlock()
	if time.Since(st.lastGC) < 10*time.Minute {
		return
	}
	st.lastGC = time.Now()
	_, _ = db.Exec(st.DB, "DELETE FROM sessions WHERE expires_at <= ?", timeutil.NowString())
}

// DestroyUser 让某用户的所有会话失效（停用账号、重置密码时调用）。
func DestroyUser(q db.Q, userID int64) error {
	_, err := db.Exec(q, "DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

// DestroyAll 清空全部会话（恢复备份后调用）。
func DestroyAll(q db.Q) error {
	_, err := db.Exec(q, "DELETE FROM sessions")
	return err
}
