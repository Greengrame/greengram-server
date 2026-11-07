package bots

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// InlineTimeout is how long "@bot query" waits for answerInlineQuery.
const InlineTimeout = 10 * time.Second

// InlineResult is one result as the bot gave it, checked by answerInlineQuery.
// Text stays in the bot's own form until it's sent (botapi.InlineMessage).
type InlineResult struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	URL         string          `json:"url,omitempty"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode,omitempty"`
	Entities    json.RawMessage `json:"entities,omitempty"`
	NoWebpage   bool            `json:"no_webpage,omitempty"`
	Markup      json.RawMessage `json:"markup,omitempty"`
}

// InlineAnswer is answerInlineQuery's result list and what goes with it.
type InlineAnswer struct {
	Results     []InlineResult `json:"results"`
	NextOffset  string         `json:"next_offset,omitempty"`
	ButtonText  string         `json:"button_text,omitempty"`
	ButtonStart string         `json:"button_start,omitempty"`
	ButtonApp   string         `json:"button_app,omitempty"`
}

// InlineQuery is a person's "@bot query"; Answer is set once the bot answered.
type InlineQuery struct {
	ID       int64
	Bot      int64
	User     int64
	Query    string
	Offset   string
	ChatType string
	Answered bool
	Answer   InlineAnswer
	Personal bool
	Cache    time.Time
}

const inlineCols = "id, bot_id, user_id, query, query_offset, chat_type, answered, answer, personal, coalesce(cache_until, 'epoch')"

func scanInline(row pgx.Row) (InlineQuery, error) {
	var q InlineQuery
	var raw []byte
	err := row.Scan(&q.ID, &q.Bot, &q.User, &q.Query, &q.Offset, &q.ChatType, &q.Answered, &raw, &q.Personal, &q.Cache)
	if err != nil {
		return q, err
	}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &q.Answer)
	}
	return q, err
}

// AskInline records a query and queues it for the bot.
func (s *Service) AskInline(ctx context.Context, q InlineQuery) (int64, error) {
	var b [8]byte
	rand.Read(b[:])
	q.ID = int64(binary.LittleEndian.Uint64(b[:]) >> 1)
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO bot_inline_queries(id, bot_id, user_id, query, query_offset, chat_type) VALUES ($1, $2, $3, $4, $5, $6)",
			q.ID, q.Bot, q.User, q.Query, q.Offset, q.ChatType); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO bot_updates(bot_id, kind, chat_id, query_id) VALUES ($1, 'inline_query', $2, $3)", q.Bot, q.User, q.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", channel, strconv.FormatInt(q.Bot, 10))
		return err
	})
	return q.ID, err
}

// CachedInline is an earlier answer to the same query still within its
// cache_time; a personal one only counts for the person who asked.
func (s *Service) CachedInline(ctx context.Context, bot, user int64, query, offset string) (InlineQuery, bool, error) {
	q, err := scanInline(s.db.QueryRow(ctx, "SELECT "+inlineCols+` FROM bot_inline_queries
		WHERE bot_id=$1 AND query=$2 AND query_offset=$3 AND answered AND cache_until > now() AND (NOT personal OR user_id=$4)
		ORDER BY cache_until DESC LIMIT 1`, bot, query, offset, user))
	if errors.Is(err, pgx.ErrNoRows) {
		return q, false, nil
	}
	return q, err == nil, err
}

func (s *Service) InlineQueryByID(ctx context.Context, id int64) (InlineQuery, bool, error) {
	q, err := scanInline(s.db.QueryRow(ctx, "SELECT "+inlineCols+" FROM bot_inline_queries WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return q, false, nil
	}
	return q, err == nil, err
}

// AnswerInline stores the bot's answer, once.
func (s *Service) AnswerInline(ctx context.Context, bot, id int64, a InlineAnswer, personal bool, cacheSeconds int) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE bot_inline_queries SET answered=true, answer=$3, personal=$4, cache_until=now() + $5 * interval '1 second'
			WHERE id=$1 AND bot_id=$2 AND NOT answered AND created_at > now() - interval '15 minutes'`, id, bot, raw, personal, cacheSeconds)
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

// WaitInline blocks until the bot answers or timeout passes; ok is false on timeout.
func (s *Service) WaitInline(ctx context.Context, id int64, timeout time.Duration) (InlineQuery, bool, error) {
	var q InlineQuery
	err := s.notify.poll(ctx, "q"+strconv.FormatInt(id, 10), time.Now().Add(timeout), func() (bool, error) {
		var err error
		q, _, err = s.InlineQueryByID(ctx, id)
		return q.Answered, err
	})
	return q, q.Answered, err
}

// Chosen is chosen_inline_result: what a person picked and sent.
type Chosen struct {
	ResultID string `json:"result_id"`
	User     int64  `json:"user"`
	Query    string `json:"query"`
	InlineID string `json:"inline_message_id,omitempty"`
}

// QueueChosen tells the bot a result was sent.
func (s *Service) QueueChosen(ctx context.Context, bot int64, c Chosen) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO bot_updates(bot_id, kind, chat_id, payload) VALUES ($1, 'chosen_inline_result', $2, $3)", bot, c.User, raw); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", channel, strconv.FormatInt(bot, 10))
		return err
	})
}

// A message sent via a bot isn't the bot's and may sit in a chat it isn't in, so
// the bot edits it by an id naming the message rather than a chat. Whoever holds
// one still only edits messages sent via their own bot: the edit checks that.

// PrivateInlineID names a private message by the id its two copies share.
func PrivateInlineID(shared int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("p" + strconv.FormatInt(shared, 10)))
}

// ChannelInlineID names a message in a group or channel.
func ChannelInlineID(ch int64, msg int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("c" + strconv.FormatInt(ch, 10) + ":" + strconv.Itoa(msg)))
}

// ParseInlineID reads either form back: shared is set for a private message,
// ch and msg for the others.
func ParseInlineID(id string) (shared, ch int64, msg int, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) < 2 {
		return 0, 0, 0, false
	}
	body := string(raw[1:])
	switch raw[0] {
	case 'p':
		shared, err = strconv.ParseInt(body, 10, 64)
		return shared, 0, 0, err == nil
	case 'c':
		a, b, found := strings.Cut(body, ":")
		if !found {
			return 0, 0, 0, false
		}
		if ch, err = strconv.ParseInt(a, 10, 64); err != nil {
			return 0, 0, 0, false
		}
		if msg, err = strconv.Atoi(b); err != nil {
			return 0, 0, 0, false
		}
		return 0, ch, msg, true
	}
	return 0, 0, 0, false
}
