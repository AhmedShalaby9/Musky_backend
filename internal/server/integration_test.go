package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"musky/backend/internal/database"
)

// MYSQL_TEST_DSN names a dedicated test database; tables are never dropped.
// The suite requires an empty database and leaves its test data for inspection.
func TestMySQLTenantIsolation(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_TEST_DSN to an empty dedicated MySQL test database")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal("migration is not repeatable:", err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("MYSQL_TEST_DSN must point at a fresh, empty test database")
	}
	password := "test-password-12345"
	if err = Bootstrap(ctx, db, "System owner", "root@musky.test", password); err != nil {
		t.Fatal(err)
	}
	if err = Bootstrap(ctx, db, "Other owner", "other@musky.test", password); err == nil {
		t.Fatal("second super_admin accepted")
	}
	gin.SetMode(gin.TestMode)
	r := New(db)
	call := func(method, path, token, body string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d; body=%s", method, path, w.Code, status, w.Body.String())
		}
		data := map[string]any{}
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(w.Body.String(), "password_hash") || strings.Contains(w.Body.String(), "$2a$") {
			t.Fatal("password leaked")
		}
		return data
	}
	login := func(email string) string {
		t.Helper()
		data := call("POST", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), 200)
		return data["access_token"].(string)
	}
	call("GET", "/api/v1/me", "", "", 401)
	call("POST", "/api/v1/auth/login", "", `{"email":"root@musky.test","password":"wrong"}`, 401)
	root := login("root@musky.test")
	call("GET", "/api/v1/me", root, "", 200)
	tenant1 := call("POST", "/api/v1/tenants", root, fmt.Sprintf(`{"name":"Business A","trader":{"name":"Trader A","email":"a@musky.test","password":%q}}`, password), 201)
	tenant2 := call("POST", "/api/v1/tenants", root, fmt.Sprintf(`{"name":"Business B","trader":{"name":"Trader B","email":"b@musky.test","password":%q}}`, password), 201)
	tid1 := uint64(tenant1["id"].(float64))
	tid2 := uint64(tenant2["id"].(float64))
	owner1 := uint64(tenant1["trader_id"].(float64))
	owner2 := uint64(tenant2["trader_id"].(float64))
	p1 := fmt.Sprintf("/api/v1/tenants/%d", tid1)
	p2 := fmt.Sprintf("/api/v1/tenants/%d", tid2)
	a := login("a@musky.test")
	b := login("b@musky.test")
	call("POST", "/api/v1/tenants", a, `{}`, 403)
	call("GET", p2+"/users", a, "", 404)
	call("POST", p1+"/users", a, fmt.Sprintf(`{"name":"Evil","email":"evil@musky.test","role":"super_admin","password":%q}`, password), 400)
	call("POST", p1+"/users", a, fmt.Sprintf(`{"name":"Second trader","email":"extra@musky.test","role":"trader","password":%q}`, password), 400)
	trader := call("POST", p1+"/users", a, fmt.Sprintf(`{"name":"Staff Admin","email":"staff@musky.test","role":"admin","password":%q}`, password), 201)
	staffID := uint64(trader["id"].(float64))
	staffPath := fmt.Sprintf("%s/users/%d", p1, staffID)
	tr := login("staff@musky.test")
	call("GET", p1+"/users", tr, "", 200)
	call("POST", p1+"/users", tr, `{}`, 403)
	call("PATCH", staffPath, a, `{"role":"trader"}`, 409)
	call("POST", p1+"/clients", tr, fmt.Sprintf(`{"name":"Invalid owner","user_id":%d}`, owner2), 404)
	call("POST", p1+"/clients", a, fmt.Sprintf(`{"name":"Invalid owner","user_id":%d}`, owner2), 404)
	call("POST", p1+"/clients", tr, fmt.Sprintf(`{"name":"Injected tenant","tenant_id":%d}`, tid2), 400)
	client := call("POST", p1+"/clients", tr, `{"name":"Client One","phone":"01000000000"}`, 201)
	clientID := uint64(client["id"].(float64))
	clientPath := fmt.Sprintf("%s/clients/%d", p1, clientID)
	if uint64(client["user_id"].(float64)) != staffID {
		t.Fatal("client not associated with creator")
	}
	call("GET", clientPath, b, "", 404)
	call("GET", fmt.Sprintf("%s/clients/%d", p2, clientID), b, "", 404)
	call("PATCH", fmt.Sprintf("%s/clients/%d", p2, clientID), b, `{"name":"stolen"}`, 404)
	call("DELETE", fmt.Sprintf("%s/clients/%d", p2, clientID), b, "", 404)
	list := call("GET", p2+"/clients", b, "", 200)
	if len(list["data"].([]any)) != 0 {
		t.Fatal("cross-tenant list leak")
	}
	call("PATCH", clientPath, tr, `{"address":"123 Street"}`, 200)
	call("DELETE", clientPath, tr, "", 204)
	archived := call("GET", clientPath, a, "", 200)
	if archived["active"] != false { t.Fatal("client was not archived", archived) }
	call("DELETE", clientPath, a, "", 404)
	call("GET", p1+"/clients?limit=0", a, "", 400)
	call("GET", p1+"/users/abc", a, "", 400)
	call("GET", fmt.Sprintf("%s/users/%d", p1, owner2), a, "", 404)
	call("DELETE", fmt.Sprintf("%s/users/%d", p1, owner1), root, "", 409)
	call("PATCH", fmt.Sprintf("%s/users/%d", p1, owner1), root, `{"role":"admin"}`, 409)
	// Tenant + trader owner must roll back together when email conflicts.
	call("POST", "/api/v1/tenants", root, fmt.Sprintf(`{"name":"Rolled back","trader":{"name":"Duplicate","email":"a@musky.test","password":%q}}`, password), 409)
	if err = db.QueryRow("SELECT COUNT(*) FROM tenants").Scan(&count); err != nil || count != 2 {
		t.Fatal("tenant creation did not roll back", err, count)
	}
	// Database constraints enforce tenant association even if an API check is bypassed.
	_, err = db.Exec("INSERT INTO clients(tenant_id,user_id,name) VALUES (?,?,?)", tid1, owner2, "Invalid")
	var constraint *mysql.MySQLError
	if !errors.As(err, &constraint) || constraint.Number != 1452 {
		t.Fatal("expected cross-tenant foreign-key rejection", err)
	}
	_, err = db.Exec("INSERT INTO users(tenant_id,name,email,password_hash,role) VALUES (?,?,?,?,?)", tid1, "Second trader", "second@musky.test", "hash", "trader")
	if !errors.As(err, &constraint) || constraint.Number != 1062 {
		t.Fatal("database must reject two traders in the same tenant", err)
	}
	_, err = db.Exec("INSERT INTO users(tenant_id,name,email,password_hash,role) VALUES (?,?,?,?,?)", tid1, "Invalid", "bad@musky.test", "hash", "super_admin")
	if err == nil {
		t.Fatal("database accepted scoped super_admin")
	}
	// Raw bearer tokens are never persisted.
	var stored string
	if err = db.QueryRow("SELECT token_hash FROM sessions WHERE user_id=?", staffID).Scan(&stored); err != nil || stored == tr {
		t.Fatal("unsafe token storage", err)
	}
	call("DELETE", staffPath, a, "", 204)
	call("GET", "/api/v1/me", tr, "", 401)
	call("POST", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":"staff@musky.test","password":%q}`, password), 401)
	call("PATCH", staffPath, a, `{"active":true}`, 200)
	tr = login("staff@musky.test")
	call("PUT", "/api/v1/me/password", tr, fmt.Sprintf(`{"current_password":%q,"new_password":"new-password-12345"}`, password), 204)
	call("GET", "/api/v1/me", tr, "", 401)
	call("POST", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":"staff@musky.test","password":%q}`, password), 401)
	newLogin := call("POST", "/api/v1/auth/login", "", `{"email":"staff@musky.test","password":"new-password-12345"}`, 200)
	tr = newLogin["access_token"].(string)
	call("POST", "/api/v1/auth/logout", tr, "", 204)
	call("GET", "/api/v1/me", tr, "", 401)
	call("PATCH", p2, root, `{"active":false}`, 200)
	call("GET", "/api/v1/me", b, "", 401)
	call("PATCH", p2, root, `{"active":true}`, 200)
	call("GET", "/api/v1/me", b, "", 401)
	b = login("b@musky.test")
	if _, err = db.Exec("UPDATE sessions SET expires_at=? WHERE token_hash=?", time.Now().UTC().Add(-time.Hour), tokenHash(b)); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/v1/me", b, "", 401)
	// A request with unsupported JSON fields must not silently assign roles/tenants.
	call("PATCH", staffPath, a, `{"tenant_id":999}`, 400)
}
