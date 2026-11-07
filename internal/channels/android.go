package channels

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// Editable is the message user may edit, checked as Edit checks it, for
// messages.getMessageEditData. The Android app asks before every edit and
// drops the edit on any error, so this must not refuse what Edit takes.
func (s *Service) Editable(ctx context.Context, user, channel int64, id int) (Msg, error) {
	c, ok, err := s.Get(ctx, channel)
	if err != nil {
		return Msg{}, err
	}
	if !ok || c.Deleted {
		return Msg{}, errInvalid
	}
	me, err := member(ctx, s.db, channel, user)
	if err != nil {
		return Msg{}, err
	}
	if !InChat(me.Status) {
		return Msg{}, errPrivate
	}
	m, err := scanMsg(s.db.QueryRow(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE channel_id=$1 AND id=$2", channel, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Msg{}, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	if err != nil {
		return Msg{}, err
	}
	own := m.From == user && (!m.Post || c.can(me, "post"))
	if m.Action != nil || m.FwdChanID != 0 || !m.Fwd.Empty() || !(own || (m.Post && c.can(me, "edit"))) {
		return Msg{}, tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
	}
	return m, nil
}

// OwnedPublic are the public chats user created, broadcast channels only
// with broadcasts set: channels.getAdminedPublicChannels and the personal
// channel picker.
func (s *Service) OwnedPublic(ctx context.Context, user int64, broadcasts bool) ([]Channel, error) {
	rows, err := s.db.Query(ctx, "SELECT "+chanCols+` FROM channels c WHERE c.creator_id=$1 AND c.username IS NOT NULL
		AND NOT c.deleted AND (NOT $2 OR NOT c.megagroup) ORDER BY c.id`, user, broadcasts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
