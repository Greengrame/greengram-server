package media

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Profile photos. users.photo_id is the one everybody sees; user_photos is
// the list photos.getUserPhotos pages through, newest first.

// SetUserPhoto makes photo the user's picture and moves it to the top.
func (s *Service) SetUserPhoto(ctx context.Context, user, photo int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_photos(user_id, photo_id, date) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, photo_id) DO UPDATE SET date = excluded.date`, user, photo, s.unix()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE users SET photo_id=$2 WHERE id=$1", user, photo)
		return err
	})
}

// HadUserPhoto is whether the user has, or ever kept, a picture. One that
// was hidden on purpose counts: photo_id is empty but the list isn't.
func (s *Service) HadUserPhoto(ctx context.Context, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND photo_id IS NOT NULL)
		OR EXISTS(SELECT 1 FROM user_photos WHERE user_id=$1)`, user).Scan(&ok)
	return ok, err
}

// PhotoFile is the bytes of the largest size of photo that fits in box.
func (s *Service) PhotoFile(ctx context.Context, photo int64, box int) ([]byte, error) {
	p, ok, err := s.Photo(ctx, photo)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, refuse(404, "Photo not found.")
	}
	z := p.Fit(box)
	return s.blobs.Read(ctx, z.Key, 0, z.Bytes)
}

// ClearUserPhoto hides the picture; it stays in the list.
func (s *Service) ClearUserPhoto(ctx context.Context, user int64) error {
	_, err := s.db.Exec(ctx, "UPDATE users SET photo_id=NULL WHERE id=$1", user)
	return err
}

func (s *Service) IsUserPhoto(ctx context.Context, user, photo int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM user_photos WHERE user_id=$1 AND photo_id=$2)", user, photo).Scan(&ok)
	return ok, err
}

// UserPhotos is one page of the user's photos, newest first, and how many
// there are. Photo ids are random, so max_id names a place in the list:
// paging starts after that photo, and offset -1 starts at it (how clients
// refetch one photo).
func (s *Service) UserPhotos(ctx context.Context, user int64, offset, limit int, maxID int64) ([]Photo, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM user_photos WHERE user_id=$1", user).Scan(&total); err != nil {
		return nil, 0, err
	}
	if maxID > 0 {
		var before int
		err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM user_photos o WHERE o.user_id=$1 AND (o.date, o.photo_id) > (a.date, a.photo_id))
			FROM user_photos a WHERE a.user_id=$1 AND a.photo_id=$2`, user, maxID).Scan(&before)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, total, nil
		}
		if err != nil {
			return nil, 0, err
		}
		offset += before + 1
	}
	rows, err := s.db.Query(ctx, "SELECT photo_id FROM user_photos WHERE user_id=$1 ORDER BY date DESC, photo_id DESC OFFSET $2 LIMIT $3", user, max(offset, 0), limit)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, 0, err
	}
	photos, err := s.Photos(ctx, list)
	return photos, total, err
}

// DeleteUserPhotos drops photos from the list; if the current one goes,
// the next newest takes its place.
func (s *Service) DeleteUserPhotos(ctx context.Context, user int64, list []int64) ([]int64, error) {
	var gone []int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "DELETE FROM user_photos WHERE user_id=$1 AND photo_id = ANY($2) RETURNING photo_id", user, list)
		if err != nil {
			return err
		}
		if gone, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil || len(gone) == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE users SET photo_id =
			(SELECT photo_id FROM user_photos WHERE user_id=$1 ORDER BY date DESC, photo_id DESC LIMIT 1)
			WHERE id=$1 AND photo_id = ANY($2)`, user, gone)
		return err
	})
	if gone == nil {
		gone = []int64{}
	}
	return gone, err
}
