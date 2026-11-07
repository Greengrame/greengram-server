package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/media/s3test"
)

// testBlobs is the stack's blob store: disk, or with GREENGRAM_TEST_BLOBS=s3
// the bucket in GREENGRAM_TEST_S3_* under a prefix of the test's own and a
// small cache, so eviction happens too.
func testBlobs(t *testing.T) (media.Blobs, error) {
	if os.Getenv("GREENGRAM_TEST_BLOBS") != "s3" {
		return media.OpenDisk(t.TempDir())
	}
	cfg, ok := testS3Config(t)
	if !ok {
		t.Fatal("GREENGRAM_TEST_BLOBS=s3 needs GREENGRAM_TEST_S3_ENDPOINT, _BUCKET, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY")
	}
	cfg.CacheBytes = 8 << 20
	return media.OpenS3(context.Background(), cfg)
}

func testS3Config(t *testing.T) (media.S3Config, bool) {
	cfg := media.S3Config{
		Endpoint:  os.Getenv("GREENGRAM_TEST_S3_ENDPOINT"),
		Bucket:    os.Getenv("GREENGRAM_TEST_S3_BUCKET"),
		AccessKey: os.Getenv("GREENGRAM_TEST_S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("GREENGRAM_TEST_S3_SECRET_ACCESS_KEY"),
		Prefix:    fmt.Sprintf("e2e/%s-%d/", regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(t.Name(), "_"), time.Now().UnixNano()),
		CacheDir:  t.TempDir(),
	}
	return cfg, cfg.Endpoint != ""
}

// TestBlobsCopyVerify is ggblobs on a database: verify finds what's not in
// the bucket, copy puts it there, verify passes.
func TestBlobsCopyVerify(t *testing.T) {
	ctx := context.Background()
	st := db(t)
	cfg, ok := testS3Config(t)
	if !ok {
		fake := s3test.New("gg-e2e")
		t.Cleanup(fake.Close)
		cfg.Endpoint, cfg.Bucket, cfg.AccessKey, cfg.SecretKey = fake.URL, "gg-e2e", "k", "s"
	}
	bucket, err := media.OpenS3(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	disk, err := media.OpenDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	// blobs the way the server stores them: a photo's sizes and a document
	med := media.New(st.Pool, media.Config{Blobs: disk, Parts: t.TempDir(), DC: dc})
	p, err := med.NewPhoto(ctx, 0, testPNG(800, 600))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sizes) == 0 {
		t.Fatal("photo without sizes")
	}
	extra, _, err := disk.Put(ctx, bytes.NewReader([]byte("a file")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, "INSERT INTO files(key, size) VALUES ($1, 6) ON CONFLICT DO NOTHING", extra); err != nil {
		t.Fatal(err)
	}
	var keys int
	st.Pool.QueryRow(ctx, "SELECT count(*) FROM files").Scan(&keys)

	rep, err := media.VerifyBlobs(ctx, st.Pool, bucket, media.VerifyOptions{})
	if err != nil || rep.Keys != keys || rep.Missing != keys || rep.OK != 0 {
		t.Fatalf("verify before copy: %+v %v", rep, err)
	}
	cr, err := media.CopyDisk(ctx, root, bucket, media.CopyOptions{})
	if err != nil || cr.Uploaded != keys || cr.Failed != 0 {
		t.Fatalf("copy: %+v %v", cr, err)
	}
	rep, err = media.VerifyBlobs(ctx, st.Pool, bucket, media.VerifyOptions{Hash: true})
	if err != nil || rep.OK != keys || rep.Missing+rep.WrongSize+rep.BadHash+rep.Failed != 0 {
		t.Fatalf("verify after copy: %+v %v", rep, err)
	}
	// a size the bucket disagrees with
	st.Pool.Exec(ctx, "UPDATE files SET size=7 WHERE key=$1", extra)
	if rep, _ = media.VerifyBlobs(ctx, st.Pool, bucket, media.VerifyOptions{}); rep.WrongSize != 1 {
		t.Fatalf("verify with a wrong size: %+v", rep)
	}
	// and the server reads it all through the bucket
	srv := media.New(st.Pool, media.Config{Blobs: bucket, Parts: t.TempDir(), DC: dc})
	for _, z := range p.Sizes {
		if z.Key == "" {
			continue
		}
		raw, err := srv.PhotoFile(ctx, p.ID, z.W)
		if err != nil || len(raw) == 0 {
			t.Fatalf("photo %d from the bucket: %v", z.W, err)
		}
	}
}
