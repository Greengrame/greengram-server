package bots

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

func (s *Service) ShareTheme(ctx context.Context, peer int64, channel bool) (tg.ShareThemeClass, error) {
	var raw []byte
	var err error
	err = s.db.QueryRow(ctx, "SELECT theme FROM peer_share_themes WHERE peer_id=$1 AND channel=$2", peer, channel).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return tg.DecodeShareTheme(&bin.Buffer{Buf: raw})
}

func (s *Service) SetShareTheme(ctx context.Context, peer int64, channel bool, theme tg.ShareThemeClass) error {
	var buffer bin.Buffer
	var err error
	err = theme.Encode(&buffer)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO peer_share_themes(peer_id,channel,user_id,channel_id,theme) VALUES($1::bigint,$2::boolean,CASE WHEN NOT $2::boolean THEN $1::bigint END,CASE WHEN $2::boolean THEN $1::bigint END,$3) ON CONFLICT(peer_id,channel) DO UPDATE SET theme=excluded.theme`, peer, channel, buffer.Buf)
	return err
}
