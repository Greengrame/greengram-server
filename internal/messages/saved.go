package messages

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SavedDialog is one source in Saved Messages (see Msg.SavedPeer).
type SavedDialog struct {
	Peer   int64
	TopID  int
	Pinned bool
}

const savedCols = `t.saved_peer, t.top, p.saved_peer IS NOT NULL`

// savedTops is every saved dialog with its newest message.
const savedTops = `(SELECT saved_peer, max(id) AS top FROM messages WHERE owner_id=$1 AND saved_peer IS NOT NULL GROUP BY saved_peer) t
	LEFT JOIN saved_dialog_pins p ON p.owner_id=$1 AND p.saved_peer = t.saved_peer`

func scanSaved(rows pgx.Rows) ([]SavedDialog, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SavedDialog, error) {
		var d SavedDialog
		err := r.Scan(&d.Peer, &d.TopID, &d.Pinned)
		return d, err
	})
}

// SavedDialogs pages the saved dialogs, newest first, below offsetID (a
// top message id) when given; total counts them all.
func (s *Service) SavedDialogs(ctx context.Context, owner int64, offsetID, limit int, excludePinned bool) ([]SavedDialog, []Msg, int, error) {
	limit = clampLimit(limit, 100)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(DISTINCT saved_peer) FROM messages WHERE owner_id=$1 AND saved_peer IS NOT NULL", owner).Scan(&total); err != nil {
		return nil, nil, 0, err
	}
	where := "TRUE"
	if excludePinned {
		where = "p.saved_peer IS NULL"
	}
	if offsetID <= 0 {
		offsetID = 1 << 30
	}
	order := " ORDER BY t.top DESC"
	if !excludePinned && offsetID == 1<<30 {
		order = " ORDER BY p.pinned_at DESC NULLS LAST, t.top DESC"
	}
	rows, err := s.db.Query(ctx, "SELECT "+savedCols+" FROM "+savedTops+" WHERE "+where+" AND t.top < $2"+order+" LIMIT $3", owner, offsetID, limit)
	if err != nil {
		return nil, nil, 0, err
	}
	list, err := scanSaved(rows)
	if err != nil {
		return nil, nil, 0, err
	}
	tops, err := s.savedTopMsgs(ctx, owner, list)
	return list, tops, total, err
}

// PinnedSavedDialogs in pin order.
func (s *Service) PinnedSavedDialogs(ctx context.Context, owner int64) ([]SavedDialog, []Msg, error) {
	rows, err := s.db.Query(ctx, "SELECT "+savedCols+" FROM "+savedTops+" WHERE p.saved_peer IS NOT NULL ORDER BY p.pinned_at DESC", owner)
	if err != nil {
		return nil, nil, err
	}
	list, err := scanSaved(rows)
	if err != nil {
		return nil, nil, err
	}
	tops, err := s.savedTopMsgs(ctx, owner, list)
	return list, tops, err
}

func (s *Service) savedTopMsgs(ctx context.Context, owner int64, list []SavedDialog) ([]Msg, error) {
	if len(list) == 0 {
		return nil, nil
	}
	want := make([]int, len(list))
	for i, d := range list {
		want[i] = d.TopID
	}
	return s.Get(ctx, owner, want)
}

// HasSaved is whether the saved dialog has any messages.
func (s *Service) HasSaved(ctx context.Context, owner, peer int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE owner_id=$1 AND saved_peer=$2)", owner, peer).Scan(&ok)
	return ok, err
}

// PinSaved pins or unpins a saved dialog.
func (s *Service) PinSaved(ctx context.Context, owner, peer int64, pinned bool) error {
	if !pinned {
		_, err := s.db.Exec(ctx, "DELETE FROM saved_dialog_pins WHERE owner_id=$1 AND saved_peer=$2", owner, peer)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO saved_dialog_pins(owner_id, saved_peer, pinned_at)
		VALUES ($1, $2, coalesce((SELECT max(pinned_at) FROM saved_dialog_pins WHERE owner_id=$1), 0) + 1)
		ON CONFLICT (owner_id, saved_peer) DO NOTHING`, owner, peer)
	return err
}

// ReorderSavedPins sets the pin order, first = top. force unpins anything
// not listed.
func (s *Service) ReorderSavedPins(ctx context.Context, owner int64, order []int64, force bool) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if force {
			if _, err := tx.Exec(ctx, "DELETE FROM saved_dialog_pins WHERE owner_id=$1 AND NOT (saved_peer = ANY($2))", owner, order); err != nil {
				return err
			}
		}
		for i, p := range order {
			if _, err := tx.Exec(ctx, `INSERT INTO saved_dialog_pins(owner_id, saved_peer, pinned_at) VALUES ($1, $2, $3)
				ON CONFLICT (owner_id, saved_peer) DO UPDATE SET pinned_at = excluded.pinned_at`, owner, p, len(order)-i); err != nil {
				return err
			}
		}
		return nil
	})
}

// SavedIDs are the ids in one saved dialog, for deleting it: up to maxID
// (0 = all) and within the dates when given.
func (s *Service) SavedIDs(ctx context.Context, owner, peer int64, maxID, minDate, maxDate int) ([]int, error) {
	q := "SELECT id FROM messages WHERE owner_id=$1 AND saved_peer=$2"
	args := []any{owner, peer}
	if maxID > 0 {
		args = append(args, maxID)
		q += " AND id <= $" + itoa(len(args))
	}
	if minDate > 0 {
		args = append(args, minDate)
		q += " AND date >= $" + itoa(len(args))
	}
	if maxDate > 0 {
		args = append(args, maxDate)
		q += " AND date <= $" + itoa(len(args))
	}
	rows, err := s.db.Query(ctx, q+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}
