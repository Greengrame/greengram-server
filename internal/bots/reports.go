package bots

import "context"

// What @bugsbot takes.
const (
	ReportBug        = "bug"
	ReportSuggestion = "suggestion"
)

// reportsPerHour bounds one account: every report is a message to every admin.
const reportsPerHour = 5

// FileReport stores a report and returns its number, 0 if the account has
// sent too many lately. The check is in the insert.
func (s *Service) FileReport(ctx context.Context, user int64, kind, text string, msgID int) (int64, error) {
	rows, err := s.db.Query(ctx, `INSERT INTO bug_reports(user_id, kind, text, msg_ids)
		SELECT $1::bigint, $2::text, $3::text, ARRAY[$5::int]
		WHERE (SELECT count(*) FROM bug_reports WHERE user_id = $1::bigint AND created_at > now() - interval '1 hour') < $4
		RETURNING id`, user, kind, text, reportsPerHour, msgID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var id int64
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
	}
	return id, rows.Err()
}

// AttachToReport adds the rest of an album to the report its first photo started.
func (s *Service) AttachToReport(ctx context.Context, id int64, msgID int) error {
	_, err := s.db.Exec(ctx, "UPDATE bug_reports SET msg_ids = msg_ids || $2::int WHERE id = $1", id, msgID)
	return err
}
