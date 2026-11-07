package account

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// SetTopPeers is contacts.toggleTopPeers: off, contacts.getTopPeers answers
// topPeersDisabled.
func (s *Service) SetTopPeers(ctx context.Context, user int64, enabled bool) error {
	return s.setPref(ctx, user, "top_peers_disabled", !enabled)
}

func (s *Service) TopPeersDisabled(ctx context.Context, user int64) (bool, error) {
	var off bool
	err := s.db.QueryRow(ctx, "SELECT top_peers_disabled FROM account_prefs WHERE user_id=$1", user).Scan(&off)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return off, err
}

// SetPersonalChannel shows channel on the user's profile; 0 takes it off.
// The caller checks the user may show it.
func (s *Service) SetPersonalChannel(ctx context.Context, user, channel int64) error {
	var v *int64
	if channel != 0 {
		v = &channel
	}
	return s.setPref(ctx, user, "personal_channel_id", v)
}

// PersonalChannel is 0 when the user shows none.
func (s *Service) PersonalChannel(ctx context.Context, user int64) (int64, error) {
	var id *int64
	err := s.db.QueryRow(ctx, "SELECT personal_channel_id FROM account_prefs WHERE user_id=$1", user).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || id == nil {
		return 0, nil
	}
	return *id, err
}
