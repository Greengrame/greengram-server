// Package messages is private messaging: per-user boxes, the update journal
// (pts), dialogs and contacts. It speaks TL types directly; what it returns
// is what clients get.
package messages

import (
	"context"
	"errors"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db, now: time.Now} }

// Event is an update for one user, already in their journal (or, for
// typing and status, not journaled at all). The caller pushes it.
type Event struct {
	UserID int64
	Update tg.UpdateClass
}

func (s *Service) unix() int { return int(s.now().Unix()) }

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

// lockBoxes takes the box rows of every user involved, in id order, so two
// people writing to each other at once can't deadlock.
func lockBoxes(ctx context.Context, tx pgx.Tx, users ...int64) error {
	if _, err := tx.Exec(ctx, "INSERT INTO user_boxes(user_id) SELECT unnest($1::bigint[]) ON CONFLICT DO NOTHING", users); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SELECT 1 FROM user_boxes WHERE user_id = ANY($1) ORDER BY user_id FOR UPDATE", users)
	return err
}

func nextID(ctx context.Context, tx pgx.Tx, user int64) (int, error) {
	var id int
	err := tx.QueryRow(ctx, "UPDATE user_boxes SET next_id = next_id + 1 WHERE user_id=$1 RETURNING next_id - 1", user).Scan(&id)
	return id, err
}

// journal moves the user's pts by count and stores the update build makes
// for the new pts. Box must be locked.
func (s *Service) journal(ctx context.Context, tx pgx.Tx, user int64, count int, build func(pts int) tg.UpdateClass) (tg.UpdateClass, error) {
	var pts int
	if err := tx.QueryRow(ctx, "UPDATE user_boxes SET pts = pts + $2 WHERE user_id=$1 RETURNING pts", user, count).Scan(&pts); err != nil {
		return nil, err
	}
	u := build(pts)
	var b bin.Buffer
	if err := u.Encode(&b); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, "INSERT INTO update_journal(user_id, pts, pts_count, date, body) VALUES ($1, $2, $3, $4, $5)", user, pts, count, s.unix(), b.Buf)
	return u, err
}

// State is the user's journal head, for updates.getState.
func (s *Service) State(ctx context.Context, user int64) (tg.UpdatesState, error) {
	st := tg.UpdatesState{Date: s.unix()}
	err := s.db.QueryRow(ctx, "SELECT pts FROM user_boxes WHERE user_id=$1", user).Scan(&st.Pts)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err == nil {
		st.UnreadCount, err = s.unreadTotal(ctx, user)
	}
	return st, err
}

func (s *Service) unreadTotal(ctx context.Context, user int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM messages m JOIN dialogs d ON d.owner_id = m.owner_id AND d.peer_id = m.peer_id
		WHERE m.owner_id=$1 AND NOT m.out AND m.id > d.read_inbox_max`, user).Scan(&n)
	return n, err
}

// differenceLimit caps one getDifference answer; the client asks again.
const differenceLimit = 1000

// Difference is everything in the journal after pts.
type Difference struct {
	Updates []tg.UpdateClass
	State   tg.UpdatesState
	More    bool // a slice; call again from State.Pts
	TooLong bool // pts is older than the journal keeps
}

func (s *Service) Difference(ctx context.Context, user int64, pts int) (Difference, error) {
	var d Difference
	st, err := s.State(ctx, user)
	if err != nil {
		return d, err
	}
	d.State = st
	// ahead of us: state from another server (the old one before the move),
	// the client has to start over or it drops our updates as seen
	if pts > st.Pts {
		d.TooLong = true
		return d, nil
	}
	if pts == st.Pts {
		return d, nil
	}
	var oldest *int
	if err := s.db.QueryRow(ctx, "SELECT min(pts - pts_count) FROM update_journal WHERE user_id=$1", user).Scan(&oldest); err != nil {
		return d, err
	}
	if oldest != nil && pts < *oldest {
		d.TooLong = true
		return d, nil
	}
	rows, err := s.db.Query(ctx, "SELECT pts, body FROM update_journal WHERE user_id=$1 AND pts > $2 ORDER BY pts LIMIT $3", user, pts, differenceLimit+1)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	last := pts
	for rows.Next() {
		var p int
		var body []byte
		if err := rows.Scan(&p, &body); err != nil {
			return d, err
		}
		if len(d.Updates) == differenceLimit {
			d.More = true
			break
		}
		u, err := tg.DecodeUpdate(&bin.Buffer{Buf: body})
		if err != nil {
			return d, err
		}
		d.Updates = append(d.Updates, u)
		last = p
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	// the head was read before the rows; a write in between may have
	// added some, and the state must cover what we hand out
	if d.More || last > d.State.Pts {
		d.State.Pts = last
	}
	return d, nil
}

var (
	errPeerInvalid    = tgerr.New(400, "PEER_ID_INVALID")
	errMessageInvalid = tgerr.New(400, "MESSAGE_ID_INVALID")
	errEmpty          = tgerr.New(400, "MESSAGE_EMPTY")
	errTooLong        = tgerr.New(400, "MESSAGE_TOO_LONG")
)
