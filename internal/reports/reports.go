// Package reports keeps what people report from the apps for the admins'
// Reports page, and tells the admins in batches.
package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// What was reported.
const (
	KindMessage  = "message"
	KindPeer     = "peer"
	KindPhoto    = "photo"
	KindStory    = "story"
	KindReaction = "reaction"
	KindSpam     = "spam"
)

// PerHour bounds one reporter. Past it a report is answered like any other
// and dropped: a refusal would tell a script when to slow down, and every
// report lands in an admin's queue.
const PerHour = 20

// ErrOwner is a ban asked for on a chat's own owner.
var ErrOwner = errors.New("reports: the owner of a chat can't be banned from it")

// MaxComment is Web A's limit on the comment field.
const MaxComment = 512

// DefaultNoticeGap is the least time between two notices to the admins; the
// reports in between go out together.
const DefaultNoticeGap = 5 * time.Minute

type Service struct {
	db    *pgxpool.Pool
	gap   atomic.Int64
	filed chan struct{}
}

func New(db *pgxpool.Pool) *Service {
	s := &Service{db: db, filed: make(chan struct{}, 1)}
	s.gap.Store(int64(DefaultNoticeGap))
	return s
}

// SetNoticeGap changes the batching window (tests).
func (s *Service) SetNoticeGap(d time.Duration) { s.gap.Store(int64(d)) }

// Filed wakes the notifier after a report went in.
func (s *Service) Filed() <-chan struct{} { return s.filed }

// Snapshot is what the reporter saw, kept with the report.
type Snapshot struct {
	Peer     *PeerShot  `json:"peer,omitempty"`
	Messages []MsgShot  `json:"messages,omitempty"`
	Story    *StoryShot `json:"story,omitempty"`
	Photo    *MediaShot `json:"photo,omitempty"`
	Reaction string     `json:"reaction,omitempty"`
}

// PeerShot is the reported account or chat as it looked.
type PeerShot struct {
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	About    string `json:"about,omitempty"`
	PhotoID  string `json:"photoId,omitempty"`
}

type MsgShot struct {
	ID       int        `json:"id"`
	From     int64      `json:"from"`
	FromName string     `json:"fromName"`
	Post     bool       `json:"post,omitempty"` // sent as the channel
	Date     int        `json:"date"`
	EditDate int        `json:"editDate,omitempty"`
	Text     string     `json:"text"`
	Media    *MediaShot `json:"media,omitempty"`
	Service  bool       `json:"service,omitempty"`
}

type StoryShot struct {
	ID      int        `json:"id"`
	Date    int        `json:"date"`
	Caption string     `json:"caption"`
	Media   *MediaShot `json:"media,omitempty"`
}

// MediaShot points at the file; ids are strings, they don't fit a JS number.
type MediaShot struct {
	Type  string `json:"type"` // photo, sticker, video, gif, voice, audio, document, other
	ID    string `json:"id,omitempty"`
	Mime  string `json:"mime,omitempty"`
	Name  string `json:"name,omitempty"`
	Emoji string `json:"emoji,omitempty"`
	Size  int64  `json:"size,omitempty"`
	What  string `json:"what,omitempty"` // for other: the TL name
}

// Report is one to file.
type Report struct {
	Reporter   int64
	Kind       string
	ChatKind   string // user or channel
	ChatID     int64
	TargetKey  string // u<id> or c<id>
	TargetType string // user, bot, group, channel
	MsgIDs     []int
	SharedIDs  []int64
	StoryID    int
	PhotoID    int64
	Reason     []string
	Comment    string
	Snapshot   Snapshot
}

// UserKey and ChatKey name a target.
func UserKey(id int64) string { return fmt.Sprintf("u%d", id) }
func ChatKey(id int64) string { return fmt.Sprintf("c%d", id) }

// File stores r. It returns false, with no error, for a report that wasn't
// stored: over PerHour, or the same reporter's same report still open.
// Both checks are in the insert so a burst can't race past them.
func (s *Service) File(ctx context.Context, r Report) (bool, error) {
	snap, err := json.Marshal(r.Snapshot)
	if err != nil {
		return false, err
	}
	msgs := r.MsgIDs
	if msgs == nil {
		msgs = []int{}
	}
	shared := r.SharedIDs
	if shared == nil {
		shared = []int64{}
	}
	var story *int
	if r.StoryID != 0 {
		story = &r.StoryID
	}
	var photo *int64
	if r.PhotoID != 0 {
		photo = &r.PhotoID
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO content_reports(reporter_id, kind, chat_kind, chat_id, target_key, target_type,
			msg_ids, shared_ids, story_id, photo_id, reason, comment, snapshot)
		SELECT $1::bigint, $2::text, $3::text, $4::bigint, $5::text, $6::text, $7::int[], $8::bigint[], $9::int, $10::bigint, $11::text[], $12::text, $13::jsonb
		WHERE (SELECT count(*) FROM content_reports WHERE reporter_id = $1::bigint AND created_at > now() - interval '1 hour') < $14
		AND NOT EXISTS(SELECT 1 FROM content_reports WHERE reporter_id = $1::bigint AND target_key = $5::text AND status = 'open'
			AND kind = $2::text AND chat_id = $4::bigint AND msg_ids = $7::int[] AND reason = $11::text[] AND story_id IS NOT DISTINCT FROM $9::int
			AND photo_id IS NOT DISTINCT FROM $10::bigint)`,
		r.Reporter, r.Kind, r.ChatKind, r.ChatID, r.TargetKey, r.TargetType, msgs, shared, story, photo, r.Reason, r.Comment, snap, PerHour)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	select {
	case s.filed <- struct{}{}:
	default:
	}
	return true, nil
}

// Pending is one target in a notice.
type Pending struct {
	TargetKey string
	Reports   int
	Reason    []string // the newest one's
}

// TakeNotice claims every report nobody was told about, if the last notice
// is at least the gap old. Otherwise it says how long to wait; 0 and no
// list means there's nothing to tell.
func (s *Service) TakeNotice(ctx context.Context) ([]Pending, time.Duration, error) {
	gap := time.Duration(s.gap.Load())
	var wait float64
	var any bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content_reports WHERE notified_at IS NULL),
		coalesce(extract(epoch FROM (SELECT max(notified_at) FROM content_reports WHERE notified_at IS NOT NULL)
			+ make_interval(secs => $1::double precision) - now()), 0)`, gap.Seconds()).Scan(&any, &wait)
	if err != nil || !any {
		return nil, 0, err
	}
	if wait > 0 {
		return nil, time.Duration(wait*float64(time.Second)) + 10*time.Millisecond, nil
	}
	// closed before anyone was told: claimed, not listed
	rows, err := s.db.Query(ctx, `UPDATE content_reports SET notified_at = now() WHERE notified_at IS NULL
		RETURNING id, target_key, reason, status = 'open'`)
	if err != nil {
		return nil, 0, err
	}
	type row struct {
		id     int64
		key    string
		reason []string
		open   bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		err := r.Scan(&v.id, &v.key, &v.reason, &v.open)
		return v, err
	})
	if err != nil {
		return nil, 0, err
	}
	var out []Pending
	at := map[string]int{}
	newest := map[string]int64{}
	for _, v := range list {
		if !v.open {
			continue
		}
		i, ok := at[v.key]
		if !ok {
			i = len(out)
			at[v.key] = i
			out = append(out, Pending{TargetKey: v.key})
		}
		out[i].Reports++
		if v.id > newest[v.key] {
			newest[v.key] = v.id
			out[i].Reason = v.reason
		}
	}
	return out, 0, nil
}

// TargetName names a target for a notice.
func (s *Service) TargetName(ctx context.Context, key string) string {
	var id int64
	if _, err := fmt.Sscanf(key[1:], "%d", &id); err != nil {
		return key
	}
	var name, username string
	var err error
	if key[0] == 'u' {
		err = s.db.QueryRow(ctx, "SELECT name, username FROM users WHERE id=$1", id).Scan(&name, &username)
	} else {
		var megagroup bool
		err = s.db.QueryRow(ctx, "SELECT title, coalesce(username, ''), megagroup FROM channels WHERE id=$1", id).Scan(&name, &username, &megagroup)
		if err == nil {
			kind := "channel"
			if megagroup {
				kind = "group"
			}
			name = fmt.Sprintf("%s %q", kind, name)
		}
	}
	if err != nil {
		return key
	}
	if username != "" {
		name += " (@" + username + ")"
	}
	return fmt.Sprintf("%s, id %d", name, id)
}
