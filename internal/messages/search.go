package messages

import (
	"context"
	"strings"
)

// KindLink is a search filter, not a media kind: messages with a link in
// the text or a text_url entity.
const KindLink = "link"

// linkSQL finds links by looking: a URL in the text, or "http" inside the
// encoded entities (only text_url entities carry one).
const linkSQL = `(%stext ~* '(https?://|www\.)[^[:space:]]' OR position('http'::bytea IN coalesce(%sentities, ''::bytea)) > 0)`

// KindsSQL is the WHERE part for a search filter's kinds, with $K standing
// for the parameter that holds arg (nil arg: no parameter). p prefixes the
// column names ("m." or "").
func KindsSQL(kinds []string, p string) (cond string, arg any) {
	switch {
	case len(kinds) == 0:
		return "", nil
	case len(kinds) == 1 && kinds[0] == KindLink:
		return strings.ReplaceAll(linkSQL, "%s", p), nil
	}
	return p + "media_kind = ANY($K)", kinds
}

// GlobalQuery pages the private-chat half of messages.searchGlobal; the
// cursor is (BeforeDate, AfterPeer, AfterID) from the last result.
type GlobalQuery struct {
	Text       string
	Kinds      []string
	BeforeDate int
	AfterPeer  int64
	AfterID    int
	MinDate    int
	MaxDate    int
	Limit      int
}

// SearchGlobal is Search over the whole box, ordered by date so it merges
// with the channel results.
func (s *Service) SearchGlobal(ctx context.Context, owner int64, q GlobalQuery) ([]Msg, int, error) {
	limit := clampLimit(q.Limit, 100)
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Text) + "%"
	where := "owner_id=$1 AND text ILIKE $2"
	args := []any{owner, pattern}
	if cond, arg := KindsSQL(q.Kinds, ""); cond != "" {
		if arg != nil {
			args = append(args, arg)
			cond = strings.ReplaceAll(cond, "$K", "$"+itoa(len(args)))
		}
		where += " AND " + cond
	}
	if q.MinDate > 0 {
		args = append(args, q.MinDate)
		where += " AND date >= $" + itoa(len(args))
	}
	if q.MaxDate > 0 {
		args = append(args, q.MaxDate)
		where += " AND date <= $" + itoa(len(args))
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM messages WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// ids are unique in a box, so they order a second across peers. The
	// merge takes private messages first within a second: a cursor on a
	// channel message (AfterPeer 0) has passed all of them.
	if q.BeforeDate > 0 && q.AfterPeer == 0 {
		args = append(args, q.BeforeDate)
		where += " AND date < $" + itoa(len(args))
	} else if q.BeforeDate > 0 {
		args = append(args, q.BeforeDate, q.AfterID)
		n := len(args)
		where += " AND (date < $" + itoa(n-1) + " OR date = $" + itoa(n-1) + " AND id < $" + itoa(n) + ")"
	}
	args = append(args, limit)
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages WHERE "+where+" ORDER BY date DESC, id DESC LIMIT $"+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := collect(rows)
	return list, total, err
}
