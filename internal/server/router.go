package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"musky/backend/internal/database"
	"musky/backend/internal/model"
	"musky/backend/internal/storage"
)

type API struct {
	orm     *gorm.DB
	files   *storage.R2
	limiter *loginLimiter
}

func New(db *sql.DB) *gin.Engine {
	files, _ := storage.NewR2FromEnv()
	orm, err := database.ORM(db)
	if err != nil {
		panic("could not initialize GORM: " + err.Error())
	}
	a := &API{orm: orm, files: files, limiter: newLoginLimiter()}
	r := gin.New()
	r.Use(gin.Recovery())
	_ = r.SetTrustedProxies(nil)
	r.Use(func(c *gin.Context) {
		if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
		} else {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 100*1024*1024)
		}
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	})
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok", "service": "musky-api"}) })
	v := r.Group("/api/v1")
	v.POST("/auth/login", a.login)
	v.Use(a.authenticate)
	v.POST("/auth/logout", a.logout)
	v.GET("/me", a.me)
	v.PUT("/me/password", a.changePassword)
	v.GET("/tenants", onlySuper, a.listTenants)
	v.POST("/tenants", onlySuper, a.createTenant)
	v.PATCH("/tenants/:tenantID", onlySuper, a.updateTenant)
	t := v.Group("/tenants/:tenantID", a.tenantScope)
	t.GET("/users", a.listUsers)
	t.POST("/users", accountManagers, a.createUser)
	t.GET("/users/:id", a.getUser)
	t.PATCH("/users/:id", accountManagers, a.updateUser)
	t.DELETE("/users/:id", accountManagers, a.deactivateUser)
	t.GET("/clients", a.listClients)
	t.POST("/clients", a.createClient)
	t.GET("/clients/:id", a.getClient)
	t.PATCH("/clients/:id", a.updateClient)
	t.DELETE("/clients/:id", a.deleteClient)
	t.POST("/clients/:id/receipts", a.createClientReceipt)
	t.GET("/clients/:id/ledger", a.clientLedger)
	t.GET("/products", a.listProducts)
	t.POST("/products", a.createProduct)
	t.GET("/products/:id/buyers", a.productBuyers)
	t.GET("/products/:id", a.getProduct)
	t.PATCH("/products/:id", a.updateProduct)
	t.DELETE("/products/:id", a.deleteProduct)
	t.GET("/invoices", a.listInvoices)
	t.POST("/invoices", a.createInvoice)
	t.GET("/invoices/:id", a.getInvoice)
	t.PUT("/invoices/:id", a.updateInvoice)
	t.DELETE("/invoices/:id", a.cancelInvoice)
	t.POST("/invoices/:id/post", a.postInvoice)
	t.POST("/invoices/:id/void", a.voidInvoice)
	t.POST("/invoices/:id/payments", a.createPayment)
	t.POST("/invoices/:id/pdf", a.invoicePDF)
	t.GET("/daily-journal", a.dailyJournal)
	t.GET("/financial-summary", a.financialSummary)
	t.POST("/files/upload", a.uploadOne)
	t.POST("/files/uploads", a.uploadMany)
	t.POST("/logo", a.uploadLogo)
	t.DELETE("/logo", a.deleteLogo)
	t.GET("/files", a.listFiles)
	t.DELETE("/files/:id", a.deleteFile)
	return r
}

func fail(c *gin.Context, code int, message string) {
	c.AbortWithStatusJSON(code, gin.H{"error": message})
}
func databaseError(c *gin.Context, err error) {
	var me *mysql.MySQLError
	switch {
	case errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound):
		fail(c, 404, "not found")
	case errors.As(err, &me) && me.Number == 1062:
		fail(c, 409, "record already exists")
	case errors.As(err, &me) && me.Number == 1451:
		fail(c, 409, "cannot delete: record is referenced by other data")
	case errors.As(err, &me) && (me.Number == 1452 || me.Number == 3819):
		fail(c, 400, "invalid record association")
	default:
		log.Printf("database operation failed: %T", err)
		fail(c, 500, "internal server error")
	}
}
func decode(c *gin.Context, target any) bool {
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		fail(c, 415, "Content-Type must be application/json")
		return false
	}
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		fail(c, 400, "invalid JSON body or unknown field")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(c, 400, "expected one JSON object")
		return false
	}
	return true
}
func validText(s string, min, max int) bool {
	n := utf8.RuneCountInString(s)
	return n >= min && n <= max
}
func pathID(c *gin.Context, key string) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param(key), 10, 64)
	if err != nil || id == 0 {
		fail(c, 400, "invalid "+key)
		return 0, false
	}
	return id, true
}
func pagination(c *gin.Context) (int, int, bool) {
	limit, e1 := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, e2 := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if e1 != nil || e2 != nil || limit < 1 || limit > 100 || offset < 0 {
		fail(c, 400, "limit must be 1-100 and offset nonnegative")
		return 0, 0, false
	}
	return limit, offset, true
}
func actor(c *gin.Context) model.User { return c.MustGet("user").(model.User) }
func tenantID(c *gin.Context) uint64  { return c.MustGet("tenant_id").(uint64) }
func onlySuper(c *gin.Context) {
	if actor(c).Role != model.SuperAdmin {
		fail(c, 403, "super_admin required")
	}
}
func accountManagers(c *gin.Context) {
	if actor(c).Role == model.Admin {
		fail(c, 403, "trader owner or super_admin required")
	}
}
func (a *API) tenantScope(c *gin.Context) {
	id, ok := pathID(c, "tenantID")
	if !ok {
		return
	}
	u := actor(c)
	if u.Role != model.SuperAdmin && (u.TenantID == nil || *u.TenantID != id) {
		fail(c, 404, "not found")
		return
	}
	var tenant model.Tenant
	result := a.orm.WithContext(c.Request.Context()).Table("tenants").Select("active").
		Where("id = ?", id).Limit(1).Scan(&tenant)
	if result.Error != nil {
		databaseError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "not found")
		return
	}
	if !tenant.Active {
		fail(c, 403, "tenant is inactive")
		return
	}
	c.Set("tenant_id", id)
}

const userColumns = "id, tenant_id, name, email, role, active, password_hash, created_at"
