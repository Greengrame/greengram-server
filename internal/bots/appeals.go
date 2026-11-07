package bots

import (
	"context"
	"time"
)

// AppealCooldown is how long after a rejection the next appeal has to wait.
const AppealCooldown = 24 * time.Hour

// AppealState is what @SpamBot needs to answer someone.
type AppealState struct {
	Frozen     bool
	Open       bool
	RejectedAt time.Time // the last rejection, zero if none
	Reason     string    // what the admin wrote with it
}

// Cooling is whether the last rejection is too recent for a new appeal.
func (st AppealState) Cooling(now time.Time) bool {
	return !st.RejectedAt.IsZero() && now.Before(st.RejectedAt.Add(AppealCooldown))
}

func (s *Service) AppealState(ctx context.Context, user int64) (AppealState, error) {
	var st AppealState
	var rejected *time.Time
	err := s.db.QueryRow(ctx, `SELECT u.account_state = 'frozen',
		EXISTS(SELECT 1 FROM freeze_appeals o WHERE o.user_id = u.id AND o.status = 'open'),
		r.decided_at, coalesce(r.reason, '')
		FROM users u LEFT JOIN LATERAL (SELECT decided_at, reason FROM freeze_appeals
			WHERE user_id = u.id AND status = 'rejected' ORDER BY decided_at DESC LIMIT 1) r ON true
		WHERE u.id = $1`, user).Scan(&st.Frozen, &st.Open, &rejected, &st.Reason)
	if err != nil {
		return st, err
	}
	if rejected != nil {
		st.RejectedAt = *rejected
	}
	return st, nil
}

// FileAppeal stores an appeal; false if the account may not file one now:
// not frozen, one already open, or a rejection too recent. The checks are in
// the insert so a double tap can't get past them.
func (s *Service) FileAppeal(ctx context.Context, user int64, text string) (bool, error) {
	tag, err := s.db.Exec(ctx, `INSERT INTO freeze_appeals(user_id, text)
		SELECT $1::bigint, $2::text
		WHERE EXISTS(SELECT 1 FROM users WHERE id = $1::bigint AND account_state = 'frozen')
		AND NOT EXISTS(SELECT 1 FROM freeze_appeals WHERE user_id = $1::bigint AND status = 'rejected'
			AND decided_at > now() - make_interval(secs => $3::double precision))
		ON CONFLICT (user_id) WHERE status = 'open' DO NOTHING`, user, text, AppealCooldown.Seconds())
	return tag.RowsAffected() == 1, err
}

// Appealer names someone for the admins' notice.
func (s *Service) Appealer(ctx context.Context, user int64) (name, username string, err error) {
	err = s.db.QueryRow(ctx, "SELECT name, username FROM users WHERE id = $1", user).Scan(&name, &username)
	return
}
