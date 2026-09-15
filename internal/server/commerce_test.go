package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"musky/backend/internal/database"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestLineTotal(t *testing.T) {
	if value, ok := lineTotal(2950, 12, 3); !ok || value != 106200 {
		t.Fatal("incorrect unit/package/carton calculation")
	}
	for _, v := range [][3]int64{{-1, 1, 1}, {1, 0, 1}, {1, 1, 0}, {1000000000000, 1000000, 1000000000}} {
		if _, ok := lineTotal(v[0], v[1], v[2]); ok {
			t.Fatal("accepted invalid amount", v)
		}
	}
}
func TestMySQLCommerce(t *testing.T) {
	dsn := os.Getenv("MYSQL_COMMERCE_TEST_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_COMMERCE_TEST_DSN to a fresh dedicated database")
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
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatal("fresh empty test database required", err)
	}
	if err = Bootstrap(ctx, db, "System", "root@commerce.test", "password-12345"); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := New(db)
	raw := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	call := func(method, path, token, body string, status int) map[string]any {
		t.Helper()
		w := raw(method, path, token, body)
		if w.Code != status {
			t.Fatalf("%s %s: %d, expected %d: %s", method, path, w.Code, status, w.Body.String())
		}
		out := map[string]any{}
		if w.Body.Len() > 0 {
			if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	login := func(email string) string {
		return call("POST", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":"password-12345"}`, email), 200)["access_token"].(string)
	}
	root := login("root@commerce.test")
	for _, name := range []string{"a", "b"} {
		call("POST", "/api/v1/tenants", root, fmt.Sprintf(`{"name":%q,"trader":{"name":%q,"email":%q,"password":"password-12345"}}`, name, name, name+"@commerce.test"), 201)
	}
	a, b := login("a@commerce.test"), login("b@commerce.test")
	p := "/api/v1/tenants/1"
	other := "/api/v1/tenants/2"
	client := call("POST", p+"/clients", a, `{"name":"Client A"}`, 201)
	foreign := call("POST", other+"/clients", b, `{"name":"Client B"}`, 201)
	cid := int(client["id"].(float64))
	foreignID := int(foreign["id"].(float64))
	product := call("POST", p+"/products", a, `{"title":"Tea pack","code":"TEA","quantity":10,"pieces_per_unit":12}`, 201)
	pid := int(product["id"].(float64))
	productPath := fmt.Sprintf("%s/products/%d", p, pid)
	call("POST", p+"/products", a, `{"title":"Duplicate","code":"TEA","quantity":1,"pieces_per_unit":1}`, 409)
	foreignProduct := call("POST", other+"/products", b, `{"title":"Other tea","code":"TEA","quantity":1,"pieces_per_unit":1}`, 201)
	fpid := int(foreignProduct["id"].(float64))
	call("GET", productPath, b, "", 404)
	call("GET", fmt.Sprintf("%s/products/%d", other, pid), b, "", 404)
	call("PATCH", fmt.Sprintf("%s/products/%d", other, pid), b, `{"version":1,"quantity":999}`, 404)
	call("DELETE", fmt.Sprintf("%s/products/%d?version=1", other, pid), b, "", 404)
	call("POST", p+"/products", a, `{"title":"Bad","code":"BAD","quantity":-1,"pieces_per_unit":12}`, 400)
	body := func(client, product int, qty int64) string {
		return fmt.Sprintf(`{"client_id":%d,"issue_date":"2026-09-12","items":[{"product_id":%d,"quantity":%d,"units_per_package":12,"unit_price_minor":2950}]}`, client, product, qty)
	}
	call("POST", p+"/invoices", a, body(foreignID, pid, 1), 404)
	call("POST", p+"/invoices", a, body(cid, fpid, 1), 404)
	inv := call("POST", p+"/invoices", a, body(cid, pid, 3), 201)
	iid := int(inv["id"].(float64))
	invoicePath := fmt.Sprintf("%s/invoices/%d", p, iid)
	if inv["total_minor"] != float64(106200) || inv["currency"] != "EGP" {
		t.Fatal("incorrect money", inv)
	}
	if q := call("GET", productPath, a, "", 200)["quantity"]; q != float64(10) {
		t.Fatal("draft changed stock")
	}
	call("GET", invoicePath, b, "", 404)
	call("GET", fmt.Sprintf("%s/invoices/%d", other, iid), b, "", 404)
	call("POST", fmt.Sprintf("%s/invoices/%d/post", other, iid), b, `{"version":1}`, 404)
	call("PUT", fmt.Sprintf("%s/invoices/%d", other, iid), b, strings.TrimSuffix(body(cid, pid, 2), "}")+`,"version":1}`, 404)
	call("PATCH", productPath, a, `{"version":1,"title":"New tea title"}`, 200)
	posted := call("POST", invoicePath+"/post", a, `{"version":1}`, 200)
	if posted["status"] != "posted" || posted["number"] != float64(1) {
		t.Fatal("not posted", posted)
	}
	item := posted["items"].([]any)[0].(map[string]any)
	if item["title"] != "Tea pack" || item["unit_price_minor"] != float64(2950) || item["pieces_per_unit"] != float64(12) {
		t.Fatal("historical snapshot changed")
	}
	call("POST", invoicePath+"/post", a, `{"version":1}`, 200)
	if q := call("GET", productPath, a, "", 200)["quantity"]; q != float64(7) {
		t.Fatal("posting was not idempotent or quantities not packs", q)
	}
	call("PATCH", productPath, a, `{"version":2,"quantity":10}`, 409)
	call("PUT", invoicePath, a, strings.TrimSuffix(body(cid, pid, 1), "}")+`,"version":2}`, 409)
	call("DELETE", invoicePath+"?version=2", a, "", 409)
	summary := call("GET", p+"/financial-summary", a, "", 200)
	if summary["receivables_minor"] != float64(106200) || summary["payables_minor"] != float64(0) || summary["net_minor"] != float64(106200) {
		t.Fatal("wrong ledger summary", summary)
	}
	if sum := call("GET", other+"/financial-summary", b, "", 200)["net_minor"]; sum != float64(0) {
		t.Fatal("ledger leaked")
	}
	// If any line lacks stock, all earlier line effects must roll back.
	empty := call("POST", p+"/products", a, `{"title":"Empty","code":"EMPTY","quantity":0,"pieces_per_unit":2}`, 201)
	emptyID := int(empty["id"].(float64))
	multi := call("POST", p+"/invoices", a, fmt.Sprintf(`{"client_id":%d,"issue_date":"2026-09-12","items":[{"product_id":%d,"quantity":1,"units_per_package":12,"unit_price_minor":2950},{"product_id":%d,"quantity":1,"units_per_package":2,"unit_price_minor":100}]}`, cid, pid, emptyID), 201)
	call("POST", fmt.Sprintf("%s/invoices/%.0f/post", p, multi["id"]), a, `{"version":1}`, 409)
	if q := call("GET", productPath, a, "", 200)["quantity"]; q != float64(7) {
		t.Fatal("failed invoice changed stock", q)
	}
	// Concurrent sales cannot oversell; each valid transition holds the tenant lock.
	x := call("POST", p+"/invoices", a, body(cid, pid, 6), 201)
	y := call("POST", p+"/invoices", a, body(cid, pid, 6), 201)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, v := range []map[string]any{x, y} {
		wg.Add(1)
		go func(v map[string]any) {
			defer wg.Done()
			codes <- raw("POST", fmt.Sprintf("%s/invoices/%.0f/post", p, v["id"]), a, `{"version":1}`).Code
		}(v)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatal("unexpected concurrent status", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent oversell", success, conflict)
	}
	if q := call("GET", productPath, a, "", 200)["quantity"]; q != float64(1) {
		t.Fatal("wrong concurrent stock", q)
	}
	call("POST", invoicePath+"/void", a, `{"version":2,"reason":"Order cancelled"}`, 200)
	call("POST", invoicePath+"/void", a, `{"version":2,"reason":"Order cancelled"}`, 200)
	if q := call("GET", productPath, a, "", 200)["quantity"]; q != float64(4) {
		t.Fatal("void did not restore exactly three packs", q)
	}
	if sum := call("GET", p+"/financial-summary", a, "", 200)["net_minor"]; sum != float64(212400) {
		t.Fatal("void did not reverse debt", sum)
	}
	draft := call("POST", p+"/invoices", a, body(cid, pid, 1), 201)
	draftPath := fmt.Sprintf("%s/invoices/%.0f", p, draft["id"])
	updated := call("PUT", draftPath, a, strings.TrimSuffix(body(cid, pid, 2), "}")+`,"version":1}`, 200)
	if updated["total_minor"] != float64(70800) {
		t.Fatal("draft edit totals")
	}
	call("PUT", draftPath, a, strings.TrimSuffix(body(cid, pid, 2), "}")+`,"version":1}`, 409)
	call("DELETE", draftPath+"?version=2", a, "", 200)
	call("POST", draftPath+"/post", a, `{"version":3}`, 409)
	current := call("GET", productPath, a, "", 200)
	call("PATCH", productPath, a, fmt.Sprintf(`{"version":%.0f,"active":false}`, current["version"]), 200)
	call("POST", p+"/invoices", a, body(cid, pid, 1), 409)
	current = call("GET", productPath, a, "", 200)
	call("PATCH", productPath, a, fmt.Sprintf(`{"version":%.0f,"active":true}`, current["version"]), 200)
	// Composite item foreign keys reject cross-tenant references even through SQL.
	if _, err = db.Exec("INSERT INTO invoice_items(tenant_id,invoice_id,product_id,title,code,pieces_per_unit,quantity,unit_price_minor,total_minor) VALUES (?,?,?,?,?,1,1,1,1)", 1, iid, fpid, "Bad", "Bad"); err == nil {
		t.Fatal("cross-tenant foreign key missing")
	}
}
