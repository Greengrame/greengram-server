package channels

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

type Invite struct {
	Hash      string
	Channel   int64
	Creator   int64
	Title     string
	Created   int
	Expire    int // 0 = never
	Limit     int // 0 = unlimited
	Usage     int
	Revoked   bool
	Permanent bool
}

const inviteCols = "hash, channel_id, creator_id, title, created_at, coalesce(expire_date, 0), coalesce(usage_limit, 0), usage, revoked, permanent"

func scanInvite(row pgx.Row) (Invite, error) {
	var i Invite
	err := row.Scan(&i.Hash, &i.Channel, &i.Creator, &i.Title, &i.Created, &i.Expire, &i.Limit, &i.Usage, &i.Revoked, &i.Permanent)
	return i, err
}

func (i Invite) Usable(now int) bool {
	return !i.Revoked && (i.Expire == 0 || i.Expire > now) && (i.Limit == 0 || i.Usage < i.Limit)
}

// TL is the invite as its channel's admins see it. base is the public site.
func (i Invite) TL(base string) *tg.ChatInviteExported {
	out := &tg.ChatInviteExported{
		Link:      base + "/+" + i.Hash,
		AdminID:   ids.Client(i.Creator),
		Date:      i.Created,
		Revoked:   i.Revoked,
		Permanent: i.Permanent,
	}
	if i.Title != "" {
		out.SetTitle(i.Title)
	}
	if i.Expire != 0 {
		out.SetExpireDate(i.Expire)
	}
	if i.Limit != 0 {
		out.SetUsageLimit(i.Limit)
	}
	out.SetUsage(i.Usage)
	return out
}

func newHash() string {
	b := make([]byte, 12)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ExportInvite makes a link; permanent replaces the channel's main link.
func (s *Service) ExportInvite(ctx context.Context, actor, channel int64, title string, expire, limit int, permanent bool) (Invite, error) {
	var inv Invite
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, actor)
		if err != nil {
			return err
		}
		if !c.can(me, "invite") {
			return errAdminRequire
		}
		if permanent {
			if _, err := tx.Exec(ctx, "UPDATE channel_invites SET revoked=true WHERE channel_id=$1 AND creator_id=$2 AND permanent", channel, actor); err != nil {
				return err
			}
		}
		inv, err = scanInvite(tx.QueryRow(ctx, `INSERT INTO channel_invites(hash, channel_id, creator_id, title, created_at, expire_date, usage_limit, permanent)
			VALUES ($1, $2, $3, $4, $5, nullif($6, 0), nullif($7, 0), $8) RETURNING `+inviteCols,
			newHash(), channel, actor, title, s.unix(), expire, limit, permanent))
		return err
	})
	return inv, err
}

// PermanentInvite is the channel's main link for actor, made on first use.
func (s *Service) PermanentInvite(ctx context.Context, actor, channel int64) (Invite, error) {
	inv, err := scanInvite(s.db.QueryRow(ctx, "SELECT "+inviteCols+" FROM channel_invites WHERE channel_id=$1 AND creator_id=$2 AND permanent AND NOT revoked", channel, actor))
	if err == nil {
		return inv, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return inv, err
	}
	return s.ExportInvite(ctx, actor, channel, "", 0, 0, true)
}

func (s *Service) Invites(ctx context.Context, actor, channel, creator int64, revoked bool, limit int) ([]Invite, int, error) {
	c, me, err := s.Access(ctx, channel, actor)
	if err != nil {
		return nil, 0, err
	}
	if !c.can(me, "invite") {
		return nil, 0, errAdminRequire
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM channel_invites WHERE channel_id=$1 AND creator_id=$2 AND revoked=$3", channel, creator, revoked).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, "SELECT "+inviteCols+" FROM channel_invites WHERE channel_id=$1 AND creator_id=$2 AND revoked=$3 ORDER BY permanent DESC, created_at DESC LIMIT $4", channel, creator, revoked, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	return out, total, rows.Err()
}

// EditInvite revokes or changes a link.
func (s *Service) EditInvite(ctx context.Context, actor int64, hash string, revoke bool, title *string, expire, limit *int) (Invite, error) {
	inv, err := scanInvite(s.db.QueryRow(ctx, "SELECT "+inviteCols+" FROM channel_invites WHERE hash=$1", hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, tgerr.New(400, "INVITE_HASH_INVALID")
	}
	if err != nil {
		return inv, err
	}
	c, me, err := s.Access(ctx, inv.Channel, actor)
	if err != nil {
		return inv, err
	}
	if !c.can(me, "invite") || (inv.Creator != actor && me.Status != Creator) {
		return inv, errAdminRequire
	}
	return scanInvite(s.db.QueryRow(ctx, `UPDATE channel_invites SET revoked = revoked OR $2,
		title = coalesce($3, title), expire_date = CASE WHEN $4::int IS NULL THEN expire_date ELSE nullif($4, 0) END,
		usage_limit = CASE WHEN $5::int IS NULL THEN usage_limit ELSE nullif($5, 0) END
		WHERE hash=$1 RETURNING `+inviteCols, hash, revoke, title, expire, limit))
}

func (s *Service) DeleteInvite(ctx context.Context, actor int64, hash string) error {
	inv, err := scanInvite(s.db.QueryRow(ctx, "SELECT "+inviteCols+" FROM channel_invites WHERE hash=$1", hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return tgerr.New(400, "INVITE_HASH_INVALID")
	}
	if err != nil {
		return err
	}
	c, me, err := s.Access(ctx, inv.Channel, actor)
	if err != nil {
		return err
	}
	if !c.can(me, "invite") || !inv.Revoked {
		return errAdminRequire
	}
	_, err = s.db.Exec(ctx, "DELETE FROM channel_invites WHERE hash=$1", hash)
	return err
}

// CheckInvite is what someone with the link sees before joining.
func (s *Service) CheckInvite(ctx context.Context, hash string) (Invite, Channel, error) {
	inv, err := scanInvite(s.db.QueryRow(ctx, "SELECT "+inviteCols+" FROM channel_invites WHERE hash=$1", hash))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inv.Usable(s.unix()) {
		return inv, Channel{}, tgerr.New(400, "INVITE_HASH_EXPIRED")
	}
	if err != nil {
		return inv, Channel{}, err
	}
	c, ok, err := s.Get(ctx, inv.Channel)
	if err != nil {
		return inv, c, err
	}
	if !ok || c.Deleted {
		return inv, c, tgerr.New(400, "INVITE_HASH_EXPIRED")
	}
	maker, err := s.Member(ctx, c.ID, inv.Creator)
	if err != nil {
		return inv, c, err
	}
	if !c.can(maker, "invite") {
		return inv, c, tgerr.New(400, "INVITE_HASH_EXPIRED")
	}
	return inv, c, nil
}

// ImportInvite joins through a link.
func (s *Service) ImportInvite(ctx context.Context, user int64, hash string) (int64, []Event, error) {
	var (
		channel int64
		events  []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		inv, err := scanInvite(tx.QueryRow(ctx, "SELECT "+inviteCols+" FROM channel_invites WHERE hash=$1 FOR UPDATE", hash))
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !inv.Usable(s.unix()) {
			return tgerr.New(400, "INVITE_HASH_EXPIRED")
		}
		if err != nil {
			return err
		}
		channel = inv.Channel
		c, err := lock(ctx, tx, inv.Channel)
		if err != nil {
			return err
		}
		// a link dies with its maker's right to invite (kicked, left, demoted)
		maker, err := member(ctx, tx, inv.Channel, inv.Creator)
		if err != nil {
			return err
		}
		if !c.can(maker, "invite") {
			return tgerr.New(400, "INVITE_HASH_EXPIRED")
		}
		if events, err = s.join(ctx, tx, user, inv.Channel, inv.Creator); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE channel_invites SET usage = usage + 1 WHERE hash=$1", hash)
		return err
	})
	return channel, events, err
}

// SetDiscussion links a broadcast channel to a group for comments (group 0
// unlinks). The user must run both.
func (s *Service) SetDiscussion(ctx context.Context, actor, broadcast, group int64) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, broadcast)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, broadcast, actor)
		if err != nil {
			return err
		}
		if c.Megagroup || !c.can(me, "change_info") {
			return errAdminRequire
		}
		if c.LinkedID != 0 {
			if _, err := tx.Exec(ctx, "UPDATE channels SET linked_id=NULL WHERE id = ANY($1)", []int64{c.ID, c.LinkedID}); err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, c.LinkedID, updChannel(c.LinkedID))
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		if group != 0 {
			g, err := lock(ctx, tx, group)
			if err != nil {
				return err
			}
			gm, err := member(ctx, tx, group, actor)
			if err != nil {
				return err
			}
			if !g.Megagroup || !g.can(gm, "change_info") {
				return tgerr.New(400, "MEGAGROUP_ID_INVALID")
			}
			if g.LinkedID != 0 && g.LinkedID != c.ID {
				return tgerr.New(400, "LINK_NOT_MODIFIED")
			}
			if _, err := tx.Exec(ctx, "UPDATE channels SET linked_id=$2 WHERE id=$1; ", c.ID, g.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE channels SET linked_id=$2 WHERE id=$1", g.ID, c.ID); err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, g.ID, updChannel(g.ID))
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		ev, err := toMembers(ctx, tx, c.ID, updChannel(c.ID))
		events = append(events, ev)
		return err
	})
	return events, err
}

// DiscussionCopy finds the group message a channel post was copied to.
func (s *Service) DiscussionCopy(ctx context.Context, channel int64, post int) (Msg, int64, error) {
	var group int64
	if err := s.db.QueryRow(ctx, "SELECT coalesce(linked_id, 0) FROM channels WHERE id=$1", channel).Scan(&group); err != nil || group == 0 {
		return Msg{}, 0, tgerr.New(400, "MSG_ID_INVALID")
	}
	m, err := scanMsg(s.db.QueryRow(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE channel_id=$1 AND fwd_channel_id=$2 AND fwd_channel_msg=$3", group, channel, post))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, group, tgerr.New(400, "MSG_ID_INVALID")
	}
	return m, group, err
}

// Replies counts comments under a group message (or a forum topic's
// messages, past its opening one) and the newest one.
func (s *Service) Replies(ctx context.Context, group int64, top int) (count, maxID int, err error) {
	err = s.db.QueryRow(ctx, `SELECT count(*), coalesce(max(id), 0) FROM channel_messages
		WHERE channel_id=$1 AND (reply_to=$2 OR topic_id=$2) AND id <> $2`, group, top).Scan(&count, &maxID)
	return
}

// Groups the user may link as a discussion group: groups they can edit.
func (s *Service) DiscussionCandidates(ctx context.Context, user int64) ([]Channel, error) {
	rows, err := s.db.Query(ctx, `SELECT `+chanCols+` FROM channels c JOIN channel_members m ON m.channel_id = c.id
		WHERE m.user_id=$1 AND m.status IN ('creator', 'admin') AND c.megagroup AND NOT c.deleted AND c.linked_id IS NULL`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Channel
	var ids []int64
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		list, ids = append(list, c), append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// rows are done, the connection is back in the pool
	ms, err := s.MemberOfMany(ctx, user, ids)
	if err != nil {
		return nil, err
	}
	var out []Channel
	for _, c := range list {
		if c.can(ms[c.ID], "change_info") {
			out = append(out, c)
		}
	}
	return out, nil
}
