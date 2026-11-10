package bots

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type BusinessConnection struct {
	ID         string
	Bot        int64
	User       int64
	Rights     tg.BusinessBotRights
	Recipients tg.BusinessBotRecipients
	Enabled    bool
	Date       int
}

func scanBusiness(row pgx.Row) (BusinessConnection, error) {
	var connection BusinessConnection
	var rights, recipients []byte
	var err error
	err = row.Scan(&connection.ID, &connection.Bot, &connection.User, &rights, &recipients, &connection.Enabled, &connection.Date)
	if err != nil {
		return connection, err
	}
	err = connection.Rights.Decode(&bin.Buffer{Buf: rights})
	if err != nil {
		return connection, err
	}
	err = connection.Recipients.Decode(&bin.Buffer{Buf: recipients})
	return connection, err
}

func (s *Service) BusinessConnection(ctx context.Context, bot int64, id string) (BusinessConnection, error) {
	var connection BusinessConnection
	var err error
	connection, err = scanBusiness(s.db.QueryRow(ctx, "SELECT id,bot_id,user_id,rights,recipients,enabled,date FROM bot_business_connections WHERE id=$1 AND bot_id=$2", id, bot))
	if errors.Is(err, pgx.ErrNoRows) {
		return connection, ErrQueryInvalid
	}
	return connection, err
}

func (s *Service) ConnectedBusiness(ctx context.Context, user int64) (BusinessConnection, bool, error) {
	var connection BusinessConnection
	var err error
	connection, err = scanBusiness(s.db.QueryRow(ctx, "SELECT id,bot_id,user_id,rights,recipients,enabled,date FROM bot_business_connections WHERE user_id=$1 AND enabled", user))
	if errors.Is(err, pgx.ErrNoRows) {
		return connection, false, nil
	}
	return connection, err == nil, err
}

func (s *Service) ConnectBusiness(ctx context.Context, c BusinessConnection) (BusinessConnection, error) {
	var rights, recipients bin.Buffer
	var err error
	c.ID, err = widgetKey()
	if err != nil {
		return c, err
	}
	c.Date = int(time.Now().Unix())
	err = c.Rights.Encode(&rights)
	if err != nil {
		return c, err
	}
	err = c.Recipients.Encode(&recipients)
	if err != nil {
		return c, err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", c.User)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE bot_business_connections SET enabled=false WHERE user_id=$1", c.User)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO bot_business_connections(id,bot_id,user_id,rights,recipients,enabled,date) VALUES($1,$2,$3,$4,$5,$6,$7)`, c.ID, c.Bot, c.User, rights.Buf, recipients.Buf, c.Enabled, c.Date)
		return err
	})
	return c, err
}

func (s *Service) QueueBusinessConnection(ctx context.Context, c BusinessConnection, payload map[string]any) error {
	var raw []byte
	var err error
	raw, err = json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, "INSERT INTO bot_updates(bot_id,kind,chat_id,payload) VALUES($1,'business_connection',$2,$3)", c.Bot, c.User, raw)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, "SELECT pg_notify($1,$2)", channel, strconv.FormatInt(c.Bot, 10))
	return err
}

func (s *Service) DeleteBusinessGift(ctx context.Context, bot int64, key string, gift int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var connection BusinessConnection
		var tag pgconn.CommandTag
		var err error
		connection, err = scanBusiness(tx.QueryRow(ctx, "SELECT id,bot_id,user_id,rights,recipients,enabled,date FROM bot_business_connections WHERE id=$1 AND bot_id=$2 FOR UPDATE", key, bot))
		if err != nil || !connection.Enabled || !connection.Rights.SellGifts {
			return ErrQueryInvalid
		}
		tag, err = tx.Exec(ctx, `DELETE FROM gift_receipts r USING gift_catalog g WHERE r.id=$1 AND r.owner_id=$2 AND r.gift_id=g.id AND r.unique_id IS NULL AND g.stock_total IS NULL AND NOT r.converted`, gift, connection.User)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrQueryInvalid
		}
		return nil
	})
}

func (s *Service) DisableBusiness(ctx context.Context, user int64) error {
	var err error
	_, err = s.db.Exec(ctx, "UPDATE bot_business_connections SET enabled=false WHERE user_id=$1", user)
	return err
}
