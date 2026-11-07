package bots

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Update is one queued getUpdates entry; the caller renders it.
type Update struct {
	ID      int64
	Kind    string // message, edited_message, callback_query, inline_query, chosen_inline_result
	ChatID  int64
	Channel bool
	MsgID   int
	QueryID int64
	Payload []byte
}

// Keep is how long unclaimed updates wait, as on Telegram.
const Keep = 24 * time.Hour

// Updates returns up to limit updates from offset on. Like the Bot API,
// asking from offset confirms (drops) everything before it; a negative
// offset means the last -offset updates.
func (s *Service) Updates(ctx context.Context, bot int64, offset int64, limit int) ([]Update, error) {
	if _, err := s.db.Exec(ctx, "DELETE FROM bot_updates WHERE bot_id=$1 AND (id < $2 OR created_at < now() - $3 * interval '1 second')",
		bot, max(offset, 0), int(Keep/time.Second)); err != nil {
		return nil, err
	}
	q := "SELECT id, kind, chat_id, channel, coalesce(msg_id, 0), coalesce(query_id, 0), payload FROM bot_updates WHERE bot_id=$1 AND id >= $2 ORDER BY id LIMIT $3"
	args := []any{bot, offset, limit}
	if offset < 0 {
		q = `SELECT * FROM (SELECT id, kind, chat_id, channel, coalesce(msg_id, 0), coalesce(query_id, 0), payload FROM bot_updates
			WHERE bot_id=$1 ORDER BY id DESC LIMIT $2) t ORDER BY id`
		args = []any{bot, min(-offset, int64(limit))}
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var u Update
		if err := rows.Scan(&u.ID, &u.Kind, &u.ChatID, &u.Channel, &u.MsgID, &u.QueryID, &u.Payload); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// WaitUpdates is Updates as a long poll: it returns as soon as there is
// something, or empty after timeout.
func (s *Service) WaitUpdates(ctx context.Context, bot int64, offset int64, limit int, timeout time.Duration) ([]Update, error) {
	var out []Update
	err := s.notify.poll(ctx, strconv.FormatInt(bot, 10), time.Now().Add(timeout), func() (bool, error) {
		var err error
		out, err = s.Updates(ctx, bot, offset, limit)
		return len(out) > 0, err
	})
	return out, err
}

// Prune drops what nobody will read: updates older than Keep (a bot that
// never polls only gets pruned here), answered or stale presses. Built-in
// bots drop theirs as they go.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, "DELETE FROM bot_updates WHERE created_at < now() - $1 * interval '1 second'", int(Keep/time.Second))
	if err != nil {
		return 0, err
	}
	if _, err = s.db.Exec(ctx, "DELETE FROM bot_callbacks WHERE created_at < now() - interval '1 hour'"); err != nil {
		return tag.RowsAffected(), err
	}
	if _, err = s.db.Exec(ctx, "DELETE FROM bot_inline_queries WHERE created_at < now() - interval '1 hour' AND coalesce(cache_until, 'epoch') < now()"); err != nil {
		return tag.RowsAffected(), err
	}
	_, err = s.db.Exec(ctx, "DELETE FROM bot_loop_guard WHERE minute < now() - interval '2 minutes'")
	return tag.RowsAffected(), err
}

// PruneEvery runs Prune until ctx ends.
func (s *Service) PruneEvery(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.Prune(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("bots: prune", "err", err)
			} else if n > 0 {
				slog.Info("bots: pruned updates", "rows", n)
			}
		}
	}
}

// Pending counts what getUpdates would hand out, for getWebhookInfo.
func (s *Service) Pending(ctx context.Context, bot int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, "SELECT count(*) FROM bot_updates WHERE bot_id=$1", bot).Scan(&n)
	return n, err
}

// Callback is a press on an inline button.
type Callback struct {
	ID      int64
	Bot     int64
	User    int64
	ChatID  int64 // the user (private chat) or the channel
	Channel bool
	MsgID   int // as the bot sees it
	Data    []byte
	Created time.Time
	// set for a message sent via the bot: the bot edits it by this
	InlineID string
	// the bot's answer
	Answered  bool
	Text      string
	Alert     bool
	URL       string
	CacheTime int
}

// CallbackTimeout is how long a press waits for the bot to answer before
// the client is told BOT_RESPONSE_TIMEOUT.
const CallbackTimeout = 10 * time.Second

// Press records a callback query and queues it for the bot.
func (s *Service) Press(ctx context.Context, c Callback) (int64, error) {
	var b [8]byte
	rand.Read(b[:])
	c.ID = int64(binary.LittleEndian.Uint64(b[:]) >> 1)
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO bot_callbacks(id, bot_id, user_id, chat_id, channel, msg_id, data, inline_message_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			c.ID, c.Bot, c.User, c.ChatID, c.Channel, c.MsgID, c.Data, c.InlineID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO bot_updates(bot_id, kind, chat_id, channel, msg_id, query_id) VALUES ($1, 'callback_query', $2, $3, $4, $5)",
			c.Bot, c.ChatID, c.Channel, c.MsgID, c.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", channel, strconv.FormatInt(c.Bot, 10))
		return err
	})
	// old presses are only kept for the bot to answer
	s.db.Exec(ctx, "DELETE FROM bot_callbacks WHERE created_at < now() - interval '1 hour'")
	return c.ID, err
}

func (s *Service) Callback(ctx context.Context, id int64) (Callback, bool, error) {
	var c Callback
	err := s.db.QueryRow(ctx, `SELECT id, bot_id, user_id, chat_id, channel, msg_id, data, created_at, answered, text, alert, url, cache_time, inline_message_id
		FROM bot_callbacks WHERE id=$1`, id).Scan(&c.ID, &c.Bot, &c.User, &c.ChatID, &c.Channel, &c.MsgID, &c.Data, &c.Created,
		&c.Answered, &c.Text, &c.Alert, &c.URL, &c.CacheTime, &c.InlineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

var ErrQueryInvalid = errors.New("query is too old and response timeout expired or query ID is invalid")

// Answer stores the bot's answer to a press, once.
func (s *Service) Answer(ctx context.Context, bot, id int64, text string, alert bool, url string, cacheTime int) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE bot_callbacks SET answered=true, text=$3, alert=$4, url=$5, cache_time=$6
			WHERE id=$1 AND bot_id=$2 AND NOT answered AND created_at > now() - interval '15 minutes'`, id, bot, text, alert, url, cacheTime)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrQueryInvalid
		}
		_, err = tx.Exec(ctx, "SELECT pg_notify($1, $2)", channel, "q"+strconv.FormatInt(id, 10))
		return err
	})
}

// WaitAnswer blocks until the bot answers the press or timeout passes;
// ok is false on timeout.
func (s *Service) WaitAnswer(ctx context.Context, id int64, timeout time.Duration) (Callback, bool, error) {
	var c Callback
	err := s.notify.poll(ctx, "q"+strconv.FormatInt(id, 10), time.Now().Add(timeout), func() (bool, error) {
		var err error
		c, _, err = s.Callback(ctx, id)
		return c.Answered, err
	})
	return c, c.Answered, err
}

// Drop removes updates the bot won't be shown (filtered out, or pointing
// at a deleted message).
func (s *Service) Drop(ctx context.Context, bot int64, list []int64) error {
	_, err := s.db.Exec(ctx, "DELETE FROM bot_updates WHERE bot_id=$1 AND id = ANY($2)", bot, list)
	return err
}

// DropAll is deleteWebhook with drop_pending_updates.
func (s *Service) DropAll(ctx context.Context, bot int64) error {
	_, err := s.db.Exec(ctx, "DELETE FROM bot_updates WHERE bot_id=$1", bot)
	return err
}
