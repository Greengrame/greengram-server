package account

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Birthday is day/month and maybe a year; zero Day means none.
type Birthday struct {
	Day, Month, Year int
}

// Prefs are the per-account settings in account_prefs.
type Prefs struct {
	Birthday             Birthday
	AccountTTLDays       int
	AuthorizationTTLDays int
	SensitiveEnabled     bool
	ContactSignupSilent  bool
	ArchiveNewPeers      bool
	KeepArchivedUnmuted  bool
	KeepArchivedFolders  bool
	HideReadMarks        bool
	NewPeersNeedPremium  bool
	DisplayGiftsButton   bool
	NoncontactPaidStars  int64
}

func DefaultPrefs() Prefs { return Prefs{AccountTTLDays: 365, AuthorizationTTLDays: 180} }

const prefCols = `user_id, coalesce(birthday_day, 0), coalesce(birthday_month, 0), coalesce(birthday_year, 0), account_ttl_days,
	authorization_ttl_days, sensitive_enabled, contact_signup_silent, archive_new_peers, keep_archived_unmuted,
	keep_archived_folders, hide_read_marks, new_peers_require_premium, display_gifts_button, noncontact_paid_stars`

func scanPrefs(row pgx.Row) (int64, Prefs, error) {
	var id int64
	var p Prefs
	err := row.Scan(&id, &p.Birthday.Day, &p.Birthday.Month, &p.Birthday.Year, &p.AccountTTLDays, &p.AuthorizationTTLDays,
		&p.SensitiveEnabled, &p.ContactSignupSilent, &p.ArchiveNewPeers, &p.KeepArchivedUnmuted, &p.KeepArchivedFolders,
		&p.HideReadMarks, &p.NewPeersNeedPremium, &p.DisplayGiftsButton, &p.NoncontactPaidStars)
	return id, p, err
}

func (s *Service) Prefs(ctx context.Context, user int64) (Prefs, error) {
	_, p, err := scanPrefs(s.db.QueryRow(ctx, "SELECT "+prefCols+" FROM account_prefs WHERE user_id=$1", user))
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPrefs(), nil
	}
	return p, err
}

// PrefsMany is Prefs for several accounts; ones with no row get defaults.
func (s *Service) PrefsMany(ctx context.Context, users []int64) (map[int64]Prefs, error) {
	out := make(map[int64]Prefs, len(users))
	for _, id := range users {
		out[id] = DefaultPrefs()
	}
	if len(users) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT "+prefCols+" FROM account_prefs WHERE user_id = ANY($1)", users)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		id, p, err := scanPrefs(rows)
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// setPref writes one column; col comes from this package, never a client.
func (s *Service) setPref(ctx context.Context, user int64, col string, v any) error {
	_, err := s.db.Exec(ctx, "INSERT INTO account_prefs(user_id, "+col+") VALUES ($1, $2) ON CONFLICT (user_id) DO UPDATE SET "+col+" = excluded."+col, user, v)
	return err
}

func (s *Service) SetDefaultReaction(ctx context.Context, user int64, key string) error {
	return s.setPref(ctx, user, "default_reaction", key)
}

// DefaultReaction is "" until the user picks one.
func (s *Service) DefaultReaction(ctx context.Context, user int64) (string, error) {
	var key *string
	err := s.db.QueryRow(ctx, "SELECT default_reaction FROM account_prefs WHERE user_id=$1", user).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) || key == nil {
		return "", nil
	}
	return *key, err
}

func (s *Service) SetAccountTTL(ctx context.Context, user int64, days int) error {
	if days < 30 || days > 730 {
		return fail(400, "TTL_DAYS_INVALID")
	}
	return s.setPref(ctx, user, "account_ttl_days", days)
}

// SetAuthorizationTTL bounds how long an idle sign-in lives; the instance's
// session length still caps it (see sessionByHash).
func (s *Service) SetAuthorizationTTL(ctx context.Context, user int64, days int) error {
	if days < 1 || days > 366 {
		return fail(400, "TTL_DAYS_INVALID")
	}
	if err := s.setPref(ctx, user, "authorization_ttl_days", days); err != nil {
		return err
	}
	// sessions idle longer than the new limit end now, not on their next touch
	_, err := s.db.Exec(ctx, "UPDATE sessions SET expires_at = least(expires_at, last_active + make_interval(days => $2)) WHERE user_id=$1", user, days)
	return err
}

func (s *Service) SetSensitive(ctx context.Context, user int64, on bool) error {
	return s.setPref(ctx, user, "sensitive_enabled", on)
}

func (s *Service) SetContactSignupSilent(ctx context.Context, user int64, silent bool) error {
	return s.setPref(ctx, user, "contact_signup_silent", silent)
}

// GlobalPrivacy is account.setGlobalPrivacySettings' part of Prefs.
func (s *Service) SetGlobalPrivacy(ctx context.Context, user int64, p Prefs) error {
	if p.NoncontactPaidStars < 0 || p.NoncontactPaidStars > 10000 {
		return fail(400, "STARS_AMOUNT_INVALID")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO account_prefs(user_id, archive_new_peers, keep_archived_unmuted, keep_archived_folders,
			hide_read_marks, new_peers_require_premium, display_gifts_button, noncontact_paid_stars)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id) DO UPDATE SET archive_new_peers = excluded.archive_new_peers,
			keep_archived_unmuted = excluded.keep_archived_unmuted, keep_archived_folders = excluded.keep_archived_folders,
			hide_read_marks = excluded.hide_read_marks, new_peers_require_premium = excluded.new_peers_require_premium,
			display_gifts_button = excluded.display_gifts_button, noncontact_paid_stars = excluded.noncontact_paid_stars`,
		user, p.ArchiveNewPeers, p.KeepArchivedUnmuted, p.KeepArchivedFolders, p.HideReadMarks, p.NewPeersNeedPremium,
		p.DisplayGiftsButton, p.NoncontactPaidStars)
	return err
}

// SetBirthday stores b; a zero Day removes it.
func (s *Service) SetBirthday(ctx context.Context, user int64, b Birthday) error {
	if b.Day == 0 {
		_, err := s.db.Exec(ctx, "UPDATE account_prefs SET birthday_day=NULL, birthday_month=NULL, birthday_year=NULL WHERE user_id=$1", user)
		return err
	}
	if !validBirthday(b, s.now()) {
		return fail(400, "BIRTHDAY_INVALID")
	}
	var year *int
	if b.Year != 0 {
		year = &b.Year
	}
	_, err := s.db.Exec(ctx, `INSERT INTO account_prefs(user_id, birthday_day, birthday_month, birthday_year) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET birthday_day = excluded.birthday_day, birthday_month = excluded.birthday_month,
			birthday_year = excluded.birthday_year`, user, b.Day, b.Month, year)
	return err
}

func validBirthday(b Birthday, now time.Time) bool {
	if b.Month < 1 || b.Month > 12 || b.Day < 1 {
		return false
	}
	year := b.Year
	if year == 0 {
		year = 2000 // leap, so 29 Feb is fine without a year
	} else if year < 1900 || year > now.Year() {
		return false
	}
	t := time.Date(year, time.Month(b.Month), b.Day, 0, 0, 0, 0, time.UTC)
	return t.Day() == b.Day && (b.Year == 0 || !t.After(now))
}

// BirthdaysAround lists which of users have a birthday within a day of
// now, either way, as Telegram's contacts.getBirthdays does.
func (s *Service) BirthdaysAround(ctx context.Context, users []int64, now time.Time) (map[int64]Birthday, error) {
	out := map[int64]Birthday{}
	if len(users) == 0 {
		return out, nil
	}
	var md [][2]int
	for d := -1; d <= 1; d++ {
		t := now.AddDate(0, 0, d)
		md = append(md, [2]int{int(t.Month()), t.Day()})
	}
	rows, err := s.db.Query(ctx, `SELECT user_id, birthday_day, birthday_month, coalesce(birthday_year, 0) FROM account_prefs
		WHERE user_id = ANY($1) AND ((birthday_month=$2 AND birthday_day=$3) OR (birthday_month=$4 AND birthday_day=$5)
			OR (birthday_month=$6 AND birthday_day=$7))`,
		users, md[0][0], md[0][1], md[1][0], md[1][1], md[2][0], md[2][1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var b Birthday
		if err := rows.Scan(&id, &b.Day, &b.Month, &b.Year); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// Profile limits are Telegram's; bios get 140 with premium.
const (
	maxNameLen = 64
	maxBio     = 70
	maxBioPrem = 140
)

// UpdateProfile changes whichever of first/last name and bio are non-nil.
// Greengram keeps one name; Telegram's first and last are joined with a space.
func (s *Service) UpdateProfile(ctx context.Context, u User, first, last, bio *string) (User, error) {
	curFirst, curLast, _ := strings.Cut(strings.TrimSpace(u.Name), " ")
	if first != nil {
		curFirst = strings.TrimSpace(*first)
	}
	if last != nil {
		curLast = strings.TrimSpace(*last)
	}
	if first != nil || last != nil {
		if curFirst == "" {
			return u, fail(400, "FIRSTNAME_INVALID")
		}
		if utf8.RuneCountInString(curFirst) > maxNameLen {
			return u, fail(400, "FIRSTNAME_INVALID")
		}
		if utf8.RuneCountInString(curLast) > maxNameLen {
			return u, fail(400, "LASTNAME_INVALID")
		}
		u.Name = strings.TrimSpace(curFirst + " " + curLast)
	}
	if bio != nil {
		limit := maxBio
		if u.Premium(s.now()) {
			limit = maxBioPrem
		}
		v := strings.TrimSpace(*bio)
		if utf8.RuneCountInString(v) > limit {
			return u, fail(400, "ABOUT_TOO_LONG")
		}
		u.Bio = v
	}
	_, err := s.db.Exec(ctx, "UPDATE users SET name=$2, bio=$3 WHERE id=$1", u.ID, u.Name, u.Bio)
	return u, err
}

// CheckUsername says whether a person could take name: the sign-up rules,
// or a collectible of theirs that isn't up for sale, whatever its shape.
func (s *Service) CheckUsername(ctx context.Context, user int64, name string) (bool, error) {
	name = normUsername(name)
	owned, err := s.ownsCollectible(ctx, user, name)
	if err != nil {
		return false, err
	}
	if owned {
		var taken bool
		err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1 AND NOT deleted)", name).Scan(&taken)
		return !taken, err
	}
	switch {
	case !usernameRe.MatchString(name):
		return false, fail(400, "USERNAME_INVALID")
	case reservedUsernames[name]:
		return false, nil
	}
	var taken bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1 AND id<>$2)
		OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1)
		OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1)`, name, user).Scan(&taken)
	return !taken, err
}

// SetUsername is account.updateUsername. The username is also what people
// sign in with here, so "" can't drop it: it only hides it from others,
// which is what Telegram's empty username looks like to them.
func (s *Service) SetUsername(ctx context.Context, u User, name string) (User, error) {
	if strings.TrimSpace(name) == "" {
		_, err := s.db.Exec(ctx, "UPDATE users SET username_public=false WHERE id=$1", u.ID)
		u.UsernamePublic = false
		return u, err
	}
	name = normUsername(name)
	if name == strings.ToLower(u.Username) {
		if !u.UsernamePublic {
			_, err := s.db.Exec(ctx, "UPDATE users SET username_public=true WHERE id=$1", u.ID)
			u.UsernamePublic = true
			return u, err
		}
		return u, fail(400, "USERNAME_NOT_MODIFIED")
	}
	free, err := s.CheckUsername(ctx, u.ID, name)
	if err != nil {
		return u, err
	}
	if !free {
		return u, fail(400, "USERNAME_OCCUPIED")
	}
	// the unique index catches a race with another taker
	if _, err := s.db.Exec(ctx, "UPDATE users SET username=$2, username_public=true WHERE id=$1", u.ID, name); err != nil {
		if strings.Contains(err.Error(), "users_username") {
			return u, fail(400, "USERNAME_OCCUPIED")
		}
		return u, err
	}
	// the old name may be a collectible that now becomes an alias
	fresh, _, err := s.User(ctx, u.ID)
	return fresh, err
}

// SetAuthorizationSettings is account.changeAuthorizationSettings for one
// of user's sign-ins.
func (s *Service) SetAuthorizationSettings(ctx context.Context, user int64, hash string, calls, secret *bool) error {
	_, err := s.db.Exec(ctx, `UPDATE sessions SET calls_disabled = coalesce($3, calls_disabled),
		secret_chats_disabled = coalesce($4, secret_chats_disabled) WHERE user_id=$1 AND token_hash=$2`, user, hash, calls, secret)
	return err
}
