package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"musky/backend/internal/model"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func validEmail(s string) bool {
	address, err := mail.ParseAddress(s)
	return err == nil && address.Address == s && len(s) <= 254
}
func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", fmt.Errorf("password must be 12-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}
func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// Bootstrap creates the only super admin. Existing accounts are never reset.
func Bootstrap(ctx context.Context, db *sql.DB, name, email, password string) error {
	name, email = strings.TrimSpace(name), normalizeEmail(email)
	if !validText(name, 1, 150) || !validEmail(email) {
		return fmt.Errorf("valid bootstrap name and email required")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO users(name,email,password_hash,role) VALUES (?,?,?,'super_admin')", name, email, hash)
	return err
}

type loginBucket struct {
	count int
	until time.Time
}
type loginLimiter struct {
	sync.Mutex
	buckets map[string]loginBucket
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{buckets: make(map[string]loginBucket)} }
func (l *loginLimiter) allow(ip string) bool {
	l.Lock()
	defer l.Unlock()
	now := time.Now()
	for key, b := range l.buckets {
		if now.After(b.until) {
			delete(l.buckets, key)
		}
	}
	b, exists := l.buckets[ip]
	if !exists {
		if len(l.buckets) >= 10000 {
			return false
		}
		b.until = now.Add(15 * time.Minute)
	}
	if b.count >= 20 {
		return false
	}
	b.count++
	l.buckets[ip] = b
	return true
}

// A valid fixed hash makes unknown-account checks perform bcrypt work too.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func (a *API) login(c *gin.Context) {
	if !a.limiter.allow(c.ClientIP()) {
		c.Header("Retry-After", "900")
		fail(c, 429, "too many login attempts; try again later")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(c, &in) {
		return
	}
	email := normalizeEmail(in.Email)
	if !validEmail(email) || len(in.Password) > 72 || in.Password == "" {
		fail(c, 401, "invalid email or password")
		return
	}
	u, err := scanUser(a.db.QueryRowContext(c.Request.Context(), "SELECT "+userColumns+" FROM users WHERE email = ?", email))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		databaseError(c, err)
		return
	}
	hash := u.PasswordHash
	if errors.Is(err, sql.ErrNoRows) {
		hash = dummyHash
	}
	passwordErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password))
	if err != nil || passwordErr != nil || !u.Active {
		fail(c, 401, "invalid email or password")
		return
	}
	if u.TenantID != nil {
		var active bool
		if err = a.db.QueryRowContext(c.Request.Context(), "SELECT active,logo_url FROM tenants WHERE id = ?", *u.TenantID).Scan(&active, &u.LogoURL); err != nil {
			databaseError(c, err)
			return
		}
		if !active {
			fail(c, 401, "invalid email or password")
			return
		}
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		fail(c, 500, "internal server error")
		return
	}
	token := hex.EncodeToString(raw)
	expiry := time.Now().UTC().Add(24 * time.Hour)
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c.Request.Context(), "DELETE FROM sessions WHERE user_id = ? AND expires_at <= UTC_TIMESTAMP(6)", u.ID); err != nil {
		databaseError(c, err)
		return
	}
	if _, err = tx.ExecContext(c.Request.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES (?,?,?)", tokenHash(token), u.ID, expiry); err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"access_token": token, "token_type": "Bearer", "expires_at": expiry, "user": u})
}

func (a *API) me(c *gin.Context) {
	u := actor(c)
	a.populateLogo(c, &u)
	c.JSON(200, u)
}

func (a *API) populateLogo(c *gin.Context, u *model.User) {
	if u.TenantID == nil {
		return
	}
	if err := a.db.QueryRowContext(c.Request.Context(), "SELECT logo_url FROM tenants WHERE id = ?", *u.TenantID).Scan(&u.LogoURL); err != nil && !errors.Is(err, sql.ErrNoRows) {
		databaseError(c, err)
	}
}
func (a *API) authenticate(c *gin.Context) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) != 64 {
		fail(c, 401, "valid bearer token required")
		return
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		fail(c, 401, "valid bearer token required")
		return
	}
	u, err := scanUser(a.db.QueryRowContext(c.Request.Context(), `SELECT u.id,u.tenant_id,u.name,u.email,u.role,u.active,u.password_hash,u.created_at
 FROM sessions s JOIN users u ON u.id=s.user_id LEFT JOIN tenants t ON t.id=u.tenant_id
 WHERE s.token_hash=? AND s.expires_at>UTC_TIMESTAMP(6) AND u.active=TRUE AND (u.tenant_id IS NULL OR t.active=TRUE)`, tokenHash(parts[1])))
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 401, "session expired or invalid")
		return
	}
	if err != nil {
		databaseError(c, err)
		return
	}
	a.populateLogo(c, &u)
	c.Set("user", u)
	c.Set("token_hash", tokenHash(parts[1]))
}
func (a *API) logout(c *gin.Context) {
	if _, err := a.db.ExecContext(c.Request.Context(), "DELETE FROM sessions WHERE token_hash=?", c.GetString("token_hash")); err != nil {
		databaseError(c, err)
		return
	}
	c.Status(204)
}
func (a *API) changePassword(c *gin.Context) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(c, &in) {
		return
	}
	u := actor(c)
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		fail(c, 403, "current password is incorrect")
		return
	}
	hash, err := HashPassword(in.New)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	tx, err := a.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		databaseError(c, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(c.Request.Context(), "UPDATE users SET password_hash=? WHERE id=? AND password_hash=?", hash, u.ID, u.PasswordHash)
	if err != nil {
		databaseError(c, err)
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		fail(c, 409, "password changed; log in again")
		return
	}
	if _, err = tx.ExecContext(c.Request.Context(), "DELETE FROM sessions WHERE user_id=?", u.ID); err != nil {
		databaseError(c, err)
		return
	}
	if err = tx.Commit(); err != nil {
		databaseError(c, err)
		return
	}
	c.Status(204)
}
