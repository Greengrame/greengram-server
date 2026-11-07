package wallet

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// The topics the app's Notifications page has a row for. They start on, like
// any app's opt-outs.
var notificationTopics = []string{"product_enabled", "marketing_enabled", "educational_enabled", "feedback_enabled", "market_trends_enabled"}

func (s *Service) notificationSettings(ctx context.Context, u account.User) (map[string]any, error) {
	saved := map[string]bool{}
	var raw []byte
	err := s.db.QueryRow(ctx, "SELECT settings FROM wallet_notifications WHERE user_id=$1", u.ID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &saved); err != nil {
			return nil, err
		}
	}
	// the page greys its rows out without an id
	out := map[string]any{"telegram_id": u.ID}
	for _, topic := range notificationTopics {
		on, have := saved[topic]
		out[topic] = on || !have
	}
	return out, nil
}

func (s *Service) setNotificationSettings(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	change := map[string]bool{}
	for _, topic := range notificationTopics {
		if on, ok := data[topic].(bool); ok {
			change[topic] = on
		}
	}
	if len(change) == 0 {
		return nil, fail(400, "Choose a notification setting.")
	}
	raw, err := json.Marshal(change)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO wallet_notifications(user_id, settings) VALUES ($1, $2::jsonb)
		ON CONFLICT (user_id) DO UPDATE SET settings = wallet_notifications.settings || excluded.settings`, u.ID, string(raw)); err != nil {
		return nil, err
	}
	return s.notificationSettings(ctx, u)
}
