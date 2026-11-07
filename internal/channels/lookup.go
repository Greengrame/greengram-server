package channels

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/messages"
)

// CommonChats are the groups both users are in, newest first, below maxID
// when given, and how many there are in all.
func (s *Service) CommonChats(ctx context.Context, a, b, maxID int64, limit int) ([]int64, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	const both = `FROM channels c JOIN channel_members x ON x.channel_id = c.id AND x.user_id = $1 AND x.status IN ` + inChatSQL + `
		JOIN channel_members y ON y.channel_id = c.id AND y.user_id = $2 AND y.status IN ` + inChatSQL + `
		WHERE c.megagroup AND NOT c.deleted`
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) "+both, a, b).Scan(&total); err != nil {
		return nil, 0, err
	}
	if maxID <= 0 {
		maxID = 1 << 62
	}
	rows, err := s.db.Query(ctx, "SELECT c.id "+both+" AND c.id < $3 ORDER BY c.id DESC LIMIT $4", a, b, maxID, limit)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	return list, total, err
}

func likePattern(q string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
}

// FindChats is the chat half of contacts.search: chats user is in whose
// title or username matches q, then public ones they aren't in.
func (s *Service) FindChats(ctx context.Context, user int64, q string, limit int) (mine, public []int64, err error) {
	q = strings.TrimPrefix(strings.TrimSpace(q), "@")
	if len([]rune(q)) < 2 {
		return nil, nil, nil
	}
	like := likePattern(q)
	prefix := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(q), `\`, `\\`), "_", `\_`) + "%"
	rows, err := s.db.Query(ctx, `SELECT c.id FROM channels c JOIN channel_members m ON m.channel_id = c.id AND m.user_id = $1
		WHERE m.status IN `+inChatSQL+` AND NOT c.deleted AND (c.title ILIKE $2 OR lower(c.username) LIKE $3)
		ORDER BY c.top_date DESC LIMIT $4`, user, like, prefix, limit)
	if err != nil {
		return nil, nil, err
	}
	if mine, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT c.id FROM channels c WHERE c.username IS NOT NULL AND NOT c.deleted
		AND (lower(c.username) LIKE $2 OR c.title ILIKE $3)
		AND NOT EXISTS (SELECT 1 FROM channel_members m WHERE m.channel_id = c.id AND m.user_id = $1 AND m.status IN `+inChatSQL+`)
		ORDER BY (SELECT count(*) FROM channel_members m WHERE m.channel_id = c.id) DESC LIMIT $4`, user, prefix, like, limit)
	if err != nil {
		return nil, nil, err
	}
	public, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	return mine, public, err
}

// GlobalQuery pages messages.searchGlobal through the chats a user is in.
// Results are newest first; a page continues below (BeforeDate, the
// message AfterChan/AfterID) the way Telegram's offset_rate does.
type GlobalQuery struct {
	Text       string
	Kinds      []string
	BeforeDate int // 0 = from the newest
	AfterChan  int64
	AfterID    int
	MinDate    int
	MaxDate    int
	Limit      int
	Broadcasts bool // channels only (searchGlobal's broadcasts_only)
	Groups     bool // groups only
	Public     bool // every public channel instead of the user's chats
}

// SearchGlobal is the group and channel half of messages.searchGlobal.
func (s *Service) SearchGlobal(ctx context.Context, user int64, q GlobalQuery) ([]Msg, int, error) {
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 100
	}
	where := "m.action IS NULL AND m.text ILIKE $1 AND NOT c.deleted"
	args := []any{likePattern(q.Text)}
	if q.Public {
		where += " AND c.username IS NOT NULL AND NOT c.megagroup"
	} else {
		args = append(args, user)
		where += " AND m.channel_id IN (SELECT channel_id FROM channel_members WHERE user_id = $2 AND status IN " + inChatSQL + ")"
	}
	if cond, arg := messages.KindsSQL(q.Kinds, "m."); cond != "" {
		if arg != nil {
			args = append(args, arg)
			cond = strings.ReplaceAll(cond, "$K", "$"+itoa(len(args)))
		}
		where += " AND " + cond
	}
	switch {
	case q.Broadcasts:
		where += " AND NOT c.megagroup"
	case q.Groups:
		where += " AND c.megagroup"
	}
	if q.MinDate > 0 {
		args = append(args, q.MinDate)
		where += " AND m.date >= $" + itoa(len(args))
	}
	if q.MaxDate > 0 {
		args = append(args, q.MaxDate)
		where += " AND m.date <= $" + itoa(len(args))
	}
	from := " FROM channel_messages m JOIN channels c ON c.id = m.channel_id WHERE "
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// ids repeat across channels, so the channel breaks ties. The merge
	// takes private messages first within a second: a cursor on one of
	// them (AfterChan 0) hasn't passed any channel message of that second.
	if q.BeforeDate > 0 && q.AfterChan == 0 {
		args = append(args, q.BeforeDate)
		where += " AND m.date <= $" + itoa(len(args))
	} else if q.BeforeDate > 0 {
		args = append(args, q.BeforeDate, q.AfterChan, q.AfterID)
		n := len(args)
		where += " AND (m.date < $" + itoa(n-2) + " OR m.date = $" + itoa(n-2) + " AND (m.id < $" + itoa(n) + " OR m.id = $" + itoa(n) + " AND m.channel_id > $" + itoa(n-1) + "))"
	}
	args = append(args, q.Limit)
	rows, err := s.db.Query(ctx, "SELECT "+mCols+from+where+" ORDER BY m.date DESC, m.id DESC, m.channel_id LIMIT $"+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := collect(rows)
	return list, total, err
}

// mCols is msgCols for a query that joins channels as c and names
// messages m.
const mCols = `m.channel_id, m.id, m.from_id, m.date, m.post, m.text, m.entities, m.action, coalesce(m.reply_to, 0), coalesce(m.fwd_from_id, 0),
	coalesce(m.fwd_date, 0), coalesce(m.edit_date, 0), coalesce(m.fwd_channel_id, 0), coalesce(m.fwd_channel_msg, 0),
	m.media, m.media_kind, m.media_id, coalesce(m.grouped_id, 0), m.reply_markup,
	coalesce(m.fwd_from_chan, 0), coalesce(m.fwd_from_post, 0), coalesce(m.fwd_from_name, ''), coalesce(m.topic_id, 0), m.pinned, m.silent, m.rich, coalesce(m.via_bot_id, 0)`
