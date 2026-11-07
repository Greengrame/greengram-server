package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OpenBlobs opens the store the environment names, for the server and the
// import tools alike:
//
//	GREENGRAM_BLOBS                 disk (default): <data dir>/blobs
//	                                s3: a bucket, see S3ConfigFromEnv
func OpenBlobs(ctx context.Context, dataDir string) (Blobs, error) {
	switch v := os.Getenv("GREENGRAM_BLOBS"); v {
	case "", "disk":
		return OpenDisk(filepath.Join(dataDir, "blobs"))
	case "s3":
		cfg, err := S3ConfigFromEnv(dataDir)
		if err != nil {
			return nil, err
		}
		return OpenS3(ctx, cfg)
	default:
		return nil, fmt.Errorf("GREENGRAM_BLOBS=%q: want disk or s3", v)
	}
}

// S3ConfigFromEnv reads
//
//	GREENGRAM_S3_ENDPOINT           https://<account id>.r2.cloudflarestorage.com
//	GREENGRAM_S3_BUCKET             the bucket
//	GREENGRAM_S3_REGION             default auto (R2)
//	GREENGRAM_S3_PREFIX             optional, put before every key, e.g. blobs/
//	GREENGRAM_S3_ACCESS_KEY_ID      the key id
//	GREENGRAM_S3_SECRET_FILE        file holding the secret (preferred), or
//	GREENGRAM_S3_SECRET_ACCESS_KEY  the secret itself
//	GREENGRAM_BLOB_CACHE_DIR        default <data dir>/blobcache
//	GREENGRAM_BLOB_CACHE_MB         read cache cap, default 2048; 0 turns it off
func S3ConfigFromEnv(dataDir string) (S3Config, error) {
	c := S3Config{
		Endpoint:  os.Getenv("GREENGRAM_S3_ENDPOINT"),
		Bucket:    os.Getenv("GREENGRAM_S3_BUCKET"),
		Region:    os.Getenv("GREENGRAM_S3_REGION"),
		Prefix:    os.Getenv("GREENGRAM_S3_PREFIX"),
		AccessKey: os.Getenv("GREENGRAM_S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("GREENGRAM_S3_SECRET_ACCESS_KEY"),
		CacheDir:  os.Getenv("GREENGRAM_BLOB_CACHE_DIR"),
	}
	if f := os.Getenv("GREENGRAM_S3_SECRET_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			return c, fmt.Errorf("GREENGRAM_S3_SECRET_FILE: %w", err)
		}
		c.SecretKey = strings.TrimSpace(string(raw))
	}
	if c.CacheDir == "" {
		c.CacheDir = filepath.Join(dataDir, "blobcache")
	}
	mb := int64(2048)
	if v := os.Getenv("GREENGRAM_BLOB_CACHE_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return c, fmt.Errorf("GREENGRAM_BLOB_CACHE_MB=%q: want megabytes", v)
		}
		mb = n
	}
	c.CacheBytes = mb << 20
	for name, v := range map[string]string{"GREENGRAM_S3_ENDPOINT": c.Endpoint, "GREENGRAM_S3_BUCKET": c.Bucket,
		"GREENGRAM_S3_ACCESS_KEY_ID": c.AccessKey, "GREENGRAM_S3_SECRET_FILE": c.SecretKey} {
		if v == "" {
			return c, fmt.Errorf("GREENGRAM_BLOBS=s3 needs %s", name)
		}
	}
	return c, nil
}
