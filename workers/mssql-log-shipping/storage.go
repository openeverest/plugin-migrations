package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// keyRoot holds every migration's objects in the bucket.
const keyRoot = "openeverest-migrations"

// Storage is the S3 bucket the backups are staged in. It holds credentials:
// never log it.
type Storage struct {
	Endpoint  *url.URL
	Bucket    string
	Region    string
	PathStyle bool
	AccessKey string
	SecretKey string
	CAFile    string
	// Prefix is this migration's key prefix, without slashes at either end.
	Prefix string

	client *minio.Client
}

func newStorage(endpoint, bucket, region string, pathStyle bool, accessKey, secretKey, caFile, name string) (*Storage, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return nil, errors.New("S3_ENDPOINT must be an https:// URL")
	}
	if parsed.Scheme != "https" {
		return nil, errors.New("S3_ENDPOINT must use https: SQL Server backs up to S3 over HTTPS only")
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return nil, errors.New("S3_ENDPOINT must not have a path")
	}
	if bucket == "" || accessKey == "" || secretKey == "" {
		return nil, errors.New("S3_BUCKET, S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY are required")
	}
	// SQL Server separates the key ID and the secret with a colon.
	if strings.Contains(accessKey, ":") {
		return nil, errors.New("the S3 access key ID must not contain ':'")
	}
	storage := &Storage{
		Endpoint: parsed, Bucket: bucket, Region: region, PathStyle: pathStyle,
		AccessKey: accessKey, SecretKey: secretKey, CAFile: caFile, Prefix: keyRoot + "/" + name,
	}
	transport, err := storage.transport()
	if err != nil {
		return nil, err
	}
	lookup := minio.BucketLookupDNS
	if pathStyle {
		lookup = minio.BucketLookupPath
	}
	storage.client, err = minio.New(parsed.Host, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: true, Region: region,
		BucketLookup: lookup, Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("S3 client: %w", err)
	}
	return storage, nil
}

func (s *Storage) transport() (http.RoundTripper, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if s.CAFile == "" {
		return transport, nil
	}
	pem, err := os.ReadFile(s.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read the S3 CA: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("the S3 CA holds no PEM certificate")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return transport, nil
}

// sqlBase is the bucket's URL as SQL Server's S3 connector addresses it.
func (s *Storage) sqlBase() string {
	if s.PathStyle {
		return "s3://" + s.Endpoint.Host + "/" + s.Bucket
	}
	return "s3://" + s.Bucket + "." + s.Endpoint.Host
}

// CredentialName names the SQL Server credential for this migration. SQL
// Server picks the credential whose name is the longest prefix of a URL, so
// it never collides with a bucket-wide credential of the operator's backups.
func (s *Storage) CredentialName() string { return s.sqlBase() + "/" + s.Prefix }

// URL is the s3:// URL of a key under the migration's prefix.
func (s *Storage) URL(key string) string { return s.CredentialName() + "/" + key }

func (s *Storage) objectName(key string) string { return s.Prefix + "/" + key }

func fullBackupKey(database string, stripe, stripes int) string {
	if stripes <= 1 {
		return database + "/full.bak"
	}
	return fmt.Sprintf("%s/full-%02dof%02d.bak", database, stripe, stripes)
}

func logBackupKey(database string, seq int) string {
	return fmt.Sprintf("%s/log-%08d.trn", database, seq)
}

const probeKey = "probe/probe.txt"

// Exists reports whether a key exists. A backup's object appears only once
// SQL Server completed its multipart upload.
func (s *Storage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.Bucket, s.objectName(key), minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
		return false, nil
	}
	return false, storageError(err)
}

func (s *Storage) Put(ctx context.Context, key string, content []byte) error {
	_, err := s.client.PutObject(ctx, s.Bucket, s.objectName(key), bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: "text/plain"})
	return storageError(err)
}

func (s *Storage) Remove(ctx context.Context, keys ...string) error {
	for _, key := range keys {
		if err := s.client.RemoveObject(ctx, s.Bucket, s.objectName(key), minio.RemoveObjectOptions{}); err != nil &&
			minio.ToErrorResponse(err).Code != minio.NoSuchKey {
			return storageError(err)
		}
	}
	return nil
}

// RemoveAll deletes every object under the migration's prefix.
func (s *Storage) RemoveAll(ctx context.Context) error {
	var listErr error
	objects := make(chan minio.ObjectInfo)
	go func() {
		defer close(objects)
		for object := range s.client.ListObjects(ctx, s.Bucket, minio.ListObjectsOptions{Prefix: s.Prefix + "/", Recursive: true}) {
			if object.Err != nil {
				listErr = object.Err
				return
			}
			objects <- object
		}
	}()
	var removeErr error
	for result := range s.client.RemoveObjects(ctx, s.Bucket, objects, minio.RemoveObjectsOptions{}) {
		if result.Err != nil && minio.ToErrorResponse(result.Err).Code != minio.NoSuchKey && removeErr == nil {
			removeErr = result.Err
		}
	}
	if listErr != nil {
		return storageError(listErr)
	}
	return storageError(removeErr)
}

// storageError keeps S3 errors short; they never contain credentials.
func storageError(err error) error {
	if err == nil {
		return nil
	}
	response := minio.ToErrorResponse(err)
	if response.Code != "" {
		return fmt.Errorf("S3 %s: %s", response.Code, response.Message)
	}
	return fmt.Errorf("S3: %w", err)
}
