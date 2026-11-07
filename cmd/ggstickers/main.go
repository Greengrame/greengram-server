// ggstickers imports the old server's embedded sticker and emoji packs
// (greengram-web resources/sticker-packs) into an instance. Run it with the
// server's environment; importing the same bundle again changes nothing.
//
//	GREENGRAM_DATABASE_URL=... GREENGRAM_DATA_DIR=data go run ./cmd/ggstickers ../greengram-web/resources/sticker-packs
//
// With -reactions it imports Telegram's reaction artwork instead, from the
// telegram_reactions_export directory gramsrv's stickerfetch writes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Greengrame/greengram-server/internal/config"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/store"
)

func main() {
	reactions := flag.Bool("reactions", false, "the directory is a reactions export")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: ggstickers <sticker-packs dir with active.json>")
		fmt.Fprintln(os.Stderr, "       ggstickers -reactions <telegram_reactions_export dir>")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *reactions); err != nil {
		slog.Error("ggstickers", "err", err)
		os.Exit(1)
	}
}

func run(dir string, reactions bool) error {
	ctx := context.Background()
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	blobs, err := media.OpenBlobs(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	m := media.New(db.Pool, media.Config{Blobs: blobs, Parts: filepath.Join(cfg.DataDir, "uploads"), DC: cfg.DC})
	if reactions {
		n, err := m.ImportReactions(ctx, dir)
		if err != nil {
			return err
		}
		fmt.Println("imported", n, "reactions")
		return nil
	}
	res, err := m.ImportPacks(ctx, dir)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d packs, %d stickers\n", res.Packs, res.Stickers)
	return nil
}
