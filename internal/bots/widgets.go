package bots

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrWidgetRateLimited = errors.New("widget rate limit exceeded")

type Widget struct {
	ID           string
	Bot          int64
	HTML         string
	Width        int
	Height       int
	PublicData   string
	Shared       bool
	AllowQueries bool
	CacheTime    int
}

type WidgetMessage struct {
	ID        string
	Receiver  int64
	MessageID int
	ChatData  string
	Widget    Widget
}

func widgetKey() (string, error) {
	var raw [24]byte
	var err error
	_, err = rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), err
}

func (s *Service) CreateWidget(ctx context.Context, w Widget) (Widget, error) {
	var err error
	w.ID, err = widgetKey()
	if err != nil {
		return w, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO bot_widgets(id,bot_id,html,width,height,public_data,shared,allow_queries) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, w.ID, w.Bot, w.HTML, w.Width, w.Height, w.PublicData, w.Shared, w.AllowQueries)
	w.CacheTime = 30
	return w, err
}

func (s *Service) Widget(ctx context.Context, bot int64, id string) (Widget, error) {
	var w Widget
	var err error
	err = s.db.QueryRow(ctx, `SELECT id,bot_id,html,width,height,public_data,shared,allow_queries,cache_time FROM bot_widgets WHERE id=$1 AND bot_id=$2`, id, bot).Scan(&w.ID, &w.Bot, &w.HTML, &w.Width, &w.Height, &w.PublicData, &w.Shared, &w.AllowQueries, &w.CacheTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrQueryInvalid
	}
	return w, err
}

func (s *Service) CreateWidgetMessage(ctx context.Context, w Widget, receiver int64, data string) (WidgetMessage, error) {
	var message = WidgetMessage{Widget: w, Receiver: receiver, ChatData: data}
	var err error
	message.ID, err = widgetKey()
	if err != nil {
		return message, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO bot_widget_messages(id,widget_id,receiver_id,chat_data) VALUES($1,$2,$3,$4)`, message.ID, w.ID, receiver, data)
	return message, err
}

func (s *Service) SetWidgetMessageID(ctx context.Context, id string, message int) error {
	var err error
	_, err = s.db.Exec(ctx, "UPDATE bot_widget_messages SET message_id=$2 WHERE id=$1", id, message)
	return err
}

func (s *Service) WidgetMessage(ctx context.Context, id string, receiver, bot int64) (WidgetMessage, error) {
	var m WidgetMessage
	var err error
	err = s.db.QueryRow(ctx, `SELECT m.id,m.receiver_id,m.message_id,m.chat_data,w.id,w.bot_id,w.html,w.width,w.height,w.public_data,w.shared,w.allow_queries,w.cache_time FROM bot_widget_messages m JOIN bot_widgets w ON w.id=m.widget_id WHERE m.id=$1 AND (m.receiver_id=$2 OR w.bot_id=$3)`, id, receiver, bot).Scan(&m.ID, &m.Receiver, &m.MessageID, &m.ChatData, &m.Widget.ID, &m.Widget.Bot, &m.Widget.HTML, &m.Widget.Width, &m.Widget.Height, &m.Widget.PublicData, &m.Widget.Shared, &m.Widget.AllowQueries, &m.Widget.CacheTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrQueryInvalid
	}
	return m, err
}

func (s *Service) WidgetMessageByID(ctx context.Context, bot, receiver int64, message int) (WidgetMessage, error) {
	var id string
	var err error
	err = s.db.QueryRow(ctx, `SELECT m.id FROM bot_widget_messages m JOIN bot_widgets w ON w.id=m.widget_id WHERE w.bot_id=$1 AND m.receiver_id=$2 AND m.message_id=$3`, bot, receiver, message).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return WidgetMessage{}, ErrQueryInvalid
	}
	if err != nil {
		return WidgetMessage{}, err
	}
	return s.WidgetMessage(ctx, id, receiver, bot)
}

func (s *Service) EditWidget(ctx context.Context, bot int64, id string, publicData *string, cache int) error {
	var err error
	var tag pgconn.CommandTag
	tag, err = s.db.Exec(ctx, `UPDATE bot_widgets SET public_data=coalesce($3,public_data),cache_time=CASE WHEN $4=0 THEN cache_time ELSE $4 END,updated_at=now() WHERE id=$1 AND bot_id=$2 AND (NOT shared OR updated_at<=now()-interval '1 second')`, id, bot, publicData, cache)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrQueryInvalid
	}
	return err
}

func (s *Service) EditWidgetMessage(ctx context.Context, id string, chatData string) error {
	var err error
	_, err = s.db.Exec(ctx, "UPDATE bot_widget_messages SET chat_data=$2 WHERE id=$1", id, chatData)
	return err
}

func (s *Service) QueueWidgetQuery(ctx context.Context, message WidgetMessage, user int64, payload map[string]any) (int64, error) {
	var id int64
	var err error
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		id, err = s.QueueWidgetQueryTx(ctx, tx, message, user, payload)
		return err
	})
	return id, err
}

func (s *Service) QueueWidgetQueryTx(ctx context.Context, tx pgx.Tx, message WidgetMessage, user int64, payload map[string]any) (int64, error) {
	var id int64
	var count int
	var raw []byte
	var err error
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", strconv.FormatInt(message.Widget.Bot, 10)+":"+strconv.FormatInt(user, 10))
	if err != nil {
		return 0, err
	}
	err = tx.QueryRow(ctx, "SELECT count(*) FROM bot_widget_queries WHERE bot_id=$1 AND user_id=$2 AND created_at>now()-interval '5 seconds'", message.Widget.Bot, user).Scan(&count)
	if err != nil {
		return 0, err
	}
	if count >= 5 {
		return 0, ErrWidgetRateLimited
	}
	err = tx.QueryRow(ctx, `INSERT INTO bot_widget_queries(bot_id,user_id,message_key) VALUES($1,$2,$3) RETURNING id`, message.Widget.Bot, user, message.ID).Scan(&id)
	if err != nil {
		return 0, err
	}
	payload["id"] = strconv.FormatInt(id, 10)
	raw, err = json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bot_updates(bot_id,kind,chat_id,query_id,payload) VALUES($1,'widget_query',$2,$3,$4)", message.Widget.Bot, user, id, raw)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, "SELECT pg_notify($1,$2)", channel, strconv.FormatInt(message.Widget.Bot, 10))
	return id, err
}

func (s *Service) AnswerWidgetQuery(ctx context.Context, bot, id int64, data string) error {
	var tag pgconn.CommandTag
	var err error
	tag, err = s.db.Exec(ctx, `UPDATE bot_widget_queries SET answer=$3 WHERE id=$1 AND bot_id=$2 AND answer IS NULL AND created_at>now()-interval '15 seconds'`, id, bot, data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrQueryInvalid
	}
	_, err = s.db.Exec(ctx, "SELECT pg_notify($1,$2)", channel, "w"+strconv.FormatInt(id, 10))
	return err
}

func (s *Service) WaitWidgetAnswer(ctx context.Context, id int64) (string, error) {
	var answer *string
	var err error
	err = s.notify.poll(ctx, "w"+strconv.FormatInt(id, 10), time.Now().Add(15*time.Second), func() (bool, error) {
		var err error
		err = s.db.QueryRow(ctx, "SELECT answer FROM bot_widget_queries WHERE id=$1", id).Scan(&answer)
		return answer != nil, err
	})
	if err != nil {
		return "", err
	}
	if answer == nil {
		return "", ErrQueryInvalid
	}
	return *answer, nil
}
