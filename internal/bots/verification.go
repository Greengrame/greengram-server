package bots

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Service) Verifier(ctx context.Context, bot int64) (tg.BotVerifierSettings, error) {
	var settings tg.BotVerifierSettings
	var err error
	var description string
	err = s.db.QueryRow(ctx, "SELECT icon,company,description,can_modify_description FROM bot_verifiers WHERE bot_id=$1", bot).Scan(&settings.Icon, &settings.Company, &description, &settings.CanModifyCustomDescription)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, ErrQueryInvalid
	}
	if err != nil {
		return settings, err
	}
	settings.SetCustomDescription(tg.TextWithEntities{Text: description})
	return settings, nil
}

func (s *Service) SetVerifier(ctx context.Context, bot int64, settings tg.BotVerifierSettings, enabled bool) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return SetVerifierTx(ctx, tx, bot, settings, enabled) })
}

func SetVerifierTx(ctx context.Context, tx pgx.Tx, bot int64, settings tg.BotVerifierSettings, enabled bool) error {
	var err error
	if !enabled {
		_, err = tx.Exec(ctx, "DELETE FROM bot_verifiers WHERE bot_id=$1", bot)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO bot_verifiers(bot_id,icon,company,description,can_modify_description) VALUES($1,$2,$3,$4,$5) ON CONFLICT(bot_id) DO UPDATE SET icon=excluded.icon,company=excluded.company,description=excluded.description,can_modify_description=excluded.can_modify_description`, bot, settings.Icon, settings.Company, settings.CustomDescription.Text, settings.CanModifyCustomDescription)
	}
	return err
}

func (s *Service) VerifyPeer(ctx context.Context, bot, peer int64, channel bool, description tg.TextWithEntities, remove bool) error {
	var buffer bin.Buffer
	var err error
	var column = "user_id"
	var settings tg.BotVerifierSettings
	var tag pgconn.CommandTag
	if channel {
		column = "channel_id"
	}
	settings, err = s.Verifier(ctx, bot)
	if err != nil {
		return err
	}
	if remove {
		_, err = s.db.Exec(ctx, "DELETE FROM bot_peer_verifications WHERE bot_id=$1 AND "+column+"=$2", bot, peer)
		return err
	}
	if description.Text != "" && !settings.CanModifyCustomDescription {
		return ErrQueryInvalid
	}
	if description.Text == "" {
		description = settings.CustomDescription
	}
	err = description.Encode(&buffer)
	if err != nil {
		return err
	}
	tag, err = s.db.Exec(ctx, "INSERT INTO bot_peer_verifications(bot_id,"+column+",description) VALUES($1,$2,$3) ON CONFLICT("+column+") DO UPDATE SET bot_id=excluded.bot_id,description=excluded.description WHERE bot_peer_verifications.bot_id=excluded.bot_id", bot, peer, buffer.Buf)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrQueryInvalid
	}
	return err
}

func (s *Service) PeerVerification(ctx context.Context, peer int64, channel bool) tg.BotVerification {
	var verification tg.BotVerification
	var raw []byte
	var err error
	var column = "user_id"
	if channel {
		column = "channel_id"
	}
	err = s.db.QueryRow(ctx, "SELECT v.bot_id,b.icon,v.description FROM bot_peer_verifications v JOIN bot_verifiers b ON b.bot_id=v.bot_id WHERE v."+column+"=$1", peer).Scan(&verification.BotID, &verification.Icon, &raw)
	if err != nil {
		return tg.BotVerification{}
	}
	err = verification.Description.Decode(&bin.Buffer{Buf: raw})
	if err != nil {
		return tg.BotVerification{}
	}
	return verification
}
