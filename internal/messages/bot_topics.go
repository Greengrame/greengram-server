package messages

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

func (s *Service) prepareBotTopic(ctx context.Context, tx pgx.Tx, from, to int64, d *Draft) error {
	var bot, user int64
	var otherTopic int
	var closed bool
	var err error
	var create *tg.MessageActionTopicCreate
	var edit *tg.MessageActionTopicEdit
	var isCreate, isEdit bool
	var title *string
	var icon *int64
	var closing *bool
	var titleValue string
	var iconValue int64
	var closingValue bool
	create, isCreate = d.Action.(*tg.MessageActionTopicCreate)
	edit, isEdit = d.Action.(*tg.MessageActionTopicEdit)
	if d.Topic == 0 && !isCreate && d.ReplyTo == 0 {
		return nil
	}
	if !isCreate && d.ReplyTo != 0 {
		err = tx.QueryRow(ctx, "SELECT bot_topic_id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3", from, to, d.ReplyTo).Scan(&otherTopic)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if otherTopic != 0 {
			if d.Topic != 0 && d.Topic != otherTopic {
				return tgerr.New(400, "TOPIC_ID_INVALID")
			}
			d.Topic = otherTopic
		}
	}
	if d.Topic == 0 && !isCreate {
		return nil
	}
	err = tx.QueryRow(ctx, `SELECT b.user_id FROM bots b JOIN users u ON u.id=b.user_id WHERE b.user_id=ANY($1::bigint[]) AND b.kind='custom' AND NOT u.disabled AND coalesce(b.state->'settings'->>'topics','false') IN ('true','1')`, []int64{from, to}).Scan(&bot)
	if errors.Is(err, pgx.ErrNoRows) {
		return tgerr.New(400, "BOT_FORUM_MISSING")
	}
	if err != nil {
		return err
	}
	user = from
	if bot == from {
		user = to
	}
	if isCreate {
		if strings.TrimSpace(create.Title) == "" || utf8.RuneCountInString(create.Title) > 128 {
			return tgerr.New(400, "TOPIC_TITLE_INVALID")
		}
		_, err = tx.Exec(ctx, "UPDATE user_boxes SET next_id=(SELECT max(next_id) FROM user_boxes WHERE user_id=ANY($1::bigint[])) WHERE user_id=ANY($1::bigint[])", []int64{from, to})
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, "SELECT next_id FROM user_boxes WHERE user_id=$1", from).Scan(&d.Topic)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bot_private_topics(bot_id,user_id,id,title,icon_color,icon_emoji_id,creator_id,date) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", bot, user, d.Topic, create.Title, create.IconColor, create.IconEmojiID, from, s.unix())
		return err
	}
	err = tx.QueryRow(ctx, "SELECT closed FROM bot_private_topics WHERE bot_id=$1 AND user_id=$2 AND id=$3 AND NOT deleted FOR UPDATE", bot, user, d.Topic).Scan(&closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return tgerr.New(400, "TOPIC_ID_INVALID")
	}
	if err != nil {
		return err
	}
	if closed && !isEdit {
		return tgerr.New(400, "TOPIC_CLOSED")
	}
	if isEdit {
		if titleValue, closed = edit.GetTitle(); closed {
			if strings.TrimSpace(titleValue) == "" || utf8.RuneCountInString(titleValue) > 128 {
				return tgerr.New(400, "TOPIC_TITLE_INVALID")
			}
			title = &titleValue
		}
		if iconValue, closed = edit.GetIconEmojiID(); closed {
			icon = &iconValue
		}
		if closingValue, closed = edit.GetClosed(); closed {
			closing = &closingValue
		}
		_, err = tx.Exec(ctx, "UPDATE bot_private_topics SET title=coalesce($4,title),icon_emoji_id=coalesce($5,icon_emoji_id),closed=coalesce($6,closed) WHERE bot_id=$1 AND user_id=$2 AND id=$3", bot, user, d.Topic, title, icon, closing)
	}
	return err
}

func (s *Service) PrivateTopics(ctx context.Context, viewer, peer int64, idsFilter []int, query string, offset, limit int) ([]tg.ForumTopic, error) {
	var result = []tg.ForumTopic{}
	var topic tg.ForumTopic
	var creator, bot, user int64
	var rows pgx.Rows
	var err error
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err = s.db.Query(ctx, `SELECT t.id,t.title,t.icon_color,t.icon_emoji_id,t.closed,t.pinned,t.creator_id,t.date,t.bot_id,t.user_id,coalesce((SELECT max(m.id) FROM messages m WHERE m.owner_id=$1 AND m.peer_id=$2 AND m.bot_topic_id=t.id),t.id) FROM bot_private_topics t WHERE ((t.bot_id=$1 AND t.user_id=$2) OR (t.bot_id=$2 AND t.user_id=$1)) AND NOT t.deleted AND ($3::integer[] IS NULL OR t.id=ANY($3)) AND ($4='' OR t.title ILIKE '%'||$4||'%') AND ($5=0 OR t.id<$5) ORDER BY t.pinned DESC,t.id DESC LIMIT $6`, viewer, peer, idsFilter, query, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		topic = tg.ForumTopic{}
		err = rows.Scan(&topic.ID, &topic.Title, &topic.IconColor, &topic.IconEmojiID, &topic.Closed, &topic.Pinned, &creator, &topic.Date, &bot, &user, &topic.TopMessage)
		if err != nil {
			return nil, err
		}
		topic.Peer = &tg.PeerUser{UserID: ids.Client(peer)}
		topic.FromID = &tg.PeerUser{UserID: ids.Client(creator)}
		topic.My = creator == viewer
		result = append(result, topic)
	}
	return result, rows.Err()
}

func (s *Service) DeletePrivateTopic(ctx context.Context, viewer, peer int64, topic int) ([]Event, error) {
	var changed int64
	var list []int
	var rows pgx.Rows
	var id int
	var events []Event
	var err error
	err = s.db.QueryRow(ctx, "UPDATE bot_private_topics SET deleted=true WHERE id=$3 AND ((bot_id=$1 AND user_id=$2) OR (bot_id=$2 AND user_id=$1)) RETURNING bot_id", viewer, peer, topic).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tgerr.New(400, "TOPIC_ID_INVALID")
	}
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, "SELECT id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND bot_topic_id=$3", viewer, peer, topic)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		err = rows.Scan(&id)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	_, events, err = s.Delete(ctx, viewer, list, true)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, "DELETE FROM bot_private_topics WHERE id=$3 AND ((bot_id=$1 AND user_id=$2) OR (bot_id=$2 AND user_id=$1))", viewer, peer, topic)
	return events, err
}

func (m Msg) privateReplyHeader() *tg.MessageReplyHeader {
	var reply = m.ReplyTo
	var header *tg.MessageReplyHeader
	if reply == 0 {
		reply = m.Topic
	}
	header = ReplyHeader(reply, m.Quote)
	if m.Topic != 0 {
		header.ForumTopic = true
		if reply != m.Topic {
			header.SetReplyToTopID(m.Topic)
		}
	}
	return header
}
