package admin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The queue of appeals frozen accounts file through @SpamBot.

type appeal struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"userId"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Frozen    bool   `json:"frozen"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"createdAt"`
	DecidedAt *int64 `json:"decidedAt"`
	DecidedBy string `json:"decidedBy"`
}

// appeals lists the open ones first, then the last decided.
func (a *API) appeals(q *req) (any, error) {
	rows, err := a.DB.Query(q.ctx, `SELECT f.id, f.user_id, u.name, u.username, u.account_state = 'frozen', f.text, f.status, f.reason,
		extract(epoch FROM f.created_at)::bigint, extract(epoch FROM f.decided_at)::bigint, coalesce(d.name, '')
		FROM freeze_appeals f JOIN users u ON u.id = f.user_id LEFT JOIN users d ON d.id = f.decided_by
		ORDER BY f.status = 'open' DESC, coalesce(f.decided_at, f.created_at) DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (appeal, error) {
		var v appeal
		err := r.Scan(&v.ID, &v.UserID, &v.Name, &v.Username, &v.Frozen, &v.Text, &v.Status, &v.Reason, &v.CreatedAt, &v.DecidedAt, &v.DecidedBy)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"appeals": list}, nil
}

// closeAppeal marks someone's open appeal decided, if there is one. The
// account's state changed some other way, so nobody gets told.
func closeAppeal(ctx context.Context, tx pgx.Tx, user, actor int64, approved bool) error {
	status := "rejected"
	if approved {
		status = "approved"
	}
	_, err := tx.Exec(ctx, "UPDATE freeze_appeals SET status=$3, decided_at=now(), decided_by=$2 WHERE user_id=$1 AND status='open'", user, actor, status)
	return err
}

// openAppeal locks an undecided appeal; tx held.
func openAppeal(ctx context.Context, tx pgx.Tx, id int64) (int64, error) {
	var user int64
	var status string
	err := tx.QueryRow(ctx, "SELECT user_id, status FROM freeze_appeals WHERE id=$1 FOR UPDATE", id).Scan(&user, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, fail(404, "Appeal not found.")
	case err != nil:
		return 0, err
	case status != "open":
		return 0, fail(409, "This appeal was already decided.")
	}
	return user, nil
}

// approveAppeal unfreezes through the same state change the Members view
// makes, and leaves the same audit entry.
func (a *API) approveAppeal(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	var user int64
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if user, err = openAppeal(q.ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(q.ctx, "UPDATE users SET account_state='active', disabled=false, is_suspended=false WHERE id=$1 AND account_state='frozen'", user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail(409, "This account is no longer frozen.")
		}
		if _, err := tx.Exec(q.ctx, "UPDATE freeze_appeals SET status='approved', decided_at=now(), decided_by=$2 WHERE id=$1", id, q.me.ID); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "account_active", user, map[string]any{
			"previousState": "frozen", "state": "active", "reason": "appeal approved", "erased": false, "appeal": id})
	})
	if err != nil {
		return nil, err
	}
	a.tellAccessChanged(user)
	a.answerAppeal(q.ctx, user, true, "")
	return ok(), nil
}

func (a *API) rejectAppeal(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	reason := ""
	if q.has("reason") && q.body["reason"] != nil {
		var valid bool
		if reason, valid = q.str("reason"); !valid || utf8.RuneCountInString(reason) > 500 {
			return nil, fail(400, "Give a reason of up to 500 characters.")
		}
		reason = strings.TrimSpace(reason)
	}
	var user int64
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if user, err = openAppeal(q.ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(q.ctx, "UPDATE freeze_appeals SET status='rejected', reason=$2, decided_at=now(), decided_by=$3 WHERE id=$1", id, reason, q.me.ID); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "appeal_rejected", user, map[string]any{"appeal": id, "reason": reason})
	})
	if err != nil {
		return nil, err
	}
	a.answerAppeal(q.ctx, user, false, reason)
	return ok(), nil
}

// The decision stands either way; the person can also ask the bot again.
func (a *API) answerAppeal(ctx context.Context, user int64, approved bool, reason string) {
	if a.AnswerAppeal == nil {
		return
	}
	if err := a.AnswerAppeal(ctx, user, approved, reason); err != nil {
		slog.Warn("admin: answer appeal", "user", user, "err", err)
	}
}
