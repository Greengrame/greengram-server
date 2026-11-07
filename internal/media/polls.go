package media

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

const KindPoll = "poll"

// Telegram's limits (appConfig poll_answers_max and friends).
const (
	maxPollAnswers  = 12
	maxQuestion     = 255
	maxAnswerText   = 100
	maxSolution     = 200
	maxOptionBytes  = 100
	pollRecentShown = 3
)

var (
	errPollInvalid   = tgerr.New(400, "POLL_OPTION_INVALID")
	errOptionInvalid = tgerr.New(400, "OPTION_INVALID")
	errPollClosed    = tgerr.New(400, "MESSAGE_POLL_CLOSED")
	errRevote        = tgerr.New(400, "REVOTE_NOT_ALLOWED")
	errPollNotPublic = tgerr.New(403, "POLL_VOTE_REQUIRED")
)

// Poll is a poll as stored; Results and PollTL render it for one viewer.
type Poll struct {
	ID       int64
	Creator  int64
	Date     int
	TL       tg.Poll // as created, without the closed and creator flags
	Closed   bool
	CloseAt  int
	Correct  []int
	Solution string
	SolEnt   []tg.MessageEntityClass
	SolMedia tg.MessageMediaClass
	Attached tg.MessageMediaClass

	// the stored TL, decoded again for every render: td's Encode writes
	// flags as it goes, so two pushes can't share one object
	body, solEnt, solMedia, attached []byte
}

// Tally is the votes of one poll as one viewer sees them.
type Tally struct {
	Total    int
	ByOption map[string]int
	Mine     map[string]bool
	Recent   []int64            // newest voters, public polls only
	ByRecent map[string][]int64 // the same per answer
}

func (t Tally) Voted() bool { return len(t.Mine) > 0 }

func encodeTL(o bin.Encoder) ([]byte, error) {
	if o == nil {
		return nil, nil
	}
	var b bin.Buffer
	if err := o.Encode(&b); err != nil {
		return nil, err
	}
	return b.Buf, nil
}

func decodeMedia(raw []byte) (tg.MessageMediaClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return tg.DecodeMessageMedia(&bin.Buffer{Buf: raw})
}

func textLen(t tg.TextWithEntities) int { return utf8.RuneCountInString(strings.TrimSpace(t.Text)) }

// CreatePoll stores a new poll from what a client sent. Attached and
// solution media are already turned into message media by the caller. The
// client's poll id is a random id; the poll gets one of ours.
func (s *Service) CreatePoll(ctx context.Context, creator int64, in *tg.InputMediaPoll, attached, solMedia tg.MessageMediaClass) (Poll, error) {
	src := in.Poll
	if n := textLen(src.Question); n == 0 || n > maxQuestion {
		return Poll{}, tgerr.New(400, "POLL_QUESTION_INVALID")
	}
	if len(src.Answers) < 2 || len(src.Answers) > maxPollAnswers {
		return Poll{}, tgerr.New(400, "POLL_ANSWERS_INVALID")
	}
	p := Poll{ID: newID(), Creator: creator, Date: s.unix(), Attached: attached, SolMedia: solMedia, Correct: []int{}}
	p.TL = tg.Poll{
		ID:                    p.ID,
		PublicVoters:          src.PublicVoters,
		MultipleChoice:        src.MultipleChoice,
		Quiz:                  src.Quiz,
		OpenAnswers:           src.OpenAnswers && !src.Quiz,
		RevotingDisabled:      src.RevotingDisabled,
		ShuffleAnswers:        src.ShuffleAnswers,
		HideResultsUntilClose: src.HideResultsUntilClose,
		Question:              src.Question,
	}
	seen := map[string]bool{}
	for i, a := range src.Answers {
		var text tg.TextWithEntities
		var opt []byte
		switch v := a.(type) {
		case *tg.PollAnswer:
			text, opt = v.Text, v.Option
		case *tg.InputPollAnswer:
			text = v.Text
		default:
			return Poll{}, errPollInvalid
		}
		// clients pick the option bytes; any unique short ones do
		if len(opt) == 0 {
			opt = []byte(strconv.Itoa(i))
		}
		if n := textLen(text); n == 0 || n > maxAnswerText || len(opt) > maxOptionBytes || seen[string(opt)] {
			return Poll{}, errPollInvalid
		}
		seen[string(opt)] = true
		p.TL.Answers = append(p.TL.Answers, &tg.PollAnswer{Text: text, Option: opt})
	}
	if src.ClosePeriod > 0 {
		if src.ClosePeriod < 5 || src.ClosePeriod > 600 {
			return Poll{}, tgerr.New(400, "POLL_CLOSE_PERIOD_INVALID")
		}
		p.TL.ClosePeriod = src.ClosePeriod
		p.CloseAt = p.Date + src.ClosePeriod
	} else if src.CloseDate > 0 {
		p.CloseAt = src.CloseDate
	}
	p.TL.CloseDate = p.CloseAt
	if p.TL.Quiz {
		correct, _ := in.GetCorrectAnswers()
		if len(correct) == 0 {
			return Poll{}, tgerr.New(400, "QUIZ_CORRECT_ANSWERS_EMPTY")
		}
		if len(correct) > 1 && !p.TL.MultipleChoice {
			return Poll{}, tgerr.New(400, "QUIZ_CORRECT_ANSWERS_TOO_MUCH")
		}
		for _, c := range correct {
			if c < 0 || c >= len(p.TL.Answers) || slices.Contains(p.Correct, c) {
				return Poll{}, tgerr.New(400, "QUIZ_CORRECT_ANSWER_INVALID")
			}
			p.Correct = append(p.Correct, c)
		}
		p.Solution, _ = in.GetSolution()
		if utf8.RuneCountInString(p.Solution) > maxSolution {
			return Poll{}, tgerr.New(400, "POLL_SOLUTION_TOO_LONG")
		}
		p.SolEnt, _ = in.GetSolutionEntities()
	} else {
		p.SolMedia = nil
	}
	p.TL.Hash = pollHash(p.TL)
	if err := s.savePoll(ctx, s.db, p, true); err != nil {
		return Poll{}, err
	}
	saved, _, err := s.Poll(ctx, p.ID)
	return saved, err
}

// pollHash changes whenever the answers do (messages.getPollResults
// poll_hash).
func pollHash(p tg.Poll) int64 {
	var opts []int64
	for _, a := range p.Answers {
		if v, ok := a.(*tg.PollAnswer); ok {
			var h int64
			for _, c := range v.Option {
				h = h*31 + int64(c)
			}
			opts = append(opts, h, int64(len(v.Text.Text)))
		}
	}
	return ListHash(opts)
}

func (s *Service) savePoll(ctx context.Context, q execer, p Poll, insert bool) error {
	body, err := encodeTL(&p.TL)
	if err != nil {
		return err
	}
	ent, err := encodeEntities(p.SolEnt)
	if err != nil {
		return err
	}
	sol, err := encodeTL(p.SolMedia)
	if err != nil {
		return err
	}
	att, err := encodeTL(p.Attached)
	if err != nil {
		return err
	}
	if !insert {
		_, err = q.Exec(ctx, "UPDATE polls SET body=$2 WHERE id=$1", p.ID, body)
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO polls(id, creator_id, date, body, close_date, correct, solution, solution_entities, solution_media, attached_media)
		VALUES ($1, $2, $3, $4, nullif($5, 0), $6, $7, $8, $9, $10)`,
		p.ID, p.Creator, p.Date, body, p.CloseAt, p.Correct, p.Solution, ent, sol, att)
	return err
}

func encodeEntities(list []tg.MessageEntityClass) ([]byte, error) {
	if len(list) == 0 {
		return nil, nil
	}
	var b bin.Buffer
	b.PutVectorHeader(len(list))
	for _, e := range list {
		if err := e.Encode(&b); err != nil {
			return nil, err
		}
	}
	return b.Buf, nil
}

func decodeEntities(raw []byte) ([]tg.MessageEntityClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: raw}
	n, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	out := make([]tg.MessageEntityClass, 0, n)
	for range n {
		e, err := tg.DecodeMessageEntity(b)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

const pollCols = "id, creator_id, date, body, closed, coalesce(close_date, 0), correct, solution, solution_entities, solution_media, attached_media"

func scanPoll(row pgx.Row) (Poll, error) {
	var p Poll
	var body, ent, sol, att []byte
	if err := row.Scan(&p.ID, &p.Creator, &p.Date, &body, &p.Closed, &p.CloseAt, &p.Correct, &p.Solution, &ent, &sol, &att); err != nil {
		return p, err
	}
	p.body, p.solEnt, p.solMedia, p.attached = body, ent, sol, att
	if err := p.TL.Decode(&bin.Buffer{Buf: body}); err != nil {
		return p, err
	}
	var err error
	if p.SolEnt, err = decodeEntities(ent); err != nil {
		return p, err
	}
	if p.SolMedia, err = decodeMedia(sol); err != nil {
		return p, err
	}
	p.Attached, err = decodeMedia(att)
	return p, err
}

// Polls loads polls by id.
func (s *Service) Polls(ctx context.Context, list []int64) (map[int64]Poll, error) {
	out := map[int64]Poll{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT "+pollCols+" FROM polls WHERE id = ANY($1)", list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPoll(rows)
		if err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

func (s *Service) Poll(ctx context.Context, id int64) (Poll, bool, error) {
	m, err := s.Polls(ctx, []int64{id})
	p, ok := m[id]
	return p, ok, err
}

// Tallies counts the votes of polls for viewer.
func (s *Service) Tallies(ctx context.Context, viewer int64, list []int64) (map[int64]Tally, error) {
	out := map[int64]Tally{}
	if len(list) == 0 {
		return out, nil
	}
	get := func(id int64) Tally {
		t, ok := out[id]
		if !ok {
			t = Tally{ByOption: map[string]int{}, Mine: map[string]bool{}, ByRecent: map[string][]int64{}}
		}
		return t
	}
	rows, err := s.db.Query(ctx, `SELECT poll_id, option, count(*), bool_or(user_id = $2) FROM poll_votes WHERE poll_id = ANY($1) GROUP BY poll_id, option`, list, viewer)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var opt []byte
		var n int
		var mine bool
		if err := rows.Scan(&id, &opt, &n, &mine); err != nil {
			rows.Close()
			return nil, err
		}
		t := get(id)
		t.ByOption[string(opt)] = n
		if mine {
			t.Mine[string(opt)] = true
		}
		out[id] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT poll_id, count(DISTINCT user_id) FROM poll_votes WHERE poll_id = ANY($1) GROUP BY poll_id`, list)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		t := get(id)
		t.Total = n
		out[id] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// the faces on a public poll: newest few voters, overall and per answer
	rows, err = s.db.Query(ctx, `SELECT poll_id, option, user_id FROM poll_votes WHERE poll_id = ANY($1) ORDER BY date DESC, user_id DESC`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, user int64
		var opt []byte
		if err := rows.Scan(&id, &opt, &user); err != nil {
			return nil, err
		}
		t := get(id)
		if len(t.Recent) < pollRecentShown && !slices.Contains(t.Recent, user) {
			t.Recent = append(t.Recent, user)
		}
		if r := t.ByRecent[string(opt)]; len(r) < pollRecentShown {
			t.ByRecent[string(opt)] = append(r, user)
		}
		out[id] = t
	}
	return out, rows.Err()
}

// ForViewer is t (counted for anyone) with viewer's own votes; for
// pushing one count to many people.
func (s *Service) ForViewer(ctx context.Context, t Tally, poll, viewer int64) (Tally, error) {
	rows, err := s.db.Query(ctx, "SELECT option FROM poll_votes WHERE poll_id=$1 AND user_id=$2", poll, viewer)
	if err != nil {
		return t, err
	}
	opts, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return t, err
	}
	t.Mine = map[string]bool{}
	for _, o := range opts {
		t.Mine[string(o)] = true
	}
	return t, nil
}

func (p Poll) options() [][]byte {
	var out [][]byte
	for _, a := range p.TL.Answers {
		if v, ok := a.(*tg.PollAnswer); ok {
			out = append(out, v.Option)
		}
	}
	return out
}

// PollTL is the poll as viewer gets it, a copy of its own.
func (p Poll) PollTL(viewer int64) tg.Poll {
	var out tg.Poll
	if err := out.Decode(&bin.Buffer{Buf: p.body}); err != nil {
		out = p.TL // we stored those bytes; can't really fail
	}
	out.Closed = p.Closed
	out.Creator = viewer == p.Creator
	return out
}

func fresh(raw []byte) tg.MessageMediaClass {
	m, _ := decodeMedia(raw)
	return m
}

// countsHidden: the creator can keep the numbers back until the end.
func (p Poll) countsHidden(viewer int64) bool {
	return p.TL.HideResultsUntilClose && !p.Closed && viewer != p.Creator
}

// Results is what viewer sees of the votes. A quiz keeps the right answer
// and the explanation back until the viewer answered or it closed.
func (p Poll) Results(t Tally, viewer int64, public bool) tg.PollResults {
	var out tg.PollResults
	reveal := p.TL.Quiz && (t.Voted() || p.Closed)
	hidden := p.countsHidden(viewer)
	list := []tg.PollAnswerVoters{}
	for i, opt := range p.options() {
		v := tg.PollAnswerVoters{Option: opt}
		if t.Mine[string(opt)] {
			v.SetChosen(true)
		}
		if reveal && slices.Contains(p.Correct, i) {
			v.SetCorrect(true)
		}
		if !hidden {
			v.SetVoters(t.ByOption[string(opt)])
			if public && p.TL.PublicVoters && len(t.ByRecent[string(opt)]) > 0 {
				v.SetRecentVoters(peers(t.ByRecent[string(opt)]))
			}
		}
		list = append(list, v)
	}
	out.SetResults(list)
	if !hidden {
		out.SetTotalVoters(t.Total)
		if public && p.TL.PublicVoters && len(t.Recent) > 0 {
			out.SetRecentVoters(peers(t.Recent))
		}
	}
	if reveal && p.Solution != "" {
		out.SetSolution(p.Solution)
		// one flag covers both: the entities go along, empty or not
		ent, _ := decodeEntities(p.solEnt)
		out.SetSolutionEntities(append([]tg.MessageEntityClass{}, ent...))
		if m := fresh(p.solMedia); m != nil {
			out.SetSolutionMedia(m)
		}
	}
	return out
}

func peers(users []int64) []tg.PeerClass {
	out := make([]tg.PeerClass, 0, len(users))
	for _, u := range users {
		out = append(out, &tg.PeerUser{UserID: ids.Client(u)})
	}
	return out
}

// Voters are the users Results names, for the users list next to it.
func (p Poll) Voters(t Tally) []int64 {
	if !p.TL.PublicVoters {
		return nil
	}
	out := slices.Clone(t.Recent)
	for _, r := range t.ByRecent {
		out = append(out, r...)
	}
	return out
}

// Media is the message media for viewer.
func (p Poll) Media(t Tally, viewer int64) *tg.MessageMediaPoll {
	m := &tg.MessageMediaPoll{Poll: p.PollTL(viewer), Results: p.Results(t, viewer, true)}
	if a := fresh(p.attached); a != nil {
		m.SetAttachedMedia(a)
	}
	return m
}

// PollRef is a new poll ready for a message, as stored: nobody's view,
// renders put the viewer in.
func (s *Service) PollRef(p Poll) Ref {
	return Ref{Kind: KindPoll, ID: p.ID, TL: p.Media(Tally{}, 0)}
}

// Vote replaces user's votes in a poll; no options takes them back.
func (s *Service) Vote(ctx context.Context, user, poll int64, options [][]byte) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		p, err := scanPoll(tx.QueryRow(ctx, "SELECT "+pollCols+" FROM polls WHERE id=$1 FOR UPDATE", poll))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		if p.Closed || p.CloseAt != 0 && p.CloseAt <= s.unix() {
			return errPollClosed
		}
		have := p.options()
		var want [][]byte
		for _, o := range options {
			if !slices.ContainsFunc(have, func(h []byte) bool { return bytes.Equal(h, o) }) {
				return errOptionInvalid
			}
			if !slices.ContainsFunc(want, func(h []byte) bool { return bytes.Equal(h, o) }) {
				want = append(want, o)
			}
		}
		if len(want) > 1 && !p.TL.MultipleChoice {
			return tgerr.New(400, "OPTIONS_TOO_MUCH")
		}
		var voted bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM poll_votes WHERE poll_id=$1 AND user_id=$2)", poll, user).Scan(&voted); err != nil {
			return err
		}
		// a quiz answer is final, and so is any vote where the creator said so
		if voted && (p.TL.Quiz || p.TL.RevotingDisabled) {
			return errRevote
		}
		if _, err := tx.Exec(ctx, "DELETE FROM poll_votes WHERE poll_id=$1 AND user_id=$2", poll, user); err != nil {
			return err
		}
		for _, o := range want {
			if _, err := tx.Exec(ctx, "INSERT INTO poll_votes(poll_id, user_id, option, date) VALUES ($1, $2, $3, $4)", poll, user, o, s.unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClosePoll stops a poll; false if it already was.
func (s *Service) ClosePoll(ctx context.Context, poll int64) (bool, error) {
	tag, err := s.db.Exec(ctx, "UPDATE polls SET closed = true WHERE id=$1 AND NOT closed", poll)
	return tag.RowsAffected() == 1, err
}

// CloseDuePolls closes polls whose time ran out and returns them.
func (s *Service) CloseDuePolls(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, "UPDATE polls SET closed = true WHERE NOT closed AND close_date IS NOT NULL AND close_date <= $1 RETURNING id", s.unix())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// AddPollAnswer adds an answer to an open poll.
func (s *Service) AddPollAnswer(ctx context.Context, user, poll int64, text tg.TextWithEntities) error {
	if n := textLen(text); n == 0 || n > maxAnswerText {
		return tgerr.New(400, "POLL_ANSWER_INVALID")
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		p, err := scanPoll(tx.QueryRow(ctx, "SELECT "+pollCols+" FROM polls WHERE id=$1 FOR UPDATE", poll))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		if !p.TL.OpenAnswers || p.Closed {
			return tgerr.New(400, "POLL_ANSWERS_CLOSED")
		}
		if len(p.TL.Answers) >= maxPollAnswers {
			return tgerr.New(400, "POLL_ANSWERS_TOO_MUCH")
		}
		// a new byte string nobody used
		var opt []byte
		for i := len(p.TL.Answers); ; i++ {
			opt = []byte(strconv.Itoa(i))
			if !slices.ContainsFunc(p.options(), func(h []byte) bool { return bytes.Equal(h, opt) }) {
				break
			}
		}
		a := &tg.PollAnswer{Text: text, Option: opt}
		a.SetAddedBy(&tg.PeerUser{UserID: ids.Client(user)})
		a.SetDate(s.unix())
		p.TL.Answers = append(p.TL.Answers, a)
		p.TL.Hash = pollHash(p.TL)
		return s.savePoll(ctx, tx, p, false)
	})
}

// DeletePollAnswer takes an added answer out again: the creator may drop
// any, others only their own.
func (s *Service) DeletePollAnswer(ctx context.Context, user, poll int64, option []byte) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		p, err := scanPoll(tx.QueryRow(ctx, "SELECT "+pollCols+" FROM polls WHERE id=$1 FOR UPDATE", poll))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		i := slices.IndexFunc(p.TL.Answers, func(a tg.PollAnswerClass) bool {
			v, ok := a.(*tg.PollAnswer)
			return ok && bytes.Equal(v.Option, option)
		})
		if i < 0 {
			return errOptionInvalid
		}
		v := p.TL.Answers[i].(*tg.PollAnswer)
		by, _ := v.AddedBy.(*tg.PeerUser)
		if user != p.Creator && (by == nil || ids.Server(by.UserID) != user) {
			return tgerr.New(403, "POLL_ANSWER_DELETE_FORBIDDEN")
		}
		if len(p.TL.Answers) <= 2 {
			return tgerr.New(400, "POLL_ANSWERS_INVALID")
		}
		p.TL.Answers = slices.Delete(p.TL.Answers, i, i+1)
		p.TL.Hash = pollHash(p.TL)
		if _, err := tx.Exec(ctx, "DELETE FROM poll_votes WHERE poll_id=$1 AND option=$2", poll, option); err != nil {
			return err
		}
		return s.savePoll(ctx, tx, p, false)
	})
}

// Vote is one voter of a public poll.
type PollVote struct {
	User    int64
	Options [][]byte
	Date    int
}

// PollVotes pages through a public poll's voters, newest first. The
// offset is "date:user" of the last row handed out.
func (s *Service) PollVotes(ctx context.Context, poll int64, option []byte, offset string, limit int) ([]PollVote, int, string, error) {
	p, ok, err := s.Poll(ctx, poll)
	if err != nil {
		return nil, 0, "", err
	}
	if !ok {
		return nil, 0, "", tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	if !p.TL.PublicVoters {
		return nil, 0, "", errPollNotPublic
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	where := "poll_id=$1"
	args := []any{poll}
	if len(option) > 0 {
		args = append(args, option)
		where += " AND option=$2"
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(DISTINCT user_id) FROM poll_votes WHERE "+where, args...).Scan(&count); err != nil {
		return nil, 0, "", err
	}
	if d, u, ok := strings.Cut(offset, ":"); ok {
		date, _ := strconv.Atoi(d)
		user, _ := strconv.ParseInt(u, 10, 64)
		args = append(args, date, user)
		where += " AND (date, user_id) < ($" + strconv.Itoa(len(args)-1) + ", $" + strconv.Itoa(len(args)) + ")"
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, "SELECT user_id, max(date), array_agg(option ORDER BY option) FROM poll_votes WHERE "+where+
		" GROUP BY user_id ORDER BY max(date) DESC, user_id DESC LIMIT $"+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, "", err
	}
	defer rows.Close()
	var out []PollVote
	for rows.Next() {
		var v PollVote
		if err := rows.Scan(&v.User, &v.Date, &v.Options); err != nil {
			return nil, 0, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = strconv.Itoa(last.Date) + ":" + strconv.FormatInt(last.User, 10)
	}
	return out, count, next, nil
}

// PollCopy is a message that shows a poll.
type PollCopy struct {
	Owner   int64 // private box; 0 for a channel
	Peer    int64
	Channel int64
	ID      int
}

// PollCopies are the messages showing a poll: everyone who must hear
// about a vote.
func (s *Service) PollCopies(ctx context.Context, poll int64) ([]PollCopy, error) {
	rows, err := s.db.Query(ctx, `SELECT owner_id, peer_id, 0, id FROM messages WHERE media_kind = 'poll' AND media_id = $1
		UNION ALL SELECT 0, 0, channel_id, id FROM channel_messages WHERE media_kind = 'poll' AND media_id = $1`, poll)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PollCopy, error) {
		var c PollCopy
		err := r.Scan(&c.Owner, &c.Peer, &c.Channel, &c.ID)
		return c, err
	})
}
