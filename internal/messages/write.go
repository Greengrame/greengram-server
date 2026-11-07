package messages

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
)

const MaxLength = 4096

type Draft struct {
	Text     string
	Entities []tg.MessageEntityClass
	ReplyTo  int   // id in the sender's box
	RandomID int64 // client's dedupe id; 0 = none
	// forwards keep the original author and date
	Fwd     Origin
	Media   media.Ref // text is the caption then, and may be empty
	Grouped int64
	// an article: encoded richMessage, Text its plain words (past MaxLength)
	Rich []byte
	// bots only; both copies carry it
	ReplyMarkup tg.ReplyMarkupClass
	// sent through this bot's inline mode; the keyboard above is the bot's then
	ViaBot int64
	// makes it a service message. A gift says different things to the two
	// sides: PeerAction is the recipient's copy, nil means the same as Action.
	Action     tg.MessageActionClass
	PeerAction tg.MessageActionClass
	Silent     bool
	// the recipient blocked the sender: only the sender's copy is written,
	// so they get no dialog bump, no push and never read it
	SenderOnly bool
}

// Sent is a new message: the sender's copy and journal entry, plus events
// for the recipient.
type Sent struct {
	Msg    Msg
	Update tg.UpdateClass // UpdateNewMessage in the sender's journal
	Events []Event
}

// Send writes one message from `from` to `to` (to == from is Saved Messages).
// The caller has checked that `to` exists and may be written to.
func (s *Service) Send(ctx context.Context, from, to int64, d Draft) (Sent, error) {
	var out Sent
	if d.Text == "" && d.Media.Empty() && d.Action == nil && len(d.Rich) == 0 {
		return out, errEmpty
	}
	if len(d.Rich) == 0 && utf8.RuneCountInString(d.Text) > MaxLength {
		return out, errTooLong
	}
	ent, err := encodeEntities(d.Entities)
	if err != nil {
		return out, err
	}
	mediaTL, err := d.Media.Encode()
	if err != nil {
		return out, err
	}
	markup, err := EncodeMarkup(d.ReplyMarkup)
	if err != nil {
		return out, err
	}
	mineAction, err := encodeAction(d.Action)
	if err != nil {
		return out, err
	}
	if d.PeerAction == nil {
		d.PeerAction = d.Action
	}
	theirAction, err := encodeAction(d.PeerAction)
	if err != nil {
		return out, err
	}
	now := s.unix()
	date := now // a forward is new here; the original date is in its header
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, from, to); err != nil {
			return err
		}
		if d.RandomID != 0 {
			var dup bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE owner_id=$1 AND random_id=$2)", from, d.RandomID).Scan(&dup); err != nil {
				return err
			}
			if dup {
				return tgerr.New(400, "RANDOM_ID_DUPLICATE")
			}
		}
		var shared int64
		if err := tx.QueryRow(ctx, "SELECT nextval('message_shared_ids')").Scan(&shared); err != nil {
			return err
		}
		var replyShared *int64
		if d.ReplyTo != 0 {
			var sh int64
			err := tx.QueryRow(ctx, "SELECT shared_id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3", from, to, d.ReplyTo).Scan(&sh)
			if errors.Is(err, pgx.ErrNoRows) {
				d.ReplyTo = 0 // replied-to message is gone; send without the quote, like Telegram
			} else if err != nil {
				return err
			} else {
				replyShared = &sh
			}
		}

		write := func(owner, peer int64, out bool, reply int, random *int64, actRaw []byte) (Msg, error) {
			id, err := nextID(ctx, tx, owner)
			if err != nil {
				return Msg{}, err
			}
			// each copy its own object: td's Encode writes flags in place, and
			// the two boxes' updates get encoded on different goroutines
			var act tg.MessageActionClass
			if len(actRaw) > 0 {
				if act, err = tg.DecodeMessageAction(&bin.Buffer{Buf: actRaw}); err != nil {
					return Msg{}, err
				}
			}
			m := Msg{Owner: owner, ID: id, Peer: peer, From: from, Out: out, Shared: shared, Date: date, Text: d.Text,
				Entities: d.Entities, ReplyTo: reply, Fwd: d.Fwd, Media: d.Media, Grouped: d.Grouped, ReplyMarkup: d.ReplyMarkup,
				Silent: d.Silent, Action: act, Rich: d.Rich, ViaBot: d.ViaBot}
			_, err = tx.Exec(ctx, `INSERT INTO messages(owner_id, id, peer_id, from_id, out, shared_id, date, text, entities, reply_to, fwd_from_id, fwd_date, random_id,
					media, media_kind, media_id, grouped_id, reply_markup, fwd_from_chan, fwd_from_post, fwd_from_name, silent, action, rich, via_bot_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, 0), nullif($11, 0), nullif($12, 0), $13, $14, nullif($15, ''), nullif($16::bigint, 0), nullif($17::bigint, 0), $18,
					nullif($19::bigint, 0), nullif($20::int, 0), nullif($21, ''), $22, $23, $24, nullif($25::bigint, 0))`,
				owner, id, peer, from, out, shared, date, d.Text, ent, reply, d.Fwd.User, d.Fwd.Date, random, mediaTL, d.Media.Kind, d.Media.ID, d.Grouped, markup,
				d.Fwd.Chan, d.Fwd.Post, d.Fwd.Name, d.Silent, actRaw, d.Rich, d.ViaBot)
			if err != nil {
				return m, err
			}
			// read marks move only through readHistory, which also tells the
			// other side; own messages never count as unread anyway
			_, err = tx.Exec(ctx, `INSERT INTO dialogs(owner_id, peer_id, top_id, top_date) VALUES ($1, $2, $3, $4)
				ON CONFLICT (owner_id, peer_id) DO UPDATE SET top_id = excluded.top_id, top_date = excluded.top_date`,
				owner, peer, id, now)
			return m, err
		}

		var random *int64
		if d.RandomID != 0 {
			random = &d.RandomID
		}
		mine, err := write(from, to, true, d.ReplyTo, random, mineAction)
		if err != nil {
			return err
		}
		out.Msg = mine
		out.Update, err = s.journal(ctx, tx, from, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateNewMessage{Message: mine.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil || to == from || d.SenderOnly {
			return err
		}

		reply := 0
		if replyShared != nil {
			err := tx.QueryRow(ctx, "SELECT id FROM messages WHERE owner_id=$1 AND shared_id=$2", to, *replyShared).Scan(&reply)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		theirs, err := write(to, from, false, reply, nil, theirAction)
		if err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, to, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateNewMessage{Message: theirs.TL(), Pts: pts, PtsCount: 1}
		})
		out.Events = append(out.Events, Event{to, u})
		return err
	})
	return out, err
}

// SetAction rewrites a service message in one box and tells that box's
// other apps. Only its owner's copy changes: what the recipient did with a
// gift is nobody else's business.
func (s *Service) SetAction(ctx context.Context, owner int64, id int, a tg.MessageActionClass) (tg.UpdateClass, error) {
	raw, err := encodeAction(a)
	if err != nil {
		return nil, err
	}
	var update tg.UpdateClass
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, owner); err != nil {
			return err
		}
		m, err := scanMsg(tx.QueryRow(ctx, "UPDATE messages SET action=$3 WHERE owner_id=$1 AND id=$2 AND action IS NOT NULL RETURNING "+msgCols, owner, id, raw))
		if err != nil {
			return err
		}
		update, err = s.journal(ctx, tx, owner, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
		})
		return err
	})
	return update, err
}

// Get returns messages of one box by id, in the order asked (missing ids
// are skipped).
func (s *Service) Get(ctx context.Context, owner int64, list []int) ([]Msg, error) {
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages WHERE owner_id=$1 AND id = ANY($2) ORDER BY id DESC", owner, list)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// Edit changes the text of one of the user's own messages, in both boxes.
// rich makes it an article; nil makes it an ordinary message.
func (s *Service) Edit(ctx context.Context, user, peer int64, id int, text string, entities []tg.MessageEntityClass, rich []byte) (Msg, tg.UpdateClass, []Event, error) {
	var (
		mine   Msg
		update tg.UpdateClass
		events []Event
	)
	if len(rich) == 0 && utf8.RuneCountInString(text) > MaxLength {
		return mine, nil, nil, errTooLong
	}
	ent, err := encodeEntities(entities)
	if err != nil {
		return mine, nil, nil, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, user, peer); err != nil {
			return err
		}
		var shared int64
		var hasMedia bool
		err := tx.QueryRow(ctx, "SELECT shared_id, media IS NOT NULL FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND out AND action IS NULL AND fwd_from_id IS NULL AND fwd_date IS NULL",
			user, peer, id).Scan(&shared, &hasMedia)
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_AUTHOR_REQUIRED")
		}
		if err != nil {
			return err
		}
		// a photo may lose its caption, a text message can't lose its text
		if text == "" && !hasMedia && len(rich) == 0 {
			return errEmpty
		}
		rows, err := tx.Query(ctx, "UPDATE messages SET text=$2, entities=$3, edit_date=$4, rich=$5 WHERE shared_id=$1 RETURNING "+msgCols, shared, text, ent, s.unix(), rich)
		if err != nil {
			return err
		}
		copies, err := collect(rows)
		if err != nil {
			return err
		}
		for _, m := range copies {
			m := m
			u, err := s.journal(ctx, tx, m.Owner, 1, func(pts int) tg.UpdateClass {
				return &tg.UpdateEditMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
			})
			if err != nil {
				return err
			}
			if m.Owner == user {
				mine, update = m, u
			} else {
				events = append(events, Event{m.Owner, u})
			}
		}
		return nil
	})
	return mine, update, events, err
}

// Delete removes messages from the user's box; with revoke, the other
// person's copies too.
func (s *Service) Delete(ctx context.Context, user int64, list []int, revoke bool) (tg.MessagesAffectedMessages, []Event, error) {
	var (
		res    tg.MessagesAffectedMessages
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		type row struct {
			id     int
			peer   int64
			shared int64
		}
		rows, err := tx.Query(ctx, "SELECT id, peer_id, shared_id FROM messages WHERE owner_id=$1 AND id = ANY($2)", user, list)
		if err != nil {
			return err
		}
		var mine []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.peer, &r.shared); err != nil {
				rows.Close()
				return err
			}
			mine = append(mine, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		involved := []int64{user}
		for _, r := range mine {
			involved = append(involved, r.peer)
		}
		if err := lockBoxes(ctx, tx, involved...); err != nil {
			return err
		}
		if len(mine) == 0 {
			st, err := s.stateTx(ctx, tx, user)
			res.Pts = st
			return err
		}
		// owner -> ids being removed from that box
		byOwner := map[int64][]int{}
		touched := map[[2]int64]bool{}
		for _, r := range mine {
			byOwner[user] = append(byOwner[user], r.id)
			touched[[2]int64{user, r.peer}] = true
		}
		if revoke {
			shared := make([]int64, 0, len(mine))
			for _, r := range mine {
				shared = append(shared, r.shared)
			}
			rows, err := tx.Query(ctx, "SELECT owner_id, id, peer_id FROM messages WHERE shared_id = ANY($1) AND owner_id <> $2", shared, user)
			if err != nil {
				return err
			}
			for rows.Next() {
				var owner, peer int64
				var id int
				if err := rows.Scan(&owner, &id, &peer); err != nil {
					rows.Close()
					return err
				}
				byOwner[owner] = append(byOwner[owner], id)
				touched[[2]int64{owner, peer}] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		for owner, gone := range byOwner {
			if _, err := tx.Exec(ctx, "DELETE FROM messages WHERE owner_id=$1 AND id = ANY($2)", owner, gone); err != nil {
				return err
			}
			gone := gone
			u, err := s.journal(ctx, tx, owner, len(gone), func(pts int) tg.UpdateClass {
				return &tg.UpdateDeleteMessages{Messages: gone, Pts: pts, PtsCount: len(gone)}
			})
			if err != nil {
				return err
			}
			if owner == user {
				d := u.(*tg.UpdateDeleteMessages)
				res = tg.MessagesAffectedMessages{Pts: d.Pts, PtsCount: d.PtsCount}
			}
			// the user's own entry goes to their other sessions
			events = append(events, Event{owner, u})
		}
		for k := range touched {
			if err := refreshDialog(ctx, tx, k[0], k[1]); err != nil {
				return err
			}
		}
		// reactions go with the last copy
		shared := make([]int64, 0, len(mine))
		for _, r := range mine {
			shared = append(shared, r.shared)
		}
		_, err = tx.Exec(ctx, `DELETE FROM message_reactions r WHERE r.shared_id = ANY($1)
			AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.shared_id = r.shared_id)`, shared)
		return err
	})
	return res, events, err
}

// refreshDialog points the dialog at its newest remaining message, or drops it.
func refreshDialog(ctx context.Context, tx pgx.Tx, owner, peer int64) error {
	var id, date int
	err := tx.QueryRow(ctx, "SELECT id, date FROM messages WHERE owner_id=$1 AND peer_id=$2 ORDER BY id DESC LIMIT 1", owner, peer).Scan(&id, &date)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, "DELETE FROM dialogs WHERE owner_id=$1 AND peer_id=$2", owner, peer)
		return err
	}
	if err != nil {
		return err
	}
	// top_date too: the chat list is ordered by it
	_, err = tx.Exec(ctx, "UPDATE dialogs SET top_id=$3, top_date=$4 WHERE owner_id=$1 AND peer_id=$2", owner, peer, id, date)
	return err
}

func (s *Service) stateTx(ctx context.Context, tx pgx.Tx, user int64) (int, error) {
	var pts int
	err := tx.QueryRow(ctx, "SELECT pts FROM user_boxes WHERE user_id=$1", user).Scan(&pts)
	return pts, err
}

// ReadHistory marks the user's inbox with peer read up to maxID and tells
// the peer their messages were read.
func (s *Service) ReadHistory(ctx context.Context, user, peer int64, maxID int) (tg.MessagesAffectedMessages, []Event, error) {
	var (
		res    tg.MessagesAffectedMessages
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, user, peer); err != nil {
			return err
		}
		var current, top int
		err := tx.QueryRow(ctx, "SELECT read_inbox_max, top_id FROM dialogs WHERE owner_id=$1 AND peer_id=$2", user, peer).Scan(&current, &top)
		if errors.Is(err, pgx.ErrNoRows) {
			res.Pts, err = s.stateTx(ctx, tx, user)
			return err
		}
		if err != nil {
			return err
		}
		if maxID <= 0 || maxID > top {
			maxID = top
		}
		if maxID <= current {
			res.Pts, err = s.stateTx(ctx, tx, user)
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE dialogs SET read_inbox_max=$3 WHERE owner_id=$1 AND peer_id=$2", user, peer, maxID); err != nil {
			return err
		}
		var left int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM messages WHERE owner_id=$1 AND peer_id=$2 AND NOT out AND id > $3", user, peer, maxID).Scan(&left); err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, user, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateReadHistoryInbox{Peer: &tg.PeerUser{UserID: ids.Client(peer)}, MaxID: maxID, StillUnreadCount: left, Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return err
		}
		res = tg.MessagesAffectedMessages{Pts: u.(*tg.UpdateReadHistoryInbox).Pts, PtsCount: 1}
		// the journal's copy for the user's other sessions: the request's
		// max_id may be 0 or past the end, and it knows what's still unread
		events = append(events, Event{user, u})
		if peer == user {
			return nil
		}
		// the newest message of theirs that is now read, in their box
		var outMax *int
		err = tx.QueryRow(ctx, `SELECT max(p.id) FROM messages o JOIN messages p ON p.shared_id = o.shared_id AND p.owner_id = $2
			WHERE o.owner_id=$1 AND o.peer_id=$2 AND NOT o.out AND o.id <= $3`, user, peer, maxID).Scan(&outMax)
		if err != nil || outMax == nil {
			return err
		}
		tag, err := tx.Exec(ctx, "UPDATE dialogs SET read_outbox_max=$3 WHERE owner_id=$1 AND peer_id=$2 AND read_outbox_max < $3", peer, user, *outMax)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		pu, err := s.journal(ctx, tx, peer, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateReadHistoryOutbox{Peer: &tg.PeerUser{UserID: ids.Client(user)}, MaxID: *outMax, Pts: pts, PtsCount: 1}
		})
		events = append(events, Event{peer, pu})
		return err
	})
	return res, events, err
}
