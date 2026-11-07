package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundle(t *testing.T, files map[string]string, extra ...*tar.Header) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	tw := tar.NewWriter(z)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	for _, h := range extra {
		tw.WriteHeader(h)
	}
	tw.Close()
	z.Close()
	f.Close()
	return p
}

func TestInstallAndPack(t *testing.T) {
	dir := t.TempDir()
	src := bundle(t, map[string]string{"greengram/botfather_pages/list.html": "<ul></ul>", "./greengram/static/stickers-emoji.json": "[]"})
	if err := install(dir, src, ""); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "greengram", "static", "stickers-emoji.json")); err != nil || string(b) != "[]" {
		t.Fatalf("installed file: %q %v", b, err)
	}
	if err := install(dir, src, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("wrong sum: %v", err)
	}
	out := filepath.Join(t.TempDir(), "again.tar.gz")
	if err := pack(dir, out); err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	if err := install(dir2, out, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "greengram", "botfather_pages", "list.html")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesOddPaths(t *testing.T) {
	cases := []string{
		bundle(t, map[string]string{"../evil": "x"}),
		bundle(t, map[string]string{"/etc/evil": "x"}),
		bundle(t, map[string]string{"elsewhere/x": "x"}),
		bundle(t, map[string]string{"greengram/.hidden": "x"}),
		bundle(t, nil, &tar.Header{Name: "greengram/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}),
		bundle(t, nil),
	}
	for i, src := range cases {
		dir := t.TempDir()
		if err := install(dir, src, ""); err == nil {
			t.Errorf("case %d installed", i)
		}
		if _, err := os.Stat(filepath.Join(dir, "greengram")); err == nil {
			t.Errorf("case %d left files behind", i)
		}
	}
}
