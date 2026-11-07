// Package importer moves a greengram-web instance (its SQLite database) into
// this server's Postgres schema. docs/migration.md is the runbook.
//
// The work is cut into stages, each one transaction recorded in
// import_stages, so a failed run picks up at the stage that failed. Every
// stage recomputes what it needs from the SQLite file instead of reading
// back what earlier stages wrote, which is also what lets --dry-run go
// through all of them without touching Postgres.
//
// Not imported yet, because main has nowhere to put it: files, stickers,
// rich messages and gifts on messages (the message itself comes over, the
// media import finds it through import_box_messages and
// import_channel_messages), stars/wallet/fragment, bots' tokens and state,
// reactions, pinned messages, drafts, mutes and archive flags, folders,
// invite links, profile and chat photos. See the hooks list
// in docs/migration.md.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/secret"
)

type Options struct {
	SQLite string        // greengram-web's database file (a copy, opened read-only)
	DB     *pgxpool.Pool // migrated target; may be nil for a dry run
	DryRun bool          // read and convert everything, write nothing
	Force  bool          // wipe a non-empty target first
	// Secret, when set, is the instance secret the phones were encrypted
	// with; every phone is decrypted once to prove it is the right file.
	Secret *secret.Key
	Now    time.Time // what counts as "now" for live sessions; zero = time.Now()
	Log    func(format string, args ...any)
}

// Counts are rows written (or, in a dry run, that would be) and things left
// out, per stage.
type Counts map[string]int

type StageReport struct {
	Name   string
	Counts Counts
	Done   bool // already done by an earlier run, skipped
}

type Report struct {
	Source string
	Stages []StageReport
}

var errNotEmpty = errors.New("target database already has accounts or chats and no import of this file in progress; run with --force to wipe it first")

type stage struct {
	name string
	run  func(ctx context.Context, r *run, w sink) (Counts, error)
}

var stages = []stage{
	{"settings", importSettings},
	{"users", importUsers},
	{"sessions", importSessions},
	{"chats", importChats},
	{"privacy", importPrivacy},
	{"blocks", importBlocks},
	{"private", importPrivate},
	{"channel", importChannelMessages},
	{"finish", finish},
}

// Stages lists the stage names in order.
func Stages() []string {
	out := make([]string, len(stages))
	for i, s := range stages {
		out[i] = s.name
	}
	return out
}

// run is what the stages share: the source, the clock and the user set,
// which nearly every stage filters against.
type run struct {
	src    *source
	now    time.Time
	users  map[int64]bool
	secret *secret.Key
	dry    bool
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

func Run(ctx context.Context, o Options) (Report, error) {
	var rep Report
	if o.DB == nil && !o.DryRun {
		return rep, errors.New("no target database")
	}
	src, err := openSource(ctx, o.SQLite)
	if err != nil {
		return rep, err
	}
	defer src.Close()
	if rep.Source, err = src.fingerprint(ctx); err != nil {
		return rep, err
	}
	r := &run{src: src, now: o.Now, secret: o.Secret, dry: o.DryRun}
	if r.now.IsZero() {
		r.now = time.Now()
	}
	if r.users, err = loadUserIDs(ctx, src); err != nil {
		return rep, err
	}

	done := map[string]bool{}
	if o.DB != nil {
		conn, err := o.DB.Acquire(ctx)
		if err != nil {
			return rep, err
		}
		defer conn.Release()
		// one import at a time; a second one would interleave stages
		var locked bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", importLock).Scan(&locked); err != nil {
			return rep, err
		}
		if !locked {
			return rep, errors.New("another import is running against this database")
		}
		defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", importLock)
		if done, err = prepare(ctx, o, rep.Source); err != nil {
			return rep, err
		}
	}

	for _, s := range stages {
		sr := StageReport{Name: s.name}
		if done[s.name] {
			sr.Done = true
			o.logf("%-9s already done", s.name)
			rep.Stages = append(rep.Stages, sr)
			continue
		}
		start := time.Now()
		if o.DryRun {
			sr.Counts, err = s.run(ctx, r, dry{})
		} else {
			err = pgx.BeginFunc(ctx, o.DB, func(tx pgx.Tx) error {
				var err error
				if sr.Counts, err = s.run(ctx, r, &pgSink{tx: tx}); err != nil {
					return err
				}
				raw, _ := json.Marshal(sr.Counts)
				_, err = tx.Exec(ctx, "INSERT INTO import_stages(stage, source, counts) VALUES ($1, $2, $3)", s.name, rep.Source, raw)
				return err
			})
		}
		if err != nil {
			return rep, fmt.Errorf("stage %s: %w", s.name, err)
		}
		o.logf("%-9s %s (%s)", s.name, sr.Counts, time.Since(start).Round(time.Millisecond))
		rep.Stages = append(rep.Stages, sr)
	}
	return rep, nil
}

const importLock = 0x6767696d706f7274 // "ggimport"

// prepare decides where this run starts: the stages an earlier run of the
// same file finished, nothing on an empty target, and with Force a wipe.
func prepare(ctx context.Context, o Options, fp string) (map[string]bool, error) {
	done := map[string]bool{}
	var migrated bool
	if err := o.DB.QueryRow(ctx, "SELECT to_regclass('import_stages') IS NOT NULL").Scan(&migrated); err != nil {
		return nil, err
	}
	if !migrated {
		if o.DryRun {
			o.logf("target has no import tables yet; the real run migrates it first")
			return done, nil
		}
		return nil, errors.New("target is not migrated (import_stages missing)")
	}
	rows, err := o.DB.Query(ctx, "SELECT stage, source FROM import_stages")
	if err != nil {
		return nil, err
	}
	var other []string
	for rows.Next() {
		var st, src string
		if err := rows.Scan(&st, &src); err != nil {
			rows.Close()
			return nil, err
		}
		if src == fp {
			done[st] = true
		} else {
			other = append(other, st)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var used bool
	if err := o.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users) OR EXISTS(SELECT 1 FROM channels) OR EXISTS(SELECT 1 FROM instance_settings)").Scan(&used); err != nil {
		return nil, err
	}
	switch {
	case o.Force && (used || len(done) > 0 || len(other) > 0):
		if o.DryRun {
			o.logf("target is not empty; the real run with --force wipes it")
			return map[string]bool{}, nil
		}
		o.logf("--force: wiping the target")
		if _, err := o.DB.Exec(ctx, wipeSQL); err != nil {
			return nil, fmt.Errorf("wipe: %w", err)
		}
		return map[string]bool{}, nil
	case len(other) > 0:
		return nil, fmt.Errorf("target holds an import of a different file (stages %s); run with --force to wipe it first", strings.Join(other, ", "))
	case used && len(done) == 0:
		return nil, errNotEmpty
	case o.DryRun:
		// a dry run checks the whole file, whatever is already in
		if len(done) > 0 {
			o.logf("target has %d of %d stages of this file done; the real run resumes", len(done), len(stages))
		}
		return map[string]bool{}, nil
	}
	return done, nil
}

// wipeSQL empties everything an import writes. CASCADE also clears the
// tables other branches hang off users and channels; on a target that is
// being (re)imported that is the point.
const wipeSQL = `TRUNCATE users, sessions, auth_flows, privacy_rules, user_boxes, messages, dialogs, update_journal,
	contacts, channels, channel_members, channel_messages, channel_journal, channel_invites, instance_settings,
	import_stages, import_chats, import_box_messages, import_channel_messages CASCADE;
	ALTER SEQUENCE message_shared_ids RESTART;
	ALTER SEQUENCE channel_ids RESTART`

func loadUserIDs(ctx context.Context, src *source) (map[int64]bool, error) {
	rows, err := src.db.QueryContext(ctx, "SELECT id FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (c Counts) String() string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%d", k, c[k])
	}
	if b.Len() == 0 {
		return "nothing"
	}
	return b.String()
}
