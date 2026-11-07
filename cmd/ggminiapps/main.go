// ggminiapps installs the mini apps bundle: the BotFather, Stickers, Wallet
// and Fragment mini apps captured from Telegram, and the files made from them
// (BotFather's page templates, the emoji lists, the Bugs and Suggestions page,
// the built-in bots' pictures).
// None of it is in this repository, since it is Telegram's; the server runs
// without it and the mini apps say what is missing.
//
//	ggminiapps [-dir DIR] install <bundle.tar.gz | https://...> [-sha256 HEX]
//	ggminiapps [-dir DIR] status
//	ggminiapps [-dir DIR] pack <out.tar.gz>
//
// DIR defaults to GREENGRAM_MINIAPPS_DIR, else <GREENGRAM_DATA_DIR>/miniapps,
// the same place the server looks. The bundle is a tar.gz of that directory:
// <app>/<path> for the captured files, each checked against the lock built
// into the server, and greengram/ for the rest (see internal/miniapps/bundle.go).
// pack writes one from an installed directory. The server picks up a new
// install without a restart, except the bots' pictures, set on the next start.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Greengrame/greengram-server/internal/miniapps"
)

const maxBundle = 1 << 30 // unpacked, all files together

func main() {
	dir := flag.String("dir", defaultDir(), "the mini apps directory")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ggminiapps [-dir DIR] install <bundle.tar.gz | https://...> [-sha256 HEX]")
		fmt.Fprintln(os.Stderr, "       ggminiapps [-dir DIR] status")
		fmt.Fprintln(os.Stderr, "       ggminiapps [-dir DIR] pack <out.tar.gz>")
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "install":
		sub := flag.NewFlagSet("install", flag.ExitOnError)
		sum := sub.String("sha256", "", "the bundle's sha256; checked before anything is unpacked")
		sub.Parse(reorder(args[1:]))
		if sub.NArg() != 1 {
			flag.Usage()
			os.Exit(2)
		}
		if err = install(*dir, sub.Arg(0), *sum); err == nil {
			err = status(*dir)
		}
	case "status":
		err = status(*dir)
	case "pack":
		if len(args) != 2 {
			flag.Usage()
			os.Exit(2)
		}
		err = pack(*dir, args[1])
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ggminiapps:", err)
		os.Exit(1)
	}
}

// reorder lets flags come after the bundle argument too.
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, args[i])
	}
	return append(flags, rest...)
}

func defaultDir() string {
	if d := os.Getenv("GREENGRAM_MINIAPPS_DIR"); d != "" {
		return d
	}
	data := os.Getenv("GREENGRAM_DATA_DIR")
	if data == "" {
		data = "data"
	}
	return filepath.Join(data, "miniapps")
}

// topDirs are the only things a bundle may hold at its top.
func topDirs() []string {
	return append(slices.Clone(miniapps.Apps), miniapps.ExtrasDir)
}

func install(dir, src, want string) error {
	f, err := open(src)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if want != "" {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
			return fmt.Errorf("the bundle's sha256 is %s, not %s", got, want)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a tar.gz: %w", err)
	}
	// unpack next to the target and swap the app directories in at the end,
	// so a broken bundle leaves the old install alone
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	tr := tar.NewReader(z)
	var total int64
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		top, _, _ := strings.Cut(name, "/")
		if !fs.ValidPath(name) || strings.HasPrefix(name, "../") || !slices.Contains(topDirs(), top) {
			return fmt.Errorf("unexpected path in the bundle: %q", h.Name)
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				return fmt.Errorf("unexpected path in the bundle: %q", h.Name)
			}
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; total > maxBundle {
				return errors.New("the bundle unpacks to more than 1 GB")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, err = io.CopyN(out, tr, h.Size)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			seen[top] = true
		default:
			return fmt.Errorf("%s: only files and directories go in a bundle", h.Name)
		}
	}
	if len(seen) == 0 {
		return errors.New("the bundle is empty")
	}
	for _, top := range topDirs() {
		if !seen[top] {
			continue
		}
		old := filepath.Join(dir, top)
		if err := os.RemoveAll(old); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(stage, top), old); err != nil {
			return err
		}
	}
	fmt.Printf("installed %s into %s\n", src, dir)
	return nil
}

// open gives a local copy of the bundle: a file as it is, a URL downloaded.
func open(src string) (*os.File, error) {
	tmp, err := os.CreateTemp("", "ggminiapps-*.tar.gz")
	if err != nil {
		return nil, err
	}
	var in io.ReadCloser
	if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
		c := &http.Client{Timeout: 30 * time.Minute}
		resp, err := c.Get(src)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s", src, resp.Status)
		}
		in = resp.Body
	} else if in, err = os.Open(src); err != nil {
		return nil, err
	}
	defer in.Close()
	if _, err := io.Copy(tmp, io.LimitReader(in, maxBundle)); err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return tmp, nil
}

func fileSum(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// status says, per app, how many pinned files are there and match.
func status(dir string) error {
	locks, err := miniapps.LoadLocks()
	if err != nil {
		return err
	}
	fmt.Printf("mini apps in %s\n", dir)
	bad := false
	for _, app := range miniapps.Apps {
		l := locks[app]
		ok, missing, changed := 0, 0, 0
		for key, a := range l.Assets {
			sum, err := fileSum(filepath.Join(dir, app, filepath.FromSlash(key)))
			switch {
			case err != nil:
				missing++
			case sum != a.SHA256:
				changed++
			default:
				ok++
			}
		}
		state := "ok"
		switch {
		case ok == 0:
			state = "not installed"
		case missing+changed > 0:
			state, bad = "incomplete", true
		}
		fmt.Printf("  %-10s %-13s %d of %d files", app, state, ok, len(l.Assets))
		if changed > 0 {
			fmt.Printf(", %d differ from the lock", changed)
		}
		fmt.Println()
	}
	count := func(sub, pattern string) int {
		m, _ := filepath.Glob(filepath.Join(dir, miniapps.ExtrasDir, sub, pattern))
		return len(m)
	}
	bugs := "no Bugs page"
	if _, err := os.Stat(filepath.Join(dir, miniapps.ExtrasDir, "bugs", "index.html")); err == nil {
		bugs = "Bugs page"
	}
	fmt.Printf("  %-10s %d BotFather pages, %d static files, %d bot pictures, %s\n", miniapps.ExtrasDir,
		count("botfather_pages", "*.html"), count("static", "*"), count("avatars", "*.jpg"), bugs)
	if bad {
		return errors.New("some apps are incomplete: install a bundle made for this server's version")
	}
	return nil
}

// pack writes the installed directory as a bundle: the files the locks pin
// (only ones that match) and everything under greengram/.
func pack(dir, out string) error {
	locks, err := miniapps.LoadLocks()
	if err != nil {
		return err
	}
	var files []string
	for _, app := range miniapps.Apps {
		for key, a := range locks[app].Assets {
			p := app + "/" + key
			sum, err := fileSum(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil {
				continue
			}
			if sum != a.SHA256 {
				return fmt.Errorf("%s differs from the lock", p)
			}
			files = append(files, p)
		}
	}
	extras := filepath.Join(dir, miniapps.ExtrasDir)
	filepath.WalkDir(extras, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(files) == 0 {
		return fmt.Errorf("nothing to pack in %s", dir)
	}
	slices.Sort(files)
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	tw := tar.NewWriter(z)
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			f.Close()
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}); err != nil {
			f.Close()
			return err
		}
		if _, err := tw.Write(data); err != nil {
			f.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := z.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	sum, err := fileSum(out)
	if err != nil {
		return err
	}
	fmt.Printf("packed %d files into %s\nsha256 %s\n", len(files), out, sum)
	return nil
}
