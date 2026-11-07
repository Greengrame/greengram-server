package messages

import (
	"context"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// DraftKey is where a saved draft belongs: a private chat (User) or a
// channel, and a comment thread or 0.
type DraftKey struct {
	User, Channel int64
	Thread        int
}

// SavedDraft is one draft of a user's.
type SavedDraft struct {
	DraftKey
	Draft tg.DraftMessage
}

// SaveDraft stores what the user is typing somewhere.
func (s *Service) SaveDraft(ctx context.Context, owner int64, k DraftKey, d tg.DraftMessage) error {
	var b bin.Buffer
	if err := d.Encode(&b); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO drafts(user_id, peer_user, peer_channel, thread, body) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, peer_user, peer_channel, thread) DO UPDATE SET body = excluded.body`, owner, k.User, k.Channel, k.Thread, b.Buf)
	return err
}

// DropDraft removes a draft; false if there was none.
func (s *Service) DropDraft(ctx context.Context, owner int64, k DraftKey) (bool, error) {
	tag, err := s.db.Exec(ctx, "DELETE FROM drafts WHERE user_id=$1 AND peer_user=$2 AND peer_channel=$3 AND thread=$4", owner, k.User, k.Channel, k.Thread)
	return tag.RowsAffected() > 0, err
}

func scanDrafts(rows pgx.Rows) ([]SavedDraft, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SavedDraft, error) {
		var d SavedDraft
		var body []byte
		if err := r.Scan(&d.User, &d.Channel, &d.Thread, &body); err != nil {
			return d, err
		}
		err := d.Draft.Decode(&bin.Buffer{Buf: body})
		return d, err
	})
}

// Drafts are all of a user's drafts.
func (s *Service) Drafts(ctx context.Context, owner int64) ([]SavedDraft, error) {
	rows, err := s.db.Query(ctx, "SELECT peer_user, peer_channel, thread, body FROM drafts WHERE user_id=$1", owner)
	if err != nil {
		return nil, err
	}
	return scanDrafts(rows)
}

// ClearDrafts removes every draft and says which there were.
func (s *Service) ClearDrafts(ctx context.Context, owner int64) ([]SavedDraft, error) {
	rows, err := s.db.Query(ctx, "DELETE FROM drafts WHERE user_id=$1 RETURNING peer_user, peer_channel, thread, body", owner)
	if err != nil {
		return nil, err
	}
	return scanDrafts(rows)
}
