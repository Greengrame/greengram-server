package bots

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Ephemeral struct {
	Bot      int64
	Receiver int64
	Chat     int64
	Channel  bool
	Message  tg.EphemeralMessage
}

func (s *Service) CreateEphemeral(ctx context.Context, record Ephemeral) (Ephemeral, error) {
	var err error
	var buffer bin.Buffer
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		err = tx.QueryRow(ctx, `INSERT INTO bot_ephemeral_messages(bot_id,receiver_id,chat_id,channel,body) VALUES($1,$2,$3,$4,'') RETURNING id`, record.Bot, record.Receiver, record.Chat, record.Channel).Scan(&record.Message.ID)
		if err != nil {
			return err
		}
		err = record.Message.Encode(&buffer)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE bot_ephemeral_messages SET body=$2 WHERE id=$1", record.Message.ID, buffer.Buf)
		return err
	})
	return record, err
}

func (s *Service) Ephemeral(ctx context.Context, bot, receiver, chat int64, channel bool, id int) (Ephemeral, error) {
	var record = Ephemeral{Bot: bot, Receiver: receiver, Chat: chat, Channel: channel}
	var raw []byte
	var err error
	err = s.db.QueryRow(ctx, `SELECT body FROM bot_ephemeral_messages WHERE id=$1 AND bot_id=$2 AND receiver_id=$3 AND chat_id=$4 AND channel=$5 AND created_at > now()-interval '24 hours'`, id, bot, receiver, chat, channel).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, ErrQueryInvalid
	}
	if err != nil {
		return record, err
	}
	err = record.Message.Decode(&bin.Buffer{Buf: raw})
	return record, err
}

func (s *Service) UpdateEphemeral(ctx context.Context, record Ephemeral, remove bool) error {
	var buffer bin.Buffer
	var err error
	var tag pgconn.CommandTag
	if remove {
		tag, err = s.db.Exec(ctx, "DELETE FROM bot_ephemeral_messages WHERE id=$1 AND bot_id=$2 AND receiver_id=$3", record.Message.ID, record.Bot, record.Receiver)
	} else {
		err = record.Message.Encode(&buffer)
		if err != nil {
			return err
		}
		tag, err = s.db.Exec(ctx, "UPDATE bot_ephemeral_messages SET body=$4 WHERE id=$1 AND bot_id=$2 AND receiver_id=$3", record.Message.ID, record.Bot, record.Receiver, buffer.Buf)
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrQueryInvalid
	}
	return err
}
