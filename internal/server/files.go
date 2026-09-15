package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"musky/backend/internal/storage"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const maxUpload int64 = 50 * 1024 * 1024

var allowedUploadTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "application/pdf": true}

func uploadKey(tenant uint64, name string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	ext := strings.ToLower(filepath.Ext(name))
	return fmt.Sprintf("tenants/%d/files/%s%s", tenant, hex.EncodeToString(b), ext)
}
func (a *API) uploadStore() *storage.R2  { return a.files }
func (a *API) uploadOne(c *gin.Context)  { a.upload(c, false) }
func (a *API) uploadMany(c *gin.Context) { a.upload(c, true) }

func (a *API) uploadLogo(c *gin.Context) {
	if a.files == nil {
		fail(c, 503, "file storage is not configured")
		return
	}
	if err := c.Request.ParseMultipartForm(maxUpload); err != nil {
		fail(c, 400, "invalid multipart upload or request is too large")
		return
	}
	files := c.Request.MultipartForm.File["file"]
	if len(files) != 1 {
		fail(c, 400, "exactly one file is required")
		return
	}
	h := files[0]
	contentType := h.Header.Get("Content-Type")
	if h.Size < 1 || h.Size > maxUpload {
		fail(c, 400, "logo must be between 1 byte and 50 MB")
		return
	}
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/webp" {
		fail(c, 415, "logo must be JPEG, PNG or WebP")
		return
	}
	file, err := h.Open()
	if err != nil {
		fail(c, 400, "cannot read uploaded file")
		return
	}
	key := uploadKey(tenantID(c), h.Filename)
	err = a.files.Put(c.Request.Context(), key, contentType, h.Size, file)
	_ = file.Close()
	if err != nil {
		fail(c, 502, "file storage upload failed")
		return
	}
	publicURL := a.files.URL(key)
	fileRow := fileRecord{TenantID: tenantID(c), UploadedByUserID: actor(c).ID, ObjectKey: key, OriginalName: h.Filename, ContentType: contentType, SizeBytes: h.Size, PublicURL: publicURL}
	err = a.orm.WithContext(c.Request.Context()).Create(&fileRow).Error
	if err != nil {
		_ = a.files.Delete(c.Request.Context(), key)
		databaseError(c, err)
		return
	}
	fileID := fileRow.ID
	var oldKey string
	var old struct{ ObjectKey string }
	_ = a.orm.WithContext(c.Request.Context()).Table("tenants t").Select("COALESCE(f.object_key,'') AS object_key").Joins("LEFT JOIN file_objects f ON f.id=t.logo_file_id AND f.tenant_id=t.id").Where("t.id = ?", tenantID(c)).Scan(&old).Error
	oldKey = old.ObjectKey
	if err = a.orm.WithContext(c.Request.Context()).Model(&tenantRecord{}).Where("id = ?", tenantID(c)).Updates(map[string]any{"logo_file_id": fileID, "logo_url": publicURL}).Error; err != nil {
		_ = a.files.Delete(c.Request.Context(), key)
		_ = a.orm.WithContext(c.Request.Context()).Where("tenant_id = ? AND id = ?", tenantID(c), fileID).Delete(&fileRecord{}).Error
		databaseError(c, err)
		return
	}
	if oldKey != "" {
		_ = a.files.Delete(c.Request.Context(), oldKey)
		_ = a.orm.WithContext(c.Request.Context()).Where("tenant_id = ? AND object_key = ?", tenantID(c), oldKey).Delete(&fileRecord{}).Error
	}
	c.JSON(http.StatusCreated, gin.H{"url": publicURL, "file_id": fileID})
}

func (a *API) deleteLogo(c *gin.Context) {
	if a.files == nil {
		fail(c, 503, "file storage is not configured")
		return
	}
	var fileID uint64
	var key string
	var logo struct {
		FileID    uint64
		ObjectKey string
	}
	if err := a.orm.WithContext(c.Request.Context()).Table("tenants t").Select("COALESCE(t.logo_file_id,0) AS file_id,COALESCE(f.object_key,'') AS object_key").Joins("LEFT JOIN file_objects f ON f.id=t.logo_file_id AND f.tenant_id=t.id").Where("t.id = ?", tenantID(c)).Scan(&logo).Error; err != nil {
		databaseError(c, err)
		return
	}
	fileID, key = logo.FileID, logo.ObjectKey
	if fileID == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	if key != "" {
		if err := a.files.Delete(c.Request.Context(), key); err != nil {
			fail(c, 502, "file storage deletion failed")
			return
		}
	}
	if err := a.orm.WithContext(c.Request.Context()).Model(&tenantRecord{}).Where("id = ?", tenantID(c)).Updates(map[string]any{"logo_file_id": nil, "logo_url": ""}).Error; err != nil {
		databaseError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (a *API) upload(c *gin.Context, multiple bool) {
	if a.files == nil {
		fail(c, 503, "file storage is not configured")
		return
	}
	if err := c.Request.ParseMultipartForm(maxUpload); err != nil {
		fail(c, 400, "invalid multipart upload or request is too large")
		return
	}
	headers := c.Request.MultipartForm.File["files"]
	if !multiple {
		headers = c.Request.MultipartForm.File["file"]
	}
	if len(headers) == 0 {
		message := "file is required"
		if multiple {
			message = "at least one file is required"
		}
		fail(c, 400, message)
		return
	}
	if multiple && len(headers) > 20 {
		fail(c, 400, "maximum 20 files per request")
		return
	}
	type result struct {
		ID          uint64 `json:"id"`
		Name        string `json:"name"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size_bytes"`
		URL         string `json:"url"`
		Key         string `json:"-"`
	}
	uploaded := []result{}
	cleanup := func() {
		for _, v := range uploaded {
			_ = a.files.Delete(c.Request.Context(), v.Key)
		}
	}
	for _, header := range headers {
		if header.Size < 1 || header.Size > maxUpload {
			cleanup()
			fail(c, 400, "each file must be between 1 byte and 50 MB")
			return
		}
		if !allowedUploadTypes[header.Header.Get("Content-Type")] {
			cleanup()
			fail(c, 415, "allowed file types are JPEG, PNG, WebP and PDF")
			return
		}
		file, err := header.Open()
		if err != nil {
			cleanup()
			fail(c, 400, "cannot read uploaded file")
			return
		}
		key := uploadKey(tenantID(c), header.Filename)
		err = a.files.Put(c.Request.Context(), key, header.Header.Get("Content-Type"), header.Size, file)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			cleanup()
			fail(c, 502, "file storage upload failed")
			return
		}
		url := a.files.URL(key)
		fileRow := fileRecord{TenantID: tenantID(c), UploadedByUserID: actor(c).ID, ObjectKey: key, OriginalName: header.Filename, ContentType: header.Header.Get("Content-Type"), SizeBytes: header.Size, PublicURL: url}
		err = a.orm.WithContext(c.Request.Context()).Create(&fileRow).Error
		if err != nil {
			_ = a.files.Delete(c.Request.Context(), key)
			databaseError(c, err)
			return
		}
		uploaded = append(uploaded, result{ID: fileRow.ID, Name: header.Filename, ContentType: header.Header.Get("Content-Type"), Size: header.Size, URL: url, Key: key})
	}
	if multiple {
		c.JSON(201, gin.H{"files": uploaded})
	} else {
		c.JSON(201, uploaded[0])
	}
}
func (a *API) listFiles(c *gin.Context) {
	limit, offset, ok := pagination(c)
	if !ok {
		return
	}
	type file struct {
		ID          uint64    `json:"id"`
		Name        string    `json:"name"`
		ContentType string    `json:"content_type"`
		Size        int64     `json:"size_bytes"`
		URL         string    `json:"url"`
		CreatedAt   time.Time `json:"created_at"`
	}
	out := []file{}
	if err := a.orm.WithContext(c.Request.Context()).Table("file_objects").Select("id,original_name AS name,content_type,size_bytes AS size,public_url AS url,created_at").Where("tenant_id = ?", tenantID(c)).Order("id DESC").Limit(limit).Offset(offset).Scan(&out).Error; err != nil {
		databaseError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": out, "limit": limit, "offset": offset})
}
func (a *API) deleteFile(c *gin.Context) {
	if a.files == nil {
		fail(c, 503, "file storage is not configured")
		return
	}
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var key string
	var file struct{ ObjectKey string }
	if err := a.orm.WithContext(c.Request.Context()).Table("file_objects").Select("object_key").Where("tenant_id = ? AND id = ?", tenantID(c), id).Take(&file).Error; err != nil {
		databaseError(c, err)
		return
	}
	key = file.ObjectKey
	if err := a.files.Delete(c.Request.Context(), key); err != nil {
		fail(c, 502, "file storage deletion failed")
		return
	}
	if err := a.orm.WithContext(c.Request.Context()).Where("tenant_id = ? AND id = ?", tenantID(c), id).Delete(&fileRecord{}).Error; err != nil {
		databaseError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
