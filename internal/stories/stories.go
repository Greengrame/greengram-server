// Package stories is users' stories: posting, who may see each one, the
// feed, read marks, views and reactions, close friends and the lists that
// hide stories one way or the other.
package stories

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/media"
)

type Service struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) unix() int { return int(s.now().Unix()) }

var (
	ErrStoryInvalid = tgerr.New(400, "STORY_ID_INVALID")
	errDuplicate    = tgerr.New(400, "RANDOM_ID_DUPLICATE")
	errTooMany      = tgerr.New(400, "STORIES_TOO_MUCH")
)

// MaxActive is how many live stories one account can have at once.
const MaxActive = 100

// MaxPinnedTop is stories_pinned_to_top_count_max.
const MaxPinnedTop = 3

// Audiences, as stored in stories.audience.
const (
	Everybody    = "everybody"
	Contacts     = "contacts"
	CloseFriends = "close_friends"
	Selected     = "selected"
)

// Rules is who a story is for: the audience plus exceptions both ways.
type Rules struct {
	Audience   string
	AllowUsers []int64
	DenyUsers  []int64
	AllowChats []int64
	DenyChats  []int64
}

type Story struct {
	Owner      int64
	ID         int
	RandomID   int64
	Date       int
	Expire     int
	Pinned     bool
	PinnedTop  int
	Noforwards bool
	EditDate   int
	Caption    string
	Entities   []tg.MessageEntityClass
	Media      media.Ref
	Rules      Rules
}

func (st Story) Active(now int) bool { return st.Expire > now }

const cols = "owner_id, id, random_id, date, expire_date, pinned, pinned_top, noforwards, edit_date, caption, entities, media, media_kind, media_id, " +
	"audience, allow_users, deny_users, allow_chats, deny_chats"

// sCols is cols for a query that names the table s.
var sCols = "s." + strings.ReplaceAll(cols, ", ", ", s.")

func scan(row pgx.Row) (Story, error) {
	var (
		st      Story
		ent     []byte
		raw     []byte
		kind    *string
		mediaID *int64
	)
	err := row.Scan(&st.Owner, &st.ID, &st.RandomID, &st.Date, &st.Expire, &st.Pinned, &st.PinnedTop, &st.Noforwards, &st.EditDate,
		&st.Caption, &ent, &raw, &kind, &mediaID, &st.Rules.Audience, &st.Rules.AllowUsers, &st.Rules.DenyUsers, &st.Rules.AllowChats, &st.Rules.DenyChats)
	if err != nil {
		return st, err
	}
	if st.Entities, err = decodeEntities(ent); err != nil {
		return st, err
	}
	st.Media, err = media.Load(raw, kind, mediaID)
	return st, err
}

func collect(rows pgx.Rows, err error) ([]Story, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Story
	for rows.Next() {
		st, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// inChat is channels' inChatSQL: member statuses that count as being in.
const inChat = "('creator', 'admin', 'member', 'restricted')"

// visible is the condition "viewer may see story s", viewer being any SQL
// expression. Blocking the viewer, either way of hiding stories from them
// and a deny exception beat everything; then an allow exception or the
// audience lets them in. The owner always sees their own.
func visible(viewer string) string {
	return strings.ReplaceAll(`(s.owner_id = {v} OR (
		NOT EXISTS (SELECT 1 FROM blocked_users b WHERE b.blocker_id = s.owner_id AND b.blocked_id = {v})
		AND NOT EXISTS (SELECT 1 FROM stories_blocked sb WHERE sb.owner_id = s.owner_id AND sb.blocked_id = {v})
		AND NOT {v} = ANY(s.deny_users)
		AND NOT EXISTS (SELECT 1 FROM channel_members dm WHERE dm.user_id = {v} AND dm.channel_id = ANY(s.deny_chats) AND dm.status IN `+inChat+`)
		AND ({v} = ANY(s.allow_users)
			OR EXISTS (SELECT 1 FROM channel_members am WHERE am.user_id = {v} AND am.channel_id = ANY(s.allow_chats) AND am.status IN `+inChat+`)
			OR s.audience = 'everybody'
			OR (s.audience = 'contacts' AND EXISTS (SELECT 1 FROM contacts c WHERE c.owner_id = s.owner_id AND c.user_id = {v}))
			OR (s.audience = 'close_friends' AND EXISTS (SELECT 1 FROM close_friends f WHERE f.owner_id = s.owner_id AND f.friend_id = {v})))))`, "{v}", viewer)
}

// Draft is a story to post. Period is in seconds.
type Draft struct {
	RandomID   int64
	Period     int
	Pinned     bool
	Noforwards bool
	Caption    string
	Entities   []tg.MessageEntityClass
	Media      media.Ref
	Rules      Rules
}

// Post stores a story under the owner's next id.
func (s *Service) Post(ctx context.Context, owner int64, n Draft) (Story, error) {
	ent, err := encodeEntities(n.Entities)
	if err != nil {
		return Story{}, err
	}
	raw, err := n.Media.Encode()
	if err != nil {
		return Story{}, err
	}
	now := s.unix()
	var st Story
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id int
		// the counter row is the lock too: two posts at once queue here
		if err := tx.QueryRow(ctx, `INSERT INTO story_owners(owner_id, last_id) VALUES ($1, 1)
			ON CONFLICT (owner_id) DO UPDATE SET last_id = story_owners.last_id + 1 RETURNING last_id`, owner).Scan(&id); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM stories WHERE owner_id=$1 AND expire_date > $2", owner, now).Scan(&live); err != nil {
			return err
		}
		if live >= MaxActive {
			return errTooMany
		}
		r := normRules(n.Rules)
		st, err = scan(tx.QueryRow(ctx, `INSERT INTO stories(owner_id, id, random_id, date, expire_date, pinned, noforwards, caption, entities,
			media, media_kind, media_id, audience, allow_users, deny_users, allow_chats, deny_chats)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, ''), nullif($12, 0::bigint), $13, $14, $15, $16, $17) RETURNING `+cols,
			owner, id, n.RandomID, now, now+n.Period, n.Pinned, n.Noforwards, n.Caption, ent, raw, n.Media.Kind, n.Media.ID,
			r.Audience, r.AllowUsers, r.DenyUsers, r.AllowChats, r.DenyChats))
		return err
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Story{}, errDuplicate
	}
	return st, err
}

func normRules(r Rules) Rules {
	fix := func(l []int64) []int64 {
		if l == nil {
			return []int64{}
		}
		return l
	}
	r.AllowUsers, r.DenyUsers, r.AllowChats, r.DenyChats = fix(r.AllowUsers), fix(r.DenyUsers), fix(r.AllowChats), fix(r.DenyChats)
	if r.Audience == "" {
		r.Audience = Selected
	}
	return r
}

// Active are the live stories of each owner that viewer may see, oldest
// first, the order the apps play them in.
func (s *Service) Active(ctx context.Context, viewer int64, owners []int64) (map[int64][]Story, error) {
	out := map[int64][]Story{}
	if len(owners) == 0 {
		return out, nil
	}
	list, err := collect(s.db.Query(ctx, "SELECT "+sCols+" FROM stories s WHERE s.owner_id = ANY($2) AND s.expire_date > $3 AND "+visible("$1")+
		" ORDER BY s.owner_id, s.id", viewer, owners, s.unix()))
	for _, st := range list {
		out[st.Owner] = append(out[st.Owner], st)
	}
	return out, err
}

// ByID is getStoriesByID: live or pinned stories viewer may see, and any
// of their own.
func (s *Service) ByID(ctx context.Context, viewer, owner int64, ids []int) ([]Story, error) {
	return collect(s.db.Query(ctx, "SELECT "+sCols+" FROM stories s WHERE s.owner_id=$2 AND s.id = ANY($3) AND (s.expire_date > $4 OR s.pinned OR s.owner_id = $1) AND "+
		visible("$1")+" ORDER BY s.id", viewer, owner, ids, s.unix()))
}

// One is a single story as ByID sees it.
func (s *Service) One(ctx context.Context, viewer, owner int64, id int) (Story, error) {
	list, err := s.ByID(ctx, viewer, owner, []int{id})
	if err != nil {
		return Story{}, err
	}
	if len(list) == 0 {
		return Story{}, ErrStoryInvalid
	}
	return list[0], nil
}

// Pinned is one page of owner's profile stories, newest first, the total
// and the ids pinned to the top, in their order.
func (s *Service) Pinned(ctx context.Context, viewer, owner int64, offsetID, limit int) ([]Story, int, []int, error) {
	limit = clamp(limit)
	cond := " FROM stories s WHERE s.owner_id=$2 AND s.pinned AND " + visible("$1")
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+cond, viewer, owner).Scan(&total); err != nil {
		return nil, 0, nil, err
	}
	list, err := collect(s.db.Query(ctx, "SELECT "+sCols+cond+" AND ($3 = 0 OR s.id < $3) ORDER BY s.id DESC LIMIT $4", viewer, owner, offsetID, limit))
	if err != nil {
		return nil, 0, nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT s.id"+cond+" AND s.pinned_top > 0 ORDER BY s.pinned_top", viewer, owner)
	if err != nil {
		return nil, 0, nil, err
	}
	top, err := pgx.CollectRows(rows, pgx.RowTo[int])
	return list, total, top, err
}

// Archive is every story owner ever posted and kept, newest first.
func (s *Service) Archive(ctx context.Context, owner int64, offsetID, limit int) ([]Story, int, error) {
	limit = clamp(limit)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM stories WHERE owner_id=$1", owner).Scan(&total); err != nil {
		return nil, 0, err
	}
	list, err := collect(s.db.Query(ctx, "SELECT "+cols+" FROM stories WHERE owner_id=$1 AND ($2 = 0 OR id < $2) ORDER BY id DESC LIMIT $3", owner, offsetID, limit))
	return list, total, err
}

func clamp(limit int) int {
	if limit <= 0 || limit > 100 {
		return 100
	}
	return limit
}

// Delete removes owner's stories and returns what they were.
func (s *Service) Delete(ctx context.Context, owner int64, ids []int) ([]Story, error) {
	return collect(s.db.Query(ctx, "DELETE FROM stories WHERE owner_id=$1 AND id = ANY($2) RETURNING "+cols, owner, ids))
}

// SetPinned puts stories on the profile or takes them off; unpinning also
// drops them from the top.
func (s *Service) SetPinned(ctx context.Context, owner int64, ids []int, pinned bool) ([]Story, error) {
	return collect(s.db.Query(ctx, `UPDATE stories SET pinned=$3, pinned_top = CASE WHEN $3 THEN pinned_top ELSE 0 END
		WHERE owner_id=$1 AND id = ANY($2) RETURNING `+cols, owner, ids, pinned))
}

// SetPinnedTop replaces the pinned-to-top list with ids, in that order.
// Only pinned stories can be on it.
func (s *Service) SetPinnedTop(ctx context.Context, owner int64, ids []int) error {
	if len(ids) > MaxPinnedTop {
		return tgerr.New(400, "STORY_ID_TOO_MANY")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE stories SET pinned_top=0 WHERE owner_id=$1 AND pinned_top > 0", owner); err != nil {
			return err
		}
		for i, id := range ids {
			tag, err := tx.Exec(ctx, "UPDATE stories SET pinned_top=$3 WHERE owner_id=$1 AND id=$2 AND pinned", owner, id, i+1)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrStoryInvalid
			}
		}
		return nil
	})
}

// Edit is editStory; nil fields stay as they are.
type Edit struct {
	Media    *media.Ref
	Caption  *string
	Entities []tg.MessageEntityClass
	Rules    *Rules
}

// Edit changes one of owner's stories and returns it before and after.
// New media or caption marks it edited; new rules alone don't.
func (s *Service) Edit(ctx context.Context, owner int64, id int, e Edit) (before, after Story, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		before, err = scan(tx.QueryRow(ctx, "SELECT "+cols+" FROM stories WHERE owner_id=$1 AND id=$2 FOR UPDATE", owner, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStoryInvalid
		}
		if err != nil {
			return err
		}
		next := before
		if e.Media != nil {
			next.Media = *e.Media
		}
		if e.Caption != nil {
			next.Caption, next.Entities = *e.Caption, e.Entities
		}
		if e.Media != nil || e.Caption != nil {
			next.EditDate = s.unix()
		}
		if e.Rules != nil {
			next.Rules = normRules(*e.Rules)
		}
		ent, err := encodeEntities(next.Entities)
		if err != nil {
			return err
		}
		raw, err := next.Media.Encode()
		if err != nil {
			return err
		}
		r := next.Rules
		after, err = scan(tx.QueryRow(ctx, `UPDATE stories SET media=$3, media_kind=nullif($4, ''), media_id=nullif($5, 0::bigint), caption=$6, entities=$7,
			edit_date=$8, audience=$9, allow_users=$10, deny_users=$11, allow_chats=$12, deny_chats=$13 WHERE owner_id=$1 AND id=$2 RETURNING `+cols,
			owner, id, raw, next.Media.Kind, next.Media.ID, next.Caption, ent, next.EditDate, r.Audience, r.AllowUsers, r.DenyUsers, r.AllowChats, r.DenyChats))
		return err
	})
	return before, after, err
}

// Watchers are who would have the story in their feed: people with the
// owner in their contacts that may see it. Pushes go to them.
func (s *Service) Watchers(ctx context.Context, st Story) ([]int64, error) {
	// the story may be gone already (deleted), so its rules come as
	// parameters instead of from the row
	rows, err := s.db.Query(ctx, `SELECT c.owner_id FROM contacts c,
		(SELECT $1::bigint AS owner_id, $2::text AS audience, $3::bigint[] AS allow_users, $4::bigint[] AS deny_users,
			$5::bigint[] AS allow_chats, $6::bigint[] AS deny_chats) s
		WHERE c.user_id = $1 AND c.owner_id <> $1 AND `+visible("c.owner_id"),
		st.Owner, st.Rules.Audience, nz(st.Rules.AllowUsers), nz(st.Rules.DenyUsers), nz(st.Rules.AllowChats), nz(st.Rules.DenyChats))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func nz(l []int64) []int64 {
	if l == nil {
		return []int64{}
	}
	return l
}

// FeedPeer is one owner in someone's feed.
type FeedPeer struct {
	Owner    int64
	MaxID    int
	LastDate int
}

// Feed is whose live stories viewer gets in getAllStories: their own and
// their contacts', the hidden ones or the rest. Newest first.
func (s *Service) Feed(ctx context.Context, viewer int64, hidden bool) ([]FeedPeer, error) {
	rows, err := s.db.Query(ctx, `SELECT s.owner_id, max(s.id), max(s.date) FROM stories s
		WHERE s.expire_date > $2
			AND (s.owner_id = $1 AND NOT $3 OR s.owner_id IN (SELECT user_id FROM contacts WHERE owner_id = $1))
			AND EXISTS (SELECT 1 FROM stories_hidden h WHERE h.viewer_id = $1 AND h.owner_id = s.owner_id) = $3
			AND `+visible("$1")+`
		GROUP BY s.owner_id ORDER BY max(s.date) DESC, s.owner_id`, viewer, s.unix(), hidden)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FeedPeer, error) {
		var p FeedPeer
		err := r.Scan(&p.Owner, &p.MaxID, &p.LastDate)
		return p, err
	})
}

// Badge is what a user object says about the owner's stories to viewer.
type Badge struct {
	MaxID       int  // newest live story viewer may see
	CloseFriend bool // on viewer's close friends list
	Hidden      bool // viewer hid their stories
}

func (s *Service) Badges(ctx context.Context, viewer int64, owners []int64) (map[int64]Badge, error) {
	out := map[int64]Badge{}
	if len(owners) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT o.id,
		coalesce((SELECT max(s.id) FROM stories s WHERE s.owner_id = o.id AND s.expire_date > $3 AND `+visible("$1")+`), 0),
		EXISTS (SELECT 1 FROM close_friends f WHERE f.owner_id = $1 AND f.friend_id = o.id),
		EXISTS (SELECT 1 FROM stories_hidden h WHERE h.viewer_id = $1 AND h.owner_id = o.id)
		FROM unnest($2::bigint[]) AS o(id)`, viewer, owners, s.unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			b  Badge
		)
		if err := rows.Scan(&id, &b.MaxID, &b.CloseFriend, &b.Hidden); err != nil {
			return nil, err
		}
		if b != (Badge{}) {
			out[id] = b
		}
	}
	return out, rows.Err()
}

// ReadMarks are viewer's read marks for owners; nil owners means all.
func (s *Service) ReadMarks(ctx context.Context, viewer int64, owners []int64) (map[int64]int, error) {
	rows, err := s.db.Query(ctx, "SELECT owner_id, max_id FROM story_reads WHERE viewer_id=$1 AND ($2::bigint[] IS NULL OR owner_id = ANY($2))", viewer, owners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var (
			o  int64
			id int
		)
		if err := rows.Scan(&o, &id); err != nil {
			return nil, err
		}
		out[o] = id
	}
	return out, rows.Err()
}

// Read moves viewer's read mark for owner up to maxID and counts a view of
// that story: the apps call it once per story as it plays. It returns the
// live stories that became read.
func (s *Service) Read(ctx context.Context, viewer, owner int64, maxID int) ([]int, error) {
	now := s.unix()
	var ids []int
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var prev int
		err := tx.QueryRow(ctx, "SELECT max_id FROM story_reads WHERE viewer_id=$1 AND owner_id=$2 FOR UPDATE", viewer, owner).Scan(&prev)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT s.id FROM stories s WHERE s.owner_id=$2 AND s.id > $3 AND s.id <= $4 AND s.expire_date > $5 AND "+visible("$1")+" ORDER BY s.id",
			viewer, owner, prev, maxID, now)
		if err != nil {
			return err
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[int]); err != nil || len(ids) == 0 {
			return err
		}
		top := ids[len(ids)-1]
		if _, err := tx.Exec(ctx, `INSERT INTO story_reads(viewer_id, owner_id, max_id, date) VALUES ($1, $2, $3, $4)
			ON CONFLICT (viewer_id, owner_id) DO UPDATE SET max_id = greatest(story_reads.max_id, excluded.max_id), date = excluded.date`,
			viewer, owner, top, now); err != nil {
			return err
		}
		if viewer != owner {
			_, err = tx.Exec(ctx, "INSERT INTO story_views(owner_id, story_id, viewer_id, date) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING", owner, top, viewer, now)
		}
		return err
	})
	return ids, err
}

// View counts viewer as having seen owner's stories (pinned ones played
// from the profile, past their expiry).
func (s *Service) View(ctx context.Context, viewer, owner int64, ids []int) error {
	if viewer == owner {
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO story_views(owner_id, story_id, viewer_id, date)
		SELECT s.owner_id, s.id, $1, $4 FROM stories s WHERE s.owner_id=$2 AND s.id = ANY($3) AND (s.expire_date > $4 OR s.pinned) AND `+visible("$1")+`
		ON CONFLICT DO NOTHING`, viewer, owner, ids, s.unix())
	return err
}

// React sets viewer's reaction to a story (nil takes it back). Reacting
// counts as a view.
func (s *Service) React(ctx context.Context, viewer, owner int64, id int, reaction tg.ReactionClass) error {
	st, err := s.One(ctx, viewer, owner, id)
	if err != nil {
		return err
	}
	if !st.Active(s.unix()) && !st.Pinned {
		return ErrStoryInvalid
	}
	if viewer == owner {
		return tgerr.New(400, "PEER_ID_INVALID")
	}
	raw, err := encodeReaction(reaction)
	if err != nil {
		return err
	}
	now := s.unix()
	_, err = s.db.Exec(ctx, `INSERT INTO story_views(owner_id, story_id, viewer_id, date, reaction, reaction_date) VALUES ($1, $2, $3, $4, $5, $4)
		ON CONFLICT (owner_id, story_id, viewer_id) DO UPDATE SET reaction = excluded.reaction, reaction_date = excluded.reaction_date`,
		owner, id, viewer, now, raw)
	return err
}

// MyReactions are viewer's reactions to owner's stories, by story id.
func (s *Service) MyReactions(ctx context.Context, viewer, owner int64, ids []int) (map[int]tg.ReactionClass, error) {
	out := map[int]tg.ReactionClass{}
	if viewer == owner || len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT story_id, reaction FROM story_views WHERE owner_id=$1 AND viewer_id=$2 AND story_id = ANY($3) AND reaction IS NOT NULL",
		owner, viewer, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  int
			raw []byte
		)
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if r, err := decodeReaction(raw); err == nil && r != nil {
			out[id] = r
		}
	}
	return out, rows.Err()
}

// Counts are a story's numbers for its owner.
type Counts struct {
	Views      int
	Reactions  int
	ByReaction []ReactionCount // most used first
	Recent     []int64         // last three viewers
}

type ReactionCount struct {
	Reaction tg.ReactionClass
	Count    int
}

func (s *Service) Counts(ctx context.Context, owner int64, ids []int) (map[int]*Counts, error) {
	out := map[int]*Counts{}
	if len(ids) == 0 {
		return out, nil
	}
	get := func(id int) *Counts {
		c := out[id]
		if c == nil {
			c = &Counts{}
			out[id] = c
		}
		return c
	}
	rows, err := s.db.Query(ctx, `SELECT story_id, count(*), count(reaction) FROM story_views WHERE owner_id=$1 AND story_id = ANY($2) GROUP BY story_id`, owner, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, v, r int
		if err := rows.Scan(&id, &v, &r); err != nil {
			rows.Close()
			return nil, err
		}
		c := get(id)
		c.Views, c.Reactions = v, r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT story_id, reaction, count(*) FROM story_views WHERE owner_id=$1 AND story_id = ANY($2) AND reaction IS NOT NULL
		GROUP BY story_id, reaction ORDER BY count(*) DESC, max(reaction_date) DESC`, owner, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			id, n int
			raw   []byte
		)
		if err := rows.Scan(&id, &raw, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if r, err := decodeReaction(raw); err == nil && r != nil {
			c := get(id)
			c.ByReaction = append(c.ByReaction, ReactionCount{r, n})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT story_id, viewer_id FROM (SELECT story_id, viewer_id, date,
		row_number() OVER (PARTITION BY story_id ORDER BY date DESC, viewer_id DESC) AS n FROM story_views WHERE owner_id=$1 AND story_id = ANY($2)) x
		WHERE n <= 3 ORDER BY story_id, date DESC, viewer_id DESC`, owner, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int
			v  int64
		)
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		c := get(id)
		c.Recent = append(c.Recent, v)
	}
	return out, rows.Err()
}

// View is one row of a story's viewers list.
type Viewer struct {
	User        int64
	Date        int
	Reaction    tg.ReactionClass
	Blocked     bool // the owner blocked them
	BlockedFrom bool // on the owner's "hide my stories from"
}

type ViewersQuery struct {
	Q              string // name or username
	JustContacts   bool
	ReactionsFirst bool
	Offset         int
	Limit          int
}

// Viewers is a page of who saw owner's story, newest first (or the ones
// who reacted first), and how many match.
func (s *Service) Viewers(ctx context.Context, owner int64, id int, q ViewersQuery) ([]Viewer, int, error) {
	cond := ` FROM story_views v JOIN users u ON u.id = v.viewer_id WHERE v.owner_id=$1 AND v.story_id=$2
		AND (NOT $3 OR EXISTS (SELECT 1 FROM contacts c WHERE c.owner_id = $1 AND c.user_id = v.viewer_id))
		AND ($4 = '' OR u.name ILIKE '%' || $4 || '%' OR u.username ILIKE $4 || '%')`
	text := strings.TrimPrefix(strings.TrimSpace(q.Q), "@")
	text = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+cond, owner, id, q.JustContacts, text).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := " ORDER BY v.date DESC, v.viewer_id DESC"
	if q.ReactionsFirst {
		order = " ORDER BY (v.reaction IS NOT NULL) DESC, v.reaction_date DESC, v.date DESC, v.viewer_id DESC"
	}
	rows, err := s.db.Query(ctx, `SELECT v.viewer_id, v.date, v.reaction,
		EXISTS (SELECT 1 FROM blocked_users b WHERE b.blocker_id = $1 AND b.blocked_id = v.viewer_id),
		EXISTS (SELECT 1 FROM stories_blocked sb WHERE sb.owner_id = $1 AND sb.blocked_id = v.viewer_id)`+cond+order+" OFFSET $5 LIMIT $6",
		owner, id, q.JustContacts, text, max(q.Offset, 0), clamp(q.Limit))
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Viewer, error) {
		var (
			v   Viewer
			raw []byte
		)
		if err := r.Scan(&v.User, &v.Date, &raw, &v.Blocked, &v.BlockedFrom); err != nil {
			return v, err
		}
		v.Reaction, _ = decodeReaction(raw)
		return v, nil
	})
	return list, total, err
}

// CloseFriends is owner's close friends list.
func (s *Service) CloseFriends(ctx context.Context, owner int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, "SELECT friend_id FROM close_friends WHERE owner_id=$1 ORDER BY friend_id", owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// SetCloseFriends replaces owner's close friends with ids (unknown ids and
// the owner are dropped) and returns who joined and who left.
func (s *Service) SetCloseFriends(ctx context.Context, owner int64, ids []int64) (added, removed []int64, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "DELETE FROM close_friends WHERE owner_id=$1 AND NOT friend_id = ANY($2) RETURNING friend_id", owner, nz(ids))
		if err != nil {
			return err
		}
		if removed, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `INSERT INTO close_friends(owner_id, friend_id) SELECT $1, id FROM users WHERE id = ANY($2) AND id <> $1
			ON CONFLICT DO NOTHING RETURNING friend_id`, owner, nz(ids))
		if err != nil {
			return err
		}
		added, err = pgx.CollectRows(rows, pgx.RowTo[int64])
		return err
	})
	return added, removed, err
}

// SetHidden moves owner's stories out of viewer's feed or back.
func (s *Service) SetHidden(ctx context.Context, viewer, owner int64, hidden bool) error {
	var err error
	if hidden {
		_, err = s.db.Exec(ctx, "INSERT INTO stories_hidden(viewer_id, owner_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", viewer, owner)
	} else {
		_, err = s.db.Exec(ctx, "DELETE FROM stories_hidden WHERE viewer_id=$1 AND owner_id=$2", viewer, owner)
	}
	return err
}

// Blocked is one entry of "hide my stories from".
type Blocked struct {
	User int64
	Date int
}

// SetBlocked puts user on owner's "hide my stories from" list or takes them
// off; false when nothing changed.
func (s *Service) SetBlocked(ctx context.Context, owner, user int64, block bool) (bool, error) {
	var (
		tag pgconn.CommandTag
		err error
	)
	if block {
		tag, err = s.db.Exec(ctx, "INSERT INTO stories_blocked(owner_id, blocked_id, date) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", owner, user, s.unix())
	} else {
		tag, err = s.db.Exec(ctx, "DELETE FROM stories_blocked WHERE owner_id=$1 AND blocked_id=$2", owner, user)
	}
	return tag.RowsAffected() == 1, err
}

// BlockedList is one page of owner's "hide my stories from", newest first.
func (s *Service) BlockedList(ctx context.Context, owner int64, offset, limit int) ([]Blocked, int, error) {
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM stories_blocked WHERE owner_id=$1", owner).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, "SELECT blocked_id, date FROM stories_blocked WHERE owner_id=$1 ORDER BY date DESC, blocked_id DESC OFFSET $2 LIMIT $3",
		owner, max(offset, 0), clamp(limit))
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Blocked, error) {
		var b Blocked
		err := r.Scan(&b.User, &b.Date)
		return b, err
	})
	return list, total, err
}

// IsBlocked is whether owner hides their stories from user.
func (s *Service) IsBlocked(ctx context.Context, owner, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stories_blocked WHERE owner_id=$1 AND blocked_id=$2)", owner, user).Scan(&ok)
	return ok, err
}

// HasPinned is whether viewer can see any of owner's profile stories.
func (s *Service) HasPinned(ctx context.Context, viewer, owner int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM stories s WHERE s.owner_id=$2 AND s.pinned AND "+visible("$1")+")", viewer, owner).Scan(&ok)
	return ok, err
}

func encodeEntities(list []tg.MessageEntityClass) ([]byte, error) {
	if len(list) == 0 {
		return nil, nil
	}
	var b bin.Buffer
	b.PutVectorHeader(len(list))
	for _, e := range list {
		if err := e.Encode(&b); err != nil {
			return nil, err
		}
	}
	return b.Buf, nil
}

func decodeEntities(raw []byte) ([]tg.MessageEntityClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: raw}
	n, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	out := make([]tg.MessageEntityClass, 0, n)
	for range n {
		e, err := tg.DecodeMessageEntity(b)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// encodeReaction is nil for no reaction (or the empty one).
func encodeReaction(r tg.ReactionClass) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	if _, empty := r.(*tg.ReactionEmpty); empty {
		return nil, nil
	}
	var b bin.Buffer
	if err := r.Encode(&b); err != nil {
		return nil, err
	}
	return b.Buf, nil
}

func decodeReaction(raw []byte) (tg.ReactionClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return tg.DecodeReaction(&bin.Buffer{Buf: raw})
}

// Clone is a deep copy. td's Encode writes the flags fields while it
// encodes, so a story going to more than one reply or push job needs its
// own media and entities for each.
func (st Story) Clone() (Story, error) {
	raw, err := st.Media.Encode()
	if err != nil {
		return st, err
	}
	kind, id := st.Media.Kind, st.Media.ID
	if st.Media, err = media.Load(raw, &kind, &id); err != nil {
		return st, err
	}
	ent, err := encodeEntities(st.Entities)
	if err != nil {
		return st, err
	}
	if st.Entities, err = decodeEntities(ent); err != nil {
		return st, err
	}
	r := st.Rules
	st.Rules = Rules{Audience: r.Audience, AllowUsers: append([]int64{}, r.AllowUsers...), DenyUsers: append([]int64{}, r.DenyUsers...),
		AllowChats: append([]int64{}, r.AllowChats...), DenyChats: append([]int64{}, r.DenyChats...)}
	return st, nil
}
