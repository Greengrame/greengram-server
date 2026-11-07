package channels

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

func pinUpdate(channel int64, list []int, pinned bool) func(pts int) tg.UpdateClass {
	return func(pts int) tg.UpdateClass {
		return &tg.UpdatePinnedChannelMessages{Pinned: pinned, ChannelID: channel, Messages: list, Pts: pts, PtsCount: 1}
	}
}

// PinMessage pins or unpins a message; pinning posts the service message
// too ("X pinned ..."), silent or not.
func (s *Service) PinMessage(ctx context.Context, user, channel int64, id int, pinned, silent bool) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		if !InChat(me.Status) {
			return errPrivate
		}
		if !c.can(me, "pin") {
			return errAdminRequire
		}
		tag, err := tx.Exec(ctx, "UPDATE channel_messages SET pinned=$3 WHERE channel_id=$1 AND id=$2 AND action IS NULL", channel, id, pinned)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		u, err := s.journal(ctx, tx, channel, 1, pinUpdate(channel, []int{id}, pinned))
		if err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, u)
		if err != nil {
			return err
		}
		events = append(events, ev)
		if !pinned {
			return nil
		}
		_, su, err := s.addMessage(ctx, tx, Msg{Channel: channel, From: user, Post: !c.Megagroup, Action: &tg.MessageActionPinMessage{}, ReplyTo: id, Silent: silent}, nil)
		if err != nil {
			return err
		}
		ev, err = toMembers(ctx, tx, channel, su)
		events = append(events, ev)
		return err
	})
	return events, err
}

// UnpinAll unpins every pinned message of a channel.
func (s *Service) UnpinAll(ctx context.Context, user, channel int64) (tg.MessagesAffectedHistory, []Event, error) {
	var (
		res    tg.MessagesAffectedHistory
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		if !InChat(me.Status) {
			return errPrivate
		}
		if !c.can(me, "pin") {
			return errAdminRequire
		}
		res.Pts = c.Pts
		rows, err := tx.Query(ctx, "UPDATE channel_messages SET pinned=false WHERE channel_id=$1 AND pinned RETURNING id", channel)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil || len(list) == 0 {
			return err
		}
		u, err := s.journal(ctx, tx, channel, 1, pinUpdate(channel, list, false))
		if err != nil {
			return err
		}
		p := u.(*tg.UpdatePinnedChannelMessages)
		res = tg.MessagesAffectedHistory{Pts: p.Pts, PtsCount: p.PtsCount}
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return res, events, err
}

// TopPinned is the newest pinned message, 0 for none.
func (s *Service) TopPinned(ctx context.Context, channel int64) (int, error) {
	var id int
	err := s.db.QueryRow(ctx, "SELECT coalesce(max(id), 0) FROM channel_messages WHERE channel_id=$1 AND pinned", channel).Scan(&id)
	return id, err
}
