package bots

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxApps is how many direct links a bot can have.
const MaxApps = 100

// App is a direct link: a named mini app of a bot.
type App struct {
	ID          int64
	Bot         int64
	AccessHash  int64
	ShortName   string
	Title       string
	Description string
	URL         string
	Photo       int64
	Version     int
}

const appCols = "id, bot_id, access_hash, short_name, title, description, url, photo_id, version"

func scanApp(row pgx.Row) (App, error) {
	var a App
	err := row.Scan(&a.ID, &a.Bot, &a.AccessHash, &a.ShortName, &a.Title, &a.Description, &a.URL, &a.Photo, &a.Version)
	return a, err
}

func (s *Service) oneApp(ctx context.Context, where string, args ...any) (App, bool, error) {
	a, err := scanApp(s.db.QueryRow(ctx, "SELECT "+appCols+" FROM bot_apps WHERE "+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

// Apps lists a bot's direct links, oldest first.
func (s *Service) Apps(ctx context.Context, bot int64) ([]App, error) {
	rows, err := s.db.Query(ctx, "SELECT "+appCols+" FROM bot_apps WHERE bot_id=$1 ORDER BY id", bot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) AppByName(ctx context.Context, bot int64, name string) (App, bool, error) {
	return s.oneApp(ctx, "bot_id=$1 AND short_name=$2", bot, strings.ToLower(name))
}

func (s *Service) AppByID(ctx context.Context, id int64) (App, bool, error) {
	return s.oneApp(ctx, "id=$1", id)
}

// appNameRe is the page's rule: starts with a letter, 3-32 of a-z, 0-9, _.
var appNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)

func checkApp(a App) error {
	switch {
	case a.Title == "" || utf8.RuneCountInString(a.Title) > 64:
		return refuse("The title can be up to 64 characters.")
	case a.Description == "" || utf8.RuneCountInString(a.Description) > 512:
		return refuse("The description can be up to 512 characters.")
	case !strings.HasPrefix(a.URL, "https://") || len(a.URL) > 2048:
		return refuse("Use an HTTPS address.")
	case a.Photo == 0:
		return refuse("Picture is required.")
	}
	return nil
}

// CreateApp adds a direct link.
func (s *Service) CreateApp(ctx context.Context, a App) (App, error) {
	a.ShortName = strings.ToLower(a.ShortName)
	if !appNameRe.MatchString(a.ShortName) {
		return a, refuse("The link starts with a letter and has 3-32 Latin letters, numbers and underscores.")
	}
	if err := checkApp(a); err != nil {
		return a, err
	}
	var n int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM bot_apps WHERE bot_id=$1", a.Bot).Scan(&n); err != nil {
		return a, err
	}
	if n >= MaxApps {
		return a, refuse("A bot can have up to 100 direct links.")
	}
	var b [8]byte
	rand.Read(b[:])
	a.AccessHash = int64(binary.LittleEndian.Uint64(b[:]) >> 1)
	got, err := scanApp(s.db.QueryRow(ctx, `INSERT INTO bot_apps(bot_id, access_hash, short_name, title, description, url, photo_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+appCols, a.Bot, a.AccessHash, a.ShortName, a.Title, a.Description, a.URL, a.Photo))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return a, refuse("This link is already taken.")
	}
	return got, err
}

// UpdateApp changes everything but the link itself, which people may have saved.
func (s *Service) UpdateApp(ctx context.Context, a App) error {
	if err := checkApp(a); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, "UPDATE bot_apps SET title=$3, description=$4, url=$5, photo_id=$6, version=version+1 WHERE id=$1 AND bot_id=$2",
		a.ID, a.Bot, a.Title, a.Description, a.URL, a.Photo)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return refuse("This link doesn't exist.")
	}
	return nil
}

func (s *Service) DeleteApp(ctx context.Context, bot, id int64) error {
	tag, err := s.db.Exec(ctx, "DELETE FROM bot_apps WHERE id=$1 AND bot_id=$2", id, bot)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return refuse("This link doesn't exist.")
	}
	return nil
}
