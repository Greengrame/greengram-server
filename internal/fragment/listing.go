package fragment

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Greengrame/greengram-server/internal/channels"
)

// Listing is a sale offer from the page or the admin panel. CountryCode and
// DisplayFormat are nil when the request left them out (keep what's stored).
type Listing struct {
	Kind          string
	Value         string
	Amount        string
	CountryCode   *string
	DisplayFormat *string
	AlternativeID string // replacement when selling the primary identifier
	// ChannelUsername is the channel's next public username when selling the
	// one it has. Blank leaves the link on until it sells.
	ChannelUsername string
	Password        string
}

var (
	listUsernameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)
	// what only an administrator can put up, and its later owner resell:
	// shorter names and names that are all digits
	premiumUsernameRe = regexp.MustCompile(`^[a-z0-9_]*[a-z0-9][a-z0-9_]*$`)
	listNumberRe      = regexp.MustCompile(`^[1-9]\d{0,127}$`)
	codeRe            = regexp.MustCompile(`^\d{1,127}$`)
	formatRe          = regexp.MustCompile(`^[+\d ()-]+$`)
	numberJunkRe      = regexp.MustCompile(`[\s()-]`)
	spaceRe           = regexp.MustCompile(`\s`)
	nonDigitRe        = regexp.MustCompile(`\D`)
)

// Names the built-in accounts use; nobody sells them.
var reserved = map[string]bool{
	"wallet": true, "botfather": true, "stickers": true, "fragmebot": true, "admin": true,
	"greengram_stars": true, "instance_stars_payments": true, "instance_premium_payments": true, "instance_giveaways": true, "instance_gifts": true, "instance_deleted_accounts": true,
}

func trimMarker(v string) string {
	if v != "" && (v[0] == '@' || v[0] == '+') {
		return v[1:]
	}
	return v
}

// Reserve is the admin panel listing an identifier nobody owns yet.
func (s *Service) Reserve(ctx context.Context, adminID int64, l Listing) (map[string]any, error) {
	return s.list(ctx, adminID, l, true, false, "")
}

// Publish is an owner listing their collectible. Selling the primary
// username or number needs the sign-in password and a replacement.
func (s *Service) Publish(ctx context.Context, userID int64, sessionHash string, l Listing) (map[string]any, error) {
	me, ok, err := s.person(ctx, s.db, userID, false)
	if err != nil || !ok {
		return nil, notFound(err)
	}
	value := strings.ToLower(numberJunkRe.ReplaceAllString(trimMarker(l.Value), ""))
	primary := l.Kind == "username" && me.Username == value || l.Kind == "number" && me.Lookup != "" && me.Lookup == s.lookup(value)
	if primary && !checkPassword(l.Password, me.Password) {
		return nil, fail(400, "Enter your current sign-in password to sell a primary identifier.")
	}
	if l.Kind == "username" {
		// the sale makes the channel private, that takes the same password
		mine, err := s.ownChannelHolds(ctx, s.db, value, userID)
		if err != nil {
			return nil, err
		}
		if mine && !checkPassword(l.Password, me.Password) {
			return nil, fail(400, "Enter your current sign-in password to sell a channel's username.")
		}
	}
	out, err := s.list(ctx, userID, l, false, primary, sessionHash)
	if err == nil {
		s.usernamesChanged(userID)
	}
	return out, err
}

func (s *Service) usernamesChanged(users ...int64) {
	if s.UsernamesChanged == nil {
		return
	}
	for _, id := range users {
		s.UsernamesChanged(id)
	}
}

// ownChannelHolds says whether a channel user created has this public username.
func (s *Service) ownChannelHolds(ctx context.Context, q querier, name string, user int64) (bool, error) {
	var mine bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channels c JOIN channel_members m ON m.channel_id = c.id
		WHERE lower(c.username)=$1 AND NOT c.deleted AND m.user_id=$2 AND m.status='creator')`, name, user).Scan(&mine)
	return mine, err
}

func (s *Service) list(ctx context.Context, actorID int64, l Listing, administrative, verified bool, sessionHash string) (map[string]any, error) {
	actor, ok, err := s.person(ctx, s.db, actorID, false)
	if err != nil || !ok {
		return nil, notFound(err)
	}
	kind := l.Kind
	junk := spaceRe
	if kind == "number" {
		junk = numberJunkRe
	}
	value := strings.ToLower(junk.ReplaceAllString(trimMarker(strings.TrimSpace(l.Value)), ""))
	units, err := parseTON(l.Amount)
	if err != nil {
		return nil, err
	}
	var code, format string
	if l.CountryCode != nil {
		code = strings.TrimPrefix(*l.CountryCode, "+")
	}
	if l.DisplayFormat != nil {
		format = strings.TrimSpace(*l.DisplayFormat)
	}
	validName := listUsernameRe.MatchString(value)
	if kind == "username" && !validName && len(value) <= 32 && premiumUsernameRe.MatchString(value) {
		validName = administrative
		if !administrative {
			it, found, err := itemBy(ctx, s.db, "kind=$1 AND lower(title)=$2", kind, "@"+value)
			if err != nil {
				return nil, err
			}
			validName = found && it.Owner == actorID
		}
	}
	if kind != "username" && kind != "number" || kind == "username" && !validName ||
		kind == "number" && !listNumberRe.MatchString(value) || units.Sign() < 1 {
		return nil, fail(400, "Choose a valid username or number and a positive GRAM price.")
	}
	if kind == "username" && (value == "announcements" || value == "instance_announcements") {
		return nil, fail(409, "That username is reserved for announcements.")
	}
	if kind == "number" && code != "" && (!codeRe.MatchString(code) || !strings.HasPrefix(value, code) || len(code) >= len(value)) {
		return nil, fail(400, "The country code must be a prefix of the full number.")
	}
	if kind == "number" && format != "" && (len(format) > 256 || !formatRe.MatchString(format) || nonDigitRe.ReplaceAllString(format, "") != value) {
		return nil, fail(400, "Formatting must contain exactly the digits of the number.")
	}
	replacement := trimMarker(strings.TrimSpace(l.ChannelUsername))
	id := kind + ":" + value
	title := prefix(kind) + value
	var renamed int64
	err = s.tx(ctx, func(tx pgx.Tx) error {
		it, found, err := itemBy(ctx, tx, "kind=$1 AND lower(title)=$2 FOR UPDATE", kind, title)
		if err != nil {
			return err
		}
		if found {
			id = it.ID
		}
		cur, ok, err := s.person(ctx, tx, actorID, true)
		if err != nil {
			return err
		}
		if !ok {
			return fail(403, "Account unavailable.")
		}
		lookup := s.lookup(value)
		primary := kind == "username" && cur.Username == value || kind == "number" && cur.Lookup != "" && cur.Lookup == lookup
		var retained int64
		if kind == "number" {
			if retained, err = retainedBy(ctx, tx, lookup); err != nil {
				return err
			}
		}
		ownerID, ownerIsUser, err := holder(ctx, tx, kind, value, lookup)
		if err != nil {
			return err
		}
		ownChannel := false
		if ownerID != 0 && !ownerIsUser && !administrative {
			if ownChannel, err = s.ownChannelHolds(ctx, tx, value, actorID); err != nil {
				return err
			}
		}
		if ownerID != 0 && !ownChannel && (administrative || !ownerIsUser || ownerID != actorID) {
			return fail(409, "This identifier is in use by another account or channel.")
		}
		if replacement != "" && !ownChannel {
			return fail(400, "Only a channel's username can be replaced when it goes up for sale.")
		}
		if kind == "username" && reserved[value] {
			return fail(409, "This username is reserved.")
		}
		if retained != 0 && (administrative || retained != actorID) {
			return fail(409, "This number is retained by another account.")
		}
		if primary {
			if err := s.swapPrimary(ctx, tx, cur, actor, kind, title, l.AlternativeID, verified, sessionHash); err != nil {
				return err
			}
		}
		if kind == "number" {
			if _, err := tx.Exec(ctx, "DELETE FROM phone_aliases WHERE lookup=$1 AND user_id=$2", lookup, actorID); err != nil {
				return err
			}
		}
		if retained == actorID {
			if _, err := tx.Exec(ctx, "UPDATE fragment_assignments SET previous_phone=NULL, previous_lookup=NULL WHERE user_id=$1", actorID); err != nil {
				return err
			}
		}
		if administrative && found && it.Owner != 0 {
			return fail(409, "This identifier is already owned.")
		}
		// with no listing yet a user may only put up what they sign in with
		// (or a number they retained). The holder check alone lets anyone
		// claim a name nobody holds.
		if !administrative && (found && it.Owner != actorID || !found && !primary && !ownChannel && retained != actorID) {
			return fail(403, "You can only list collectibles you own.")
		}
		if replacement != "" {
			if renamed, err = s.renameChannel(ctx, tx, actorID, value, replacement); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE fragment_quotes SET expires_at=0 WHERE item_id=$1 AND result IS NULL", id); err != nil {
			return err
		}
		if !found {
			var owner *int64
			if !administrative {
				owner = &actorID
			}
			_, err = tx.Exec(ctx, "INSERT INTO fragment_items(id, kind, title, units, owner_id) VALUES ($1, $2, $3, $4::numeric, $5)", id, kind, title, units.String(), owner)
		} else {
			_, err = tx.Exec(ctx, "UPDATE fragment_items SET units=$2::numeric, listed=true, active=false WHERE id=$1", id, units.String())
		}
		if err != nil {
			return err
		}
		if kind == "number" {
			c, f := it.CountryCode, it.DisplayFormat
			if l.CountryCode != nil {
				c = code
			}
			if l.DisplayFormat != nil {
				f = format
			}
			if _, err := tx.Exec(ctx, "UPDATE fragment_items SET country_code=$2, display_format=$3 WHERE id=$1", id, c, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if renamed != 0 && s.ChannelReleased != nil {
		s.ChannelReleased(renamed)
	}
	return map[string]any{"ok": true, "itemId": id}, nil
}

// renameChannel moves the channel off the name going up for sale, onto one
// picked by the same rule the channel's own settings use. The old name stays
// out of reach because the listing is in the same transaction. Returns the
// channel.
func (s *Service) renameChannel(ctx context.Context, tx pgx.Tx, creator int64, name, next string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT c.id FROM channels c JOIN channel_members m ON m.channel_id = c.id
		WHERE lower(c.username)=$1 AND NOT c.deleted AND m.user_id=$2 AND m.status='creator' FOR UPDATE OF c`, name, creator).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fail(409, "This identifier is in use by another account or channel.")
	}
	if err != nil {
		return 0, err
	}
	lower := strings.ToLower(next)
	if lower == name {
		return 0, fail(400, "Choose a different username for the channel.")
	}
	if reserved[lower] || lower == "announcements" || lower == "instance_announcements" {
		return 0, fail(409, "That username is reserved.")
	}
	free, err := channels.UsernameFreeInTx(ctx, tx, creator, next, id)
	if tgerr.Is(err, "USERNAME_INVALID") {
		return 0, fail(400, "Usernames are 4 to 32 letters, digits or underscores and start with a letter.")
	}
	if err != nil {
		return 0, err
	}
	if !free {
		return 0, fail(409, "That username is taken.")
	}
	if _, err := tx.Exec(ctx, "UPDATE channels SET username=$2 WHERE id=$1", id, next); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return 0, fail(409, "That username is taken.")
		}
		return 0, err
	}
	return id, nil
}

// swapPrimary moves the account onto another owned identifier before its
// current one goes up for sale.
func (s *Service) swapPrimary(ctx context.Context, tx pgx.Tx, cur, actor person, kind, title, alternativeID string, verified bool, sessionHash string) error {
	if !verified || cur.Password != actor.Password {
		return fail(400, "Confirm your password and choose an owned replacement before selling a primary identifier.")
	}
	assets, err := s.owned(ctx, tx, cur)
	if err != nil {
		return err
	}
	var alt *asset
	for i := range assets {
		if a := assets[i]; a.ID == alternativeID && a.Kind == kind && !a.Listed && a.Title != title && a.channel == 0 {
			alt = &assets[i]
			break
		}
	}
	if alt == nil {
		return fail(400, "Choose another owned, unlisted identifier as the replacement.")
	}
	if kind == "username" {
		_, err := tx.Exec(ctx, "UPDATE users SET username=$2 WHERE id=$1", cur.ID, alt.Title[1:])
		return err
	}
	for _, a := range assets {
		if a.Kind == "number" && !a.Listed {
			if err := s.remember(ctx, tx, cur.ID, a.Title); err != nil {
				return err
			}
		}
	}
	enc, err := s.key.EncryptPhone(alt.Title, "user:"+strconv.FormatInt(cur.ID, 10))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET phone=$2, phone_lookup=$3 WHERE id=$1", cur.ID, enc, s.lookup(alt.Title)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM fragment_assignments WHERE user_id=$1", cur.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", cur.ID, sessionHash)
	return err
}

func retainedBy(ctx context.Context, tx pgx.Tx, lookup string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM phone_aliases WHERE lookup=$1
		UNION ALL SELECT user_id FROM fragment_assignments WHERE previous_lookup=$1 LIMIT 1`, lookup).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// holder is who uses the identifier right now: a user (isUser) or channel.
func holder(ctx context.Context, tx pgx.Tx, kind, value, lookup string) (int64, bool, error) {
	var id int64
	var isUser bool
	var err error
	if kind == "username" {
		err = tx.QueryRow(ctx, `SELECT id, true FROM users WHERE lower(username)=$1
			UNION ALL SELECT id, false FROM channels WHERE lower(username)=$1 AND NOT deleted LIMIT 1`, value).Scan(&id, &isUser)
	} else {
		err = tx.QueryRow(ctx, "SELECT id, true FROM users WHERE phone_lookup=$1", lookup).Scan(&id, &isUser)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, isUser, err
}

// Cancel takes a listing off sale. remove (admin, never sold) deletes it. A
// channel's name that nobody ever bought goes back to being just the channel's:
// kept as an item it would read as the seller's own collectible.
func (s *Service) Cancel(ctx context.Context, actorID int64, itemID string, administrative, remove bool) (map[string]any, error) {
	var owner int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		it, found, err := itemBy(ctx, tx, "id=$1 FOR UPDATE", itemID)
		if err != nil {
			return err
		}
		if !found || it.Kind == "gift" {
			return fail(404, "Listing not found.")
		}
		if !administrative && it.Owner != actorID {
			return fail(403, "You cannot cancel this listing.")
		}
		owner = it.Owner
		if administrative && remove && it.Owner == 0 {
			if _, err := tx.Exec(ctx, "DELETE FROM fragment_quotes WHERE item_id=$1 AND result IS NULL", it.ID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "DELETE FROM fragment_items WHERE id=$1 AND owner_id IS NULL", it.ID)
			return err
		}
		if it.Kind == "username" && it.Owner != 0 && it.PurchasedAt == 0 {
			var held bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE lower(username)=lower($1) AND NOT deleted)", trimMarker(it.Title)).Scan(&held); err != nil {
				return err
			}
			if held {
				_, err := tx.Exec(ctx, "DELETE FROM fragment_items WHERE id=$1", it.ID)
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE fragment_items SET listed=false WHERE id=$1", it.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE fragment_quotes SET expires_at=0 WHERE item_id=$1 AND result IS NULL", it.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if owner != 0 {
		s.usernamesChanged(owner)
	}
	return map[string]any{"ok": true}, nil
}
