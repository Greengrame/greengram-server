package messages

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// NotifyKey says whose notification settings: a scope default ("users",
// "chats", "broadcasts") or one chat ("peer" with User or Channel).
type NotifyKey struct {
	Scope         string
	User, Channel int64
}

// NotifyEntry is one stored setting.
type NotifyEntry struct {
	NotifyKey
	Settings tg.PeerNotifySettings
}

func (s *Service) SetNotify(ctx context.Context, owner int64, k NotifyKey, v tg.PeerNotifySettings) error {
	var b bin.Buffer
	if err := v.Encode(&b); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO notify_settings(user_id, scope, peer_user, peer_channel, body) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, scope, peer_user, peer_channel) DO UPDATE SET body = excluded.body`, owner, k.Scope, k.User, k.Channel, b.Buf)
	return err
}

// Notify is a stored setting; ok is false when the user never set one.
func (s *Service) Notify(ctx context.Context, owner int64, k NotifyKey) (tg.PeerNotifySettings, bool, error) {
	var v tg.PeerNotifySettings
	var body []byte
	err := s.db.QueryRow(ctx, "SELECT body FROM notify_settings WHERE user_id=$1 AND scope=$2 AND peer_user=$3 AND peer_channel=$4",
		owner, k.Scope, k.User, k.Channel).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	return v, true, v.Decode(&bin.Buffer{Buf: body})
}

// NotifyExceptions are the user's per-chat settings.
func (s *Service) NotifyExceptions(ctx context.Context, owner int64) ([]NotifyEntry, error) {
	rows, err := s.db.Query(ctx, "SELECT scope, peer_user, peer_channel, body FROM notify_settings WHERE user_id=$1 AND scope='peer'", owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (NotifyEntry, error) {
		var e NotifyEntry
		var body []byte
		if err := r.Scan(&e.Scope, &e.User, &e.Channel, &body); err != nil {
			return e, err
		}
		return e, e.Settings.Decode(&bin.Buffer{Buf: body})
	})
}

// ResetNotify drops every setting of the user's.
func (s *Service) ResetNotify(ctx context.Context, owner int64) error {
	_, err := s.db.Exec(ctx, "DELETE FROM notify_settings WHERE user_id=$1", owner)
	return err
}
