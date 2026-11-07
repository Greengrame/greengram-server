// ggcollectibles imports a collectible gifts bundle (models, patterns and
// backdrops with their rarity, see scripts/collectibles.py) into an instance.
// Run it with the server's environment; importing the same bundle again
// changes nothing. A collection imported here is offered in the admin
// panel's gift form; nothing upgrades into it until an admin says so.
//
// With -gifts it also puts a gift in the catalog for every collection no
// gift upgrades into yet (see stars.GiftsForSets): the collection's art and
// title, the price, upgrade and transfer fees from the flags, on sale and
// unlimited. -supply N limits a collection that has no limit yet (and its
// gift's stock with it); -off-sale leaves the gifts for an admin to turn on.
//
//	GREENGRAM_DATABASE_URL=... GREENGRAM_DATA_DIR=data go run ./cmd/ggcollectibles [-gifts] <bundle dir with active.json>
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Greengrame/greengram-server/internal/config"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/store"
)

func main() {
	gifts := flag.Bool("gifts", false, "put a gift in the catalog for every collection that has none, on sale")
	var d stars.SetGiftDefaults
	flag.Int64Var(&d.Stars, "price", 100, "with -gifts: the gift's price in Stars")
	flag.Int64Var(&d.ConvertStars, "convert", 85, "with -gifts: what the gift converts to")
	flag.Int64Var(&d.UpgradeStars, "upgrade", 50, "with -gifts: what upgrading it costs")
	flag.Int64Var(&d.TransferStars, "transfer", 25, "with -gifts: what moving one of its collectibles costs")
	flag.IntVar(&d.Supply, "supply", 0, "with -gifts: limit a collection that has no limit yet to N; 0 leaves it unlimited")
	flag.BoolVar(&d.OffSale, "off-sale", false, "with -gifts: make the gifts off sale until an admin turns them on")
	flag.BoolVar(&d.NoVariants, "no-variants", false, "with -gifts: sell them as plain gifts, not as picked variants")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ggcollectibles [-gifts [-price N -convert N -upgrade N -transfer N -supply N -off-sale -no-variants]] <collectibles bundle dir with active.json>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	var defaults *stars.SetGiftDefaults
	if *gifts {
		defaults = &d
	}
	if err := run(flag.Arg(0), defaults); err != nil {
		slog.Error("ggcollectibles", "err", err)
		os.Exit(1)
	}
}

func run(dir string, gifts *stars.SetGiftDefaults) error {
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
	began := time.Now()
	res, err := m.ImportCollectibles(ctx, dir)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d collections: %d models, %d patterns, %d backdrops in %s\n", res.Sets, res.Models, res.Patterns, res.Backdrops,
		time.Since(began).Round(time.Second))
	if gifts == nil {
		return nil
	}
	made, err := stars.New(db.Pool, nil).GiftsForSets(ctx, *gifts)
	if err != nil {
		return err
	}
	sale, supply := "on sale", "unlimited"
	if gifts.OffSale {
		sale = "off sale"
	}
	if gifts.Supply > 0 {
		supply = fmt.Sprintf("supply %d where unset", gifts.Supply)
	}
	fmt.Printf("catalog gifts: %d made, %s, %s (%d Stars, converts to %d, upgrade %d, transfer %d)\n", len(made.Created), sale, supply,
		gifts.Stars, gifts.ConvertStars, gifts.UpgradeStars, gifts.TransferStars)
	for _, t := range made.Skipped {
		fmt.Printf("  skipped %s: a gift with that title is in the catalog\n", t)
	}
	return nil
}
