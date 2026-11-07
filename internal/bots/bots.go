// Package bots stores what makes an account a bot: token, commands, menu
// button, the getUpdates queue and callback queries. Message delivery to
// bots happens in Postgres triggers (migration 0020), so nothing on the
// send path has to know about them.
package bots

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/secret"
)

// Kinds. Everything but Custom is run by the server.
const (
	Custom    = "custom"
	BotFather = "botfather"
	Stickers  = "stickers"
	Wallet    = "wallet"
	Fragment  = "fragment"
	SpamBot   = "spambot"
	BugsBot   = "bugsbot"
)

// MaxPerOwner is Telegram's limit on bots per account.
const MaxPerOwner = 20

type Service struct {
	db     *pgxpool.Pool
	notify *Notifier
	// TokenKey keeps tokens readable for their owner (BotFather's bot page);
	// without it only the hash is stored and Token finds nothing.
	TokenKey *secret.Key
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db, notify: newNotifier(db)}
}

// Notifier is how long polls and callback waits learn about new rows.
func (s *Service) Notifier() *Notifier { return s.notify }

type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// MenuButton is a web app button; a nil *MenuButton means "commands".
type MenuButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type Bot struct {
	UserID      int64
	OwnerID     int64 // 0 for built-ins
	Kind        string
	Description string
	Commands    []Command
	Menu        *MenuButton
	MainAppURL  string
	InfoVersion int
	WebAppKey   []byte
	// from users
	Username string
	Name     string
	About    string
	Disabled bool
	// the bot or its owner is frozen: the Bot API only reads
	Frozen bool
	State  State
}

func (b Bot) BuiltIn() bool { return b.Kind != Custom }

// Usable is Restrict bot usage: the owner and AccessUsers (server ids) may
// interact with this bot, nobody else.
func (b Bot) Usable(user int64) bool {
	return !bool(b.State.Settings.Access) || user == b.OwnerID || slices.Contains(b.State.Settings.AccessUsers, user)
}

// Usable is the same check for a send, which doesn't otherwise load the bot.
// Anyone who isn't a bot has no such row, and everyone may write to them.
func (s *Service) Usable(ctx context.Context, bot, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT coalesce(state #>> '{settings,access}' IN ('true', '1'), false) IS NOT TRUE
		OR owner_id = $2 OR coalesce(state #> '{settings,access_users}' @> to_jsonb($2::bigint), false)
		FROM bots WHERE user_id = $1`, bot, user).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return ok, err
}

const botCols = `b.user_id, coalesce(b.owner_id, 0), b.kind, b.description, b.commands, b.menu_button, coalesce(b.main_app_url, ''),
	b.info_version, b.webapp_key, u.username, u.name, u.bio, u.disabled, b.state,
	u.account_state = 'frozen' OR EXISTS(SELECT 1 FROM users o WHERE o.id = b.owner_id AND o.account_state = 'frozen')`

func scanBot(row pgx.Row) (Bot, error) {
	var (
		b        Bot
		cmds     []byte
		menu     []byte
		state    []byte
		disabled bool
	)
	err := row.Scan(&b.UserID, &b.OwnerID, &b.Kind, &b.Description, &cmds, &menu, &b.MainAppURL, &b.InfoVersion, &b.WebAppKey,
		&b.Username, &b.Name, &b.About, &disabled, &state, &b.Frozen)
	if err != nil {
		return b, err
	}
	b.Disabled = disabled
	b.State = parseState(state)
	if err := json.Unmarshal(cmds, &b.Commands); err != nil {
		return b, err
	}
	if len(menu) > 0 && string(menu) != "null" {
		b.Menu = &MenuButton{}
		if err := json.Unmarshal(menu, b.Menu); err != nil {
			return b, err
		}
	}
	return b, nil
}

func (s *Service) one(ctx context.Context, where string, args ...any) (Bot, bool, error) {
	b, err := scanBot(s.db.QueryRow(ctx, "SELECT "+botCols+" FROM bots b JOIN users u ON u.id = b.user_id WHERE "+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, false, nil
	}
	return b, err == nil, err
}

func (s *Service) Get(ctx context.Context, id int64) (Bot, bool, error) {
	return s.one(ctx, "b.user_id=$1", id)
}

// Many loads the bots among ids; people are left out.
func (s *Service) Many(ctx context.Context, list []int64) (map[int64]Bot, error) {
	out := map[int64]Bot{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT "+botCols+" FROM bots b JOIN users u ON u.id = b.user_id WHERE b.user_id = ANY($1)", list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out[b.UserID] = b
	}
	return out, rows.Err()
}

func (s *Service) ByKind(ctx context.Context, kind string) (Bot, bool, error) {
	return s.one(ctx, "b.kind=$1 AND b.owner_id IS NULL", kind)
}

// ByToken is the bot a Bot API token belongs to. A bot whose owner was
// disabled stops working, like greengram-web.
func (s *Service) ByToken(ctx context.Context, token string) (Bot, bool, error) {
	id, _, ok := strings.Cut(token, ":")
	if !ok {
		return Bot{}, false, nil
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return Bot{}, false, nil
	}
	return s.one(ctx, `b.token_hash=$1 AND b.kind='custom' AND NOT u.disabled
		AND NOT EXISTS(SELECT 1 FROM users o WHERE o.id = b.owner_id AND o.disabled)`, hashToken(token))
}

// Owned lists owner's bots, newest first.
func (s *Service) Owned(ctx context.Context, owner int64) ([]Bot, error) {
	rows, err := s.db.Query(ctx, "SELECT "+botCols+" FROM bots b JOIN users u ON u.id = b.user_id WHERE b.owner_id=$1 AND NOT u.disabled ORDER BY b.user_id DESC", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bot
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// OwnedByUsername is one of owner's bots by @username.
func (s *Service) OwnedByUsername(ctx context.Context, owner int64, username string) (Bot, bool, error) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	return s.one(ctx, "b.owner_id=$1 AND lower(u.username)=$2 AND NOT u.disabled", owner, name)
}

// ByUsername is any live bot by @username.
func (s *Service) ByUsername(ctx context.Context, username string) (Bot, bool, error) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	return s.one(ctx, "lower(u.username)=$1 AND NOT u.disabled", name)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// WebAppKey is what Telegram signs mini app initData with.
func WebAppKey(token string) []byte {
	m := hmac.New(sha256.New, []byte("WebAppData"))
	m.Write([]byte(token))
	return m.Sum(nil)
}

func randomKey() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return b
}

func newToken(id int64) string {
	return strconv.FormatInt(id, 10) + ":" + base64.RawURLEncoding.EncodeToString(randomKey())
}

// usernameRe is BotFather's rule: 5-32 characters ending in "bot".
var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,28}bot$`)

// Error is a refusal meant for the person (BotFather says it in chat).
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func refuse(msg string) error { return &Error{msg} }

// ErrFrozen refuses a frozen owner's change.
var ErrFrozen = refuse("Your account is frozen, so it can't make or change bots.")

// Frozen says whether user's account is frozen (read-only).
func (s *Service) Frozen(ctx context.Context, user int64) (bool, error) {
	var frozen bool
	err := s.db.QueryRow(ctx, "SELECT account_state = 'frozen' FROM users WHERE id=$1", user).Scan(&frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return frozen, err
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// CheckUsername says why a bot username can't be had, or nil.
func (s *Service) CheckUsername(ctx context.Context, username string) error {
	return checkUsername(ctx, s.db, username)
}

func checkUsername(ctx context.Context, q querier, username string) error {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if !strings.HasSuffix(name, "bot") {
		return refuse("Sorry, the username must end in 'bot'. E.g. 'Tetris_bot' or 'Tetrisbot'.")
	}
	if !usernameRe.MatchString(name) {
		return refuse("Sorry, this username is invalid. Use 5-32 Latin letters, numbers or underscores, starting with a letter.")
	}
	// a Fragment listing holds the name even with nobody using it
	var taken bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1)
		OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1)
		OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1)`, name).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return refuse("Sorry, this username is already taken. Please try something different.")
	}
	return nil
}

// Create makes a bot for owner and returns it with its first token.
func (s *Service) Create(ctx context.Context, owner int64, name, username string) (Bot, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 || strings.HasPrefix(name, "/") {
		return Bot{}, "", refuse("Sorry, this name can't be used. Use 1-64 characters.")
	}
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	var id int64
	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// usernames are checked and taken under one lock, like sign-up
		if _, err := tx.Exec(ctx, "LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return err
		}
		var n int
		var frozen bool
		if err := tx.QueryRow(ctx, `SELECT count(*), (SELECT account_state = 'frozen' FROM users WHERE id=$1)
			FROM bots b JOIN users u ON u.id = b.user_id WHERE b.owner_id=$1 AND NOT u.disabled`, owner).Scan(&n, &frozen); err != nil {
			return err
		}
		if frozen {
			return ErrFrozen
		}
		if n >= MaxPerOwner {
			return refuse("That I cannot do. You can only have up to 20 bots at the moment.")
		}
		if err := checkUsername(ctx, tx, username); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "INSERT INTO users(username, name, password, is_bot) VALUES ($1, $2, '', true) RETURNING id", username, name).Scan(&id); err != nil {
			return err
		}
		token = newToken(id)
		_, err := tx.Exec(ctx, "INSERT INTO bots(user_id, owner_id, kind, token_hash, webapp_key, token_sealed) VALUES ($1, $2, 'custom', $3, $4, $5)",
			id, owner, hashToken(token), WebAppKey(token), s.seal(id, token))
		return err
	})
	if err != nil {
		return Bot{}, "", err
	}
	b, _, err := s.Get(ctx, id)
	return b, token, err
}

// Revoke replaces the bot's token; the old one stops working at once.
func (s *Service) Revoke(ctx context.Context, bot int64) (string, error) {
	token := newToken(bot)
	tag, err := s.db.Exec(ctx, "UPDATE bots SET token_hash=$2, webapp_key=$3, token_sealed=$4 WHERE user_id=$1 AND kind='custom'", bot, hashToken(token), WebAppKey(token), s.seal(bot, token))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", refuse("Bot not found.")
	}
	return token, nil
}

// Delete takes the bot down: it can't sign in or be written to and its
// username is free again. Chats with it stay, like a deleted account.
func (s *Service) Delete(ctx context.Context, bot int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE bots SET token_hash=NULL, webapp_key=NULL, token_sealed=NULL WHERE user_id=$1", bot); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE users SET disabled=true, username='deleted' || id::text || 'bot' WHERE id=$1 AND is_bot", bot)
		return err
	})
}

// bump changes a bot field and moves info_version so clients refetch.
func (s *Service) bump(ctx context.Context, bot int64, set string, args ...any) error {
	_, err := s.db.Exec(ctx, "UPDATE bots SET "+set+", info_version = info_version + 1 WHERE user_id=$1", append([]any{bot}, args...)...)
	return err
}

func (s *Service) SetCommands(ctx context.Context, bot int64, list []Command) error {
	if list == nil {
		list = []Command{}
	}
	raw, _ := json.Marshal(list)
	return s.bump(ctx, bot, "commands=$2", raw)
}

func (s *Service) SetDescription(ctx context.Context, bot int64, text string) error {
	return s.bump(ctx, bot, "description=$2", text)
}

// SetAbout is the short description, shown as the profile bio.
func (s *Service) SetAbout(ctx context.Context, bot int64, text string) error {
	if _, err := s.db.Exec(ctx, "UPDATE users SET bio=$2 WHERE id=$1", bot, text); err != nil {
		return err
	}
	return s.bump(ctx, bot, "description=description")
}

func (s *Service) SetName(ctx context.Context, bot int64, name string) error {
	_, err := s.db.Exec(ctx, "UPDATE users SET name=$2 WHERE id=$1", bot, name)
	return err
}

// SetMenu sets the default menu button; nil is the commands list.
func (s *Service) SetMenu(ctx context.Context, bot int64, m *MenuButton) error {
	var raw []byte
	if m != nil {
		raw, _ = json.Marshal(m)
	}
	return s.bump(ctx, bot, "menu_button=$2", raw)
}

// SetChatMenu overrides the menu button in one private chat; nil goes
// back to the bot's default.
func (s *Service) SetChatMenu(ctx context.Context, bot, user int64, m *MenuButton, commands bool) error {
	if m == nil && !commands {
		_, err := s.db.Exec(ctx, "DELETE FROM bot_chat_menu_buttons WHERE bot_id=$1 AND user_id=$2", bot, user)
		return err
	}
	raw := []byte(`{"commands":true}`)
	if m != nil {
		raw, _ = json.Marshal(m)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO bot_chat_menu_buttons(bot_id, user_id, button) VALUES ($1, $2, $3)
		ON CONFLICT (bot_id, user_id) DO UPDATE SET button = excluded.button`, bot, user, raw)
	return err
}

// ChatMenu is the menu button user sees in their chat with b.
func (s *Service) ChatMenu(ctx context.Context, b Bot, user int64) (*MenuButton, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, "SELECT button FROM bot_chat_menu_buttons WHERE bot_id=$1 AND user_id=$2", b.UserID, user).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return b.Menu, nil
	}
	if err != nil {
		return nil, err
	}
	var m struct {
		MenuButton
		Commands bool `json:"commands"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Commands {
		return nil, err
	}
	return &m.MenuButton, nil
}

// SetMainApp is the URL the profile's "Open App" button launches.
func (s *Service) SetMainApp(ctx context.Context, bot int64, url string) error {
	return s.bump(ctx, bot, "main_app_url=nullif($2, '')", url)
}
