// ggimport moves a greengram-web instance into this server's database:
// accounts, sessions, privacy, contacts, chats and messages. Run it against
// a copy of the web server's SQLite file; docs/migration.md has the steps.
//
//	ggimport -sqlite greengram.sqlite3 -db postgres://... [-secret instance-secret.key] [-dry-run] [-force]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/Greengrame/greengram-server/internal/importer"
	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/store"
)

func main() {
	var (
		sqlite = flag.String("sqlite", "", "greengram-web database (a copy; opened read-only)")
		dbURL  = flag.String("db", os.Getenv("GREENGRAM_DATABASE_URL"), "target Postgres (default $GREENGRAM_DATABASE_URL)")
		key    = flag.String("secret", "", "the web server's instance-secret.key, to check every phone decrypts with it")
		dry    = flag.Bool("dry-run", false, "read and convert everything, write nothing")
		force  = flag.Bool("force", false, "wipe a target that already has data, then import")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: ggimport -sqlite FILE [-db URL] [-secret FILE] [-dry-run] [-force]\n\nstages: %v\n\n", importer.Stages())
		flag.PrintDefaults()
	}
	flag.Parse()
	if *sqlite == "" || flag.NArg() > 0 || *dbURL == "" && !*dry {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*sqlite, *dbURL, *key, *dry, *force); err != nil {
		fmt.Fprintln(os.Stderr, "ggimport:", err)
		os.Exit(1)
	}
}

func run(sqlite, dbURL, keyFile string, dry, force bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	o := importer.Options{SQLite: sqlite, DryRun: dry, Force: force, Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}
	if keyFile != "" {
		// read only: secret.Load would make a new key if the path is wrong
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return err
		}
		if len(b) != 32 {
			return fmt.Errorf("%s: instance secret must be exactly 32 bytes", keyFile)
		}
		o.Secret = secret.FromBytes(b)
	}
	if dbURL != "" {
		db, err := store.Open(ctx, dbURL)
		if err != nil {
			return err
		}
		defer db.Close()
		if !dry {
			if err := db.Migrate(ctx); err != nil {
				return err
			}
		}
		o.DB = db.Pool
	}
	if dry {
		fmt.Println("dry run: nothing is written")
	}
	rep, err := importer.Run(ctx, o)
	fmt.Println("source:", rep.Source)
	if err != nil {
		return err
	}
	if dry {
		fmt.Println("dry run ok")
	} else {
		fmt.Println("import done")
	}
	return nil
}
