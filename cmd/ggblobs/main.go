// ggblobs moves an instance's files from the disk store into an S3 bucket
// (Cloudflare R2) and checks the bucket before the server switches to it.
// It takes the server's environment, GREENGRAM_S3_* included (see
// media.S3ConfigFromEnv); GREENGRAM_BLOBS doesn't matter to it.
//
//	ggblobs copy [-from data/blobs] [-j 4] [-quick]
//	    every blob on disk into the bucket. Resumable: what's there with the
//	    right size is kept. Each object is downloaded and hashed afterwards
//	    (-quick: only the ones this run uploads). The disk is only read.
//	ggblobs verify [-j 8] [-hash]
//	    every key the database references is in the bucket with its size
//	    (-hash: and its sha256). Exits 1 if anything is off.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	ok := true
	switch os.Args[1] {
	case "copy":
		ok, err = copyCmd(ctx, os.Args[2:])
	case "verify":
		ok, err = verifyCmd(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		slog.Error("ggblobs", "err", err)
		os.Exit(1)
	}
	if !ok {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ggblobs copy [-from <data dir>/blobs] [-j 4] [-quick]")
	fmt.Fprintln(os.Stderr, "       ggblobs verify [-j 8] [-hash]")
	os.Exit(2)
}

func dataDir() string {
	if d := os.Getenv("GREENGRAM_DATA_DIR"); d != "" {
		return d
	}
	return "data"
}

func bucket(ctx context.Context) (*media.S3, error) {
	cfg, err := media.S3ConfigFromEnv(dataDir())
	if err != nil {
		return nil, err
	}
	// the tools stream past the cache; it's the server's
	cfg.CacheDir = filepath.Join(os.TempDir(), fmt.Sprintf("ggblobs-%d", os.Getpid()))
	cfg.CacheBytes = 0
	return media.OpenS3(ctx, cfg)
}

func copyCmd(ctx context.Context, args []string) (bool, error) {
	fs := flag.NewFlagSet("copy", flag.ExitOnError)
	from := fs.String("from", filepath.Join(dataDir(), "blobs"), "the disk store")
	jobs := fs.Int("j", 4, "uploads at a time")
	quick := fs.Bool("quick", false, "don't download and hash objects that were already there")
	fs.Parse(args)
	if st, err := os.Stat(*from); err != nil || !st.IsDir() {
		return false, fmt.Errorf("-from %s: not a directory", *from)
	}
	dst, err := bucket(ctx)
	if err != nil {
		return false, err
	}
	defer cleanup()
	began := time.Now()
	rep, err := media.CopyDisk(ctx, *from, dst, media.CopyOptions{Jobs: *jobs, Quick: *quick,
		Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }})
	fmt.Printf("files %d (%s): uploaded %d (%s), already there %d, hashed in bucket %d, bad on disk %d, failed %d, in %s\n",
		rep.Files, mb(rep.Bytes), rep.Uploaded, mb(rep.BytesSent), rep.Present, rep.Verified, rep.BadLocal, rep.Failed,
		time.Since(began).Round(time.Second))
	if err != nil {
		return false, err
	}
	return rep.Failed == 0 && rep.BadLocal == 0, nil
}

func verifyCmd(ctx context.Context, args []string) (bool, error) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	jobs := fs.Int("j", 8, "requests at a time")
	hash := fs.Bool("hash", false, "download every object and check its sha256")
	fs.Parse(args)
	url := os.Getenv("GREENGRAM_DATABASE_URL")
	if url == "" {
		return false, fmt.Errorf("GREENGRAM_DATABASE_URL is not set")
	}
	db, err := store.Open(ctx, url)
	if err != nil {
		return false, err
	}
	defer db.Close()
	s, err := bucket(ctx)
	if err != nil {
		return false, err
	}
	defer cleanup()
	began := time.Now()
	rep, err := media.VerifyBlobs(ctx, db.Pool, s, media.VerifyOptions{Jobs: *jobs, Hash: *hash})
	if err != nil {
		return false, err
	}
	fmt.Printf("keys %d: ok %d (%s), missing %d, wrong size %d, wrong hash %d, failed to check %d, in %s\n",
		rep.Keys, rep.OK, mb(rep.Bytes), rep.Missing, rep.WrongSize, rep.BadHash, rep.Failed, time.Since(began).Round(time.Second))
	for _, k := range rep.MissingKeys {
		fmt.Println("missing", k)
	}
	for _, k := range rep.BadKeys {
		fmt.Println("bad", k)
	}
	if rep.OK != rep.Keys {
		return false, nil
	}
	fmt.Println("verify passed")
	return true, nil
}

func cleanup() {
	os.RemoveAll(filepath.Join(os.TempDir(), fmt.Sprintf("ggblobs-%d", os.Getpid())))
}

func mb(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
