package bots

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Greengrame/greengram-server/internal/account"
)

// BugStatuses is the status list of the real platform, in its filter order.
// Keep in sync with the bug_reports.status check and platform.js.
var BugStatuses = []string{"open", "closed", "fix coming", "fixed"}

type BugCard struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Author    string    `json:"author"`
	UserID    int64     `json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
	Likes     int       `json:"likes"`
	Dislikes  int       `json:"dislikes"`
	Vote      int       `json:"vote"`
	Comments  int       `json:"comments"`
}

type BugComment struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

type BugFilter struct {
	Query  string
	Kind   string
	Sort   string
	Status string
	From   string
	To     string
	Offset int
}

func (s *Service) BugCards(ctx context.Context, user int64, f BugFilter, id int64) ([]BugCard, error) {
	var rows pgx.Rows
	var err error
	var out = []BugCard{}
	var card BugCard
	rows, err = s.db.Query(ctx, `SELECT r.id, r.title, r.text, r.kind, r.status, u.name, r.user_id, r.created_at,
        (SELECT count(*) FROM bug_report_votes v WHERE v.report_id=r.id AND v.value=1),
        (SELECT count(*) FROM bug_report_votes v WHERE v.report_id=r.id AND v.value=-1),
        coalesce((SELECT value FROM bug_report_votes v WHERE v.report_id=r.id AND v.user_id=$1), 0),
        (SELECT count(*) FROM bug_report_comments c WHERE c.report_id=r.id)
        FROM bug_reports r JOIN users u ON u.id=r.user_id
        WHERE r.published AND ($2::bigint=0 OR r.id=$2) AND ($3::text='' OR r.kind=$3)
        AND ($4::text='' OR position(lower($4) in lower(r.title || ' ' || r.text)) > 0)
        AND ($5::text='' OR r.status=$5)
        AND ($6::text='' OR r.created_at >= NULLIF($6, '')::date)
        AND ($7::text='' OR r.created_at < NULLIF($7, '')::date + interval '1 day')
        ORDER BY CASE WHEN $8='rate' THEN (SELECT coalesce(sum(value), 0) FROM bug_report_votes v WHERE v.report_id=r.id) ELSE 0 END DESC,
        r.created_at DESC, r.id DESC LIMIT 50 OFFSET $9`, user, id, f.Kind, f.Query, f.Status, f.From, f.To, f.Sort, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = rows.Scan(&card.ID, &card.Title, &card.Text, &card.Kind, &card.Status, &card.Author, &card.UserID, &card.CreatedAt, &card.Likes, &card.Dislikes, &card.Vote, &card.Comments); err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, rows.Err()
}

func (s *Service) BugComments(ctx context.Context, id int64) ([]BugComment, error) {
	var rows pgx.Rows
	var err error
	var out = []BugComment{}
	var comment BugComment
	rows, err = s.db.Query(ctx, `SELECT c.id, u.name, c.text, c.created_at FROM bug_report_comments c
        JOIN users u ON u.id=c.user_id JOIN bug_reports r ON r.id=c.report_id WHERE r.id=$1 AND r.published ORDER BY c.id LIMIT 500`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = rows.Scan(&comment.ID, &comment.Author, &comment.Text, &comment.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, comment)
	}
	return out, rows.Err()
}

func ValidateBugCard(kind, title, text string) error {
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	if (kind != ReportBug && kind != ReportSuggestion) || utf8.RuneCountInString(title) < 3 || utf8.RuneCountInString(title) > 200 || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 12000 {
		return &account.Error{Status: 400, Message: "Use a title of 3–200 characters and a description of 1–12,000 characters."}
	}
	return nil
}

func (s *Service) CheckBugCardAllowance(ctx context.Context, user int64) error {
	var count int
	var err error
	err = s.db.QueryRow(ctx, "SELECT count(*) FROM bug_reports WHERE user_id=$1 AND created_at > now() - interval '1 hour'", user).Scan(&count)
	if err != nil {
		return err
	}
	if count >= reportsPerHour {
		return &account.Error{Status: 429, Message: "You can create up to five cards per hour. Try again later."}
	}
	return nil
}

func (s *Service) CreateBugCard(ctx context.Context, user int64, kind, title, text string) (int64, error) {
	return s.CreateBugCardWithFiles(ctx, user, kind, title, text, nil)
}

func (s *Service) CreateBugCardWithFiles(ctx context.Context, user int64, kind, title, text string, files []BugUpload) (int64, error) {
	var id int64
	var err error
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	if (kind != ReportBug && kind != ReportSuggestion) || utf8.RuneCountInString(title) < 3 || utf8.RuneCountInString(title) > 200 || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 12000 {
		return 0, &account.Error{Status: 400, Message: "Use a title of 3–200 characters and a description of 1–12,000 characters."}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var count int
		var lockID int64
		var txErr error
		var file BugUpload
		var owned bool
		txErr = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", user).Scan(&lockID)
		if txErr != nil {
			return txErr
		}
		txErr = tx.QueryRow(ctx, "SELECT count(*) FROM bug_reports WHERE user_id=$1 AND created_at > now() - interval '1 hour'", user).Scan(&count)
		if txErr != nil {
			return txErr
		}
		if count >= reportsPerHour {
			return &account.Error{Status: 429, Message: "You can create up to five cards per hour. Try again later."}
		}
		if len(files) > 5 {
			return &account.Error{Status: 400, Message: "Attach at most five files."}
		}
		for _, file = range files {
			txErr = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1 AND owner_id=$2 AND size <= 1048576)", file.ID, user).Scan(&owned)
			if txErr != nil {
				return txErr
			}
			if !owned {
				return &account.Error{Status: 400, Message: "Choose your attachment again."}
			}
		}
		txErr = tx.QueryRow(ctx, "INSERT INTO bug_reports(user_id, kind, title, text, published) VALUES($1,$2,$3,$4,true) RETURNING id", user, kind, title, text).Scan(&id)
		if txErr != nil {
			return txErr
		}
		for _, file = range files {
			_, txErr = tx.Exec(ctx, "INSERT INTO bug_report_files(report_id,document_id,name) VALUES($1,$2,$3)", id, file.ID, file.Name)
			if txErr != nil {
				return txErr
			}
		}
		return nil
	})
	return id, err
}

func (s *Service) VoteBugCard(ctx context.Context, user, id int64, value int) error {
	var err error
	var exists bool
	if value < -1 || value > 1 {
		return &account.Error{Status: 400, Message: "Choose an upvote, downvote or no vote."}
	}
	err = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM bug_reports WHERE id=$1 AND published)", id).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return &account.Error{Status: 404, Message: "Card not found."}
	}
	if value == 0 {
		_, err = s.db.Exec(ctx, "DELETE FROM bug_report_votes WHERE report_id=$1 AND user_id=$2", id, user)
	} else {
		_, err = s.db.Exec(ctx, "INSERT INTO bug_report_votes(report_id,user_id,value) VALUES($1,$2,$3) ON CONFLICT(report_id,user_id) DO UPDATE SET value=excluded.value", id, user, value)
	}
	return err
}

func (s *Service) CommentBugCard(ctx context.Context, user, id int64, text string) error {
	var err error
	var count int
	var lockID int64
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 4000 {
		return &account.Error{Status: 400, Message: "Use a comment of 1–4,000 characters."}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", user).Scan(&lockID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, "SELECT count(*) FROM bug_report_comments WHERE user_id=$1 AND created_at > now() - interval '1 hour'", user).Scan(&count)
		if err != nil {
			return err
		}
		if count >= 30 {
			return &account.Error{Status: 429, Message: "Too many comments. Try again later."}
		}
		tag, err = tx.Exec(ctx, "INSERT INTO bug_report_comments(report_id,user_id,text) SELECT id,$2,$3 FROM bug_reports WHERE id=$1 AND published", id, user, text)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &account.Error{Status: 404, Message: "Card not found."}
		}
		return nil
	})
}

func (s *Service) SetBugCardStatus(ctx context.Context, u account.User, id int64, status string) error {
	var err error
	var tag pgconn.CommandTag
	if u.Role != "admin" {
		return &account.Error{Status: 403, Message: "Only admins can change a card's status."}
	}
	if !slices.Contains(BugStatuses, status) {
		return &account.Error{Status: 400, Message: "Choose a valid status."}
	}
	tag, err = s.db.Exec(ctx, "UPDATE bug_reports SET status=$2 WHERE id=$1 AND published", id, status)
	if err == nil && tag.RowsAffected() == 0 {
		return &account.Error{Status: 404, Message: "Card not found."}
	}
	return err
}

type BugFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

type BugUpload struct {
	ID   int64
	Name string
}

func (s *Service) BugFiles(ctx context.Context, report, document int64) ([]BugFile, error) {
	var rows pgx.Rows
	var err error
	var files = []BugFile{}
	var file BugFile
	rows, err = s.db.Query(ctx, `SELECT d.id::text, f.name, d.mime, d.size FROM bug_report_files f JOIN documents d ON d.id=f.document_id
        JOIN bug_reports r ON r.id=f.report_id WHERE r.published AND ($1::bigint=0 OR r.id=$1) AND ($2::bigint=0 OR d.id=$2) ORDER BY d.id`, report, document)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = rows.Scan(&file.ID, &file.Name, &file.Mime, &file.Size); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Service) EditBugCard(ctx context.Context, user account.User, id int64, title, text string) error {
	var tag pgconn.CommandTag
	var err error
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	if utf8.RuneCountInString(title) < 3 || utf8.RuneCountInString(title) > 200 || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 12000 {
		return &account.Error{Status: 400, Message: "Use a title of 3–200 characters and a description of 1–12,000 characters."}
	}
	tag, err = s.db.Exec(ctx, "UPDATE bug_reports SET title=$3, text=$4 WHERE id=$1 AND published AND (user_id=$2 OR $5)", id, user.ID, title, text, user.Role == "admin")
	if err == nil && tag.RowsAffected() == 0 {
		return &account.Error{Status: 404, Message: "Card not found, or you cannot edit it."}
	}
	return err
}

func (s *Service) DeleteBugCard(ctx context.Context, user account.User, id int64) error {
	var tag pgconn.CommandTag
	var err error
	tag, err = s.db.Exec(ctx, "UPDATE bug_reports SET published=false WHERE id=$1 AND published AND (user_id=$2 OR $3)", id, user.ID, user.Role == "admin")
	if err == nil && tag.RowsAffected() == 0 {
		return &account.Error{Status: 404, Message: "Card not found, or you cannot delete it."}
	}
	return err
}
