package storage

import (
	"context"
	"fmt"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"net/url"
	"os"
	"strings"
)

type R2 struct {
	client            *minio.Client
	bucket, publicURL string
}

func NewR2FromEnv() (*R2, error) {
	endpoint := strings.TrimSpace(os.Getenv("R2_ENDPOINT"))
	key := os.Getenv("R2_ACCESS_KEY_ID")
	secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	bucket := strings.TrimSpace(os.Getenv("R2_BUCKET_NAME"))
	if bucket == "" {
		bucket = os.Getenv("R2_BUCKET")
	}
	if endpoint == "" || key == "" || secret == "" || bucket == "" {
		return nil, fmt.Errorf("R2 storage is not configured")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Path != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid R2_ENDPOINT")
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(key, secret, ""), Secure: u.Scheme == "https"})
	if err != nil {
		return nil, err
	}
	return &R2{client: client, bucket: bucket, publicURL: strings.TrimRight(os.Getenv("R2_PUBLIC_URL"), "/")}, nil
}
func (r *R2) Put(ctx context.Context, key, contentType string, size int64, body io.Reader) error {
	_, err := r.client.PutObject(ctx, r.bucket, key, body, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}
func (r *R2) Delete(ctx context.Context, key string) error {
	return r.client.RemoveObject(ctx, r.bucket, key, minio.RemoveObjectOptions{})
}
func (r *R2) URL(key string) string {
	if r.publicURL == "" {
		return ""
	}
	return r.publicURL + "/" + key
}
