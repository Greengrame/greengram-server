package admin

import (
	"bufio"
	"math"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// metrics is the request counter behind the health page: totals since
// start, the last 500 latencies and a per-minute histogram for an hour.
type metrics struct {
	mu        sync.Mutex
	total     int64
	client    int64
	server    int64
	latencies []float64
	minutes   map[int64]int64
}

func (m *metrics) record(status int, ms float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	minute := time.Now().Unix() / 60
	m.total++
	if status >= 500 {
		m.server++
	} else if status >= 400 {
		m.client++
	}
	m.latencies = append(m.latencies, ms)
	if len(m.latencies) > 500 {
		m.latencies = m.latencies[1:]
	}
	if m.minutes == nil {
		m.minutes = map[int64]int64{}
	}
	m.minutes[minute]++
	for k := range m.minutes {
		if k < minute-59 {
			delete(m.minutes, k)
		}
	}
}

func percentile(values []float64, f float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	return math.Round(s[min(len(s)-1, int(float64(len(s))*f))]*10) / 10
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Count wraps the HTTP handler so /api/ requests show up on the health
// page. Everything else (websocket, static files) passes straight through.
func (a *API) Count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		a.metrics.record(sw.status, float64(time.Since(start).Microseconds())/1000)
	})
}

// online means a session was used in the last five minutes; sessions
// touch last_active at most once a minute.
const onlineWindow = "interval '5 minutes'"

func (a *API) health(q *req) (any, error) {
	ctx := q.ctx
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	total, free := hostMemory()
	if total == 0 {
		total = int64(ms.Sys)
	}
	var dbSize, streams, online int64
	var t [6]int64
	err := a.DB.QueryRow(ctx, `SELECT pg_database_size(current_database()),
		(SELECT count(*) FROM users u WHERE `+people+`),
		(SELECT count(*) FROM users WHERE is_bot AND NOT is_system),
		(SELECT count(*) FROM channels WHERE NOT deleted),
		(SELECT count(*) FROM messages WHERE owner_id=from_id) + (SELECT count(*) FROM channel_messages WHERE action IS NULL),
		(SELECT count(*) FROM sessions WHERE expires_at > now()),
		(SELECT count(*) FROM audit_log),
		(SELECT count(*) FROM sessions WHERE last_active > now() - `+onlineWindow+`),
		(SELECT count(DISTINCT user_id) FROM sessions WHERE last_active > now() - `+onlineWindow+`)`).Scan(
		&dbSize, &t[0], &t[1], &t[2], &t[3], &t[4], &t[5], &streams, &online)
	if err != nil {
		return nil, err
	}
	tables := map[string]int64{"users": t[0], "bots": t[1], "chats": t[2], "messages": t[3], "sessions": t[4], "audit": t[5]}

	a.metrics.mu.Lock()
	minute := time.Now().Unix() / 60
	perMinute := make([]int64, 60)
	for i := range perMinute {
		perMinute[i] = a.metrics.minutes[minute-59+int64(i)]
	}
	requests := map[string]any{
		"total": a.metrics.total, "clientErrors": a.metrics.client, "serverErrors": a.metrics.server,
		"p50": percentile(a.metrics.latencies, 0.5), "p95": percentile(a.metrics.latencies, 0.95), "perMinute": perMinute,
	}
	a.metrics.mu.Unlock()

	empty := map[string]int64{"count": 0, "bytes": 0}
	return map[string]any{
		"process": map[string]any{
			"uptime": int64(time.Since(a.started).Seconds()), "startedAt": a.started.Unix(),
			"rss": ms.Sys, "heapUsed": ms.HeapAlloc, "heapTotal": ms.HeapSys,
			"runtime": runtime.Version(), "platform": runtime.GOOS + " " + runtime.GOARCH, "pid": os.Getpid(),
		},
		"host": map[string]any{"cpus": runtime.NumCPU(), "load": loadAvg(), "totalMemory": total, "freeMemory": free},
		// the panel's database card was written for SQLite; pages = size, no WAL to show
		"database": map[string]any{"file": dbSize, "wal": 0, "pages": dbSize, "free": 0, "journal": "postgres"},
		// media is its own feature; it fills these in when it lands
		"storage":  map[string]any{"files": empty, "photos": empty},
		"tables":   tables,
		"live":     map[string]int64{"streams": streams, "online": online},
		"requests": requests,
	}, nil
}

func loadAvg() []float64 {
	out := []float64{0, 0, 0}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return out
	}
	for i, f := range strings.Fields(string(b)) {
		if i == 3 {
			break
		}
		out[i], _ = strconv.ParseFloat(f, 64)
	}
	return out
}

func hostMemory() (total, free int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, _ := strings.Cut(s.Text(), ":")
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			free = n * 1024
		}
	}
	return total, free
}

// sent is every message once: the sender's copy of private ones, plus
// non-service channel posts.
const sent = `(SELECT date, from_id, 0::bigint AS channel_id FROM messages WHERE owner_id=from_id AND date >= $1
	UNION ALL SELECT date, from_id, channel_id FROM channel_messages WHERE action IS NULL AND date >= $1)`

func (a *API) stats(q *req) (any, error) {
	ctx := q.ctx
	days := int64(30)
	if d, _ := strconv.ParseInt(q.r.URL.Query().Get("days"), 10, 64); d == 7 || d == 90 {
		days = d
	}
	today := time.Now().Unix() / 86400 * 86400
	from := today - (days-1)*86400
	previous := from - days*86400

	type day struct {
		messages, signups, active int64
	}
	byDay := map[string]*day{}
	get := func(k string) *day {
		if byDay[k] == nil {
			byDay[k] = &day{}
		}
		return byDay[k]
	}
	dayOf := "to_char(to_timestamp(%s) AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
	collect := func(sql string, set func(*day, int64), args ...any) error {
		rows, err := a.DB.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int64
			if err := rows.Scan(&k, &n); err != nil {
				return err
			}
			set(get(k), n)
		}
		return rows.Err()
	}
	if err := collect("SELECT "+strings.Replace(dayOf, "%s", "m.date", 1)+", count(*) FROM "+sent+" m GROUP BY 1",
		func(d *day, n int64) { d.messages = n }, from); err != nil {
		return nil, err
	}
	if err := collect("SELECT "+strings.Replace(dayOf, "%s", "m.date", 1)+", count(DISTINCT m.from_id) FROM "+sent+" m JOIN users u ON u.id=m.from_id WHERE NOT u.is_system GROUP BY 1",
		func(d *day, n int64) { d.active = n }, from); err != nil {
		return nil, err
	}
	if err := collect("SELECT to_char(u.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), count(*) FROM users u WHERE u.created_at >= to_timestamp($1) AND "+people+" GROUP BY 1",
		func(d *day, n int64) { d.signups = n }, from); err != nil {
		return nil, err
	}
	series := make([]map[string]any, days)
	for i := range series {
		k := time.Unix(from+int64(i)*86400, 0).UTC().Format("2006-01-02")
		d := get(k)
		series[i] = map[string]any{"day": k, "messages": d.messages, "signups": d.signups, "active": d.active}
	}

	var t [6]int64
	if err := a.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM `+sent+` m WHERE m.date >= $2),
		(SELECT count(*) FROM `+sent+` m WHERE m.date < $2),
		(SELECT count(*) FROM users u WHERE u.created_at >= to_timestamp($2) AND `+people+`),
		(SELECT count(*) FROM users u WHERE u.created_at >= to_timestamp($1) AND u.created_at < to_timestamp($2) AND `+people+`),
		(SELECT count(DISTINCT m.from_id) FROM `+sent+` m JOIN users u ON u.id=m.from_id WHERE m.date >= $2 AND NOT u.is_system),
		(SELECT count(DISTINCT m.from_id) FROM `+sent+` m JOIN users u ON u.id=m.from_id WHERE m.date < $2 AND NOT u.is_system)`,
		previous, from).Scan(&t[0], &t[1], &t[2], &t[3], &t[4], &t[5]); err != nil {
		return nil, err
	}
	totals := map[string]int64{"messages": t[0], "messagesBefore": t[1], "signups": t[2], "signupsBefore": t[3], "active": t[4], "activeBefore": t[5]}

	hours := make([]int64, 24)
	rows, err := a.DB.Query(ctx, "SELECT extract(hour FROM to_timestamp(m.date) AT TIME ZONE 'UTC')::int, count(*) FROM "+sent+" m GROUP BY 1", from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h int
		var n int64
		if err := rows.Scan(&h, &n); err != nil {
			rows.Close()
			return nil, err
		}
		hours[h] = n
	}
	rows.Close()

	rows, err = a.DB.Query(ctx, `SELECT c.id, c.title, coalesce(c.username, ''), c.megagroup, count(*),
		(SELECT count(*) FROM channel_members v WHERE v.channel_id=c.id AND v.status IN ('creator', 'admin', 'member', 'restricted'))
		FROM channel_messages m JOIN channels c ON c.id=m.channel_id
		WHERE m.date >= $1 AND m.action IS NULL AND NOT c.deleted GROUP BY c.id ORDER BY 5 DESC LIMIT 5`, from)
	if err != nil {
		return nil, err
	}
	topChats, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, msgs, members int64
		var title, username string
		var group bool
		err := r.Scan(&id, &title, &username, &group, &msgs, &members)
		isChannel := 1
		if group {
			isChannel = 0
		}
		return map[string]any{"id": id, "kind": "group", "title": title, "username": nilIfEmpty(username), "isChannel": isChannel, "messages": msgs, "members": members}, err
	})
	if err != nil {
		return nil, err
	}

	rows, err = a.DB.Query(ctx, `SELECT u.id, u.name, u.username, count(*) FROM `+sent+` m JOIN users u ON u.id=m.from_id
		WHERE `+people+` GROUP BY u.id ORDER BY 4 DESC LIMIT 5`, from)
	if err != nil {
		return nil, err
	}
	topSenders, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, n int64
		var name, username string
		err := r.Scan(&id, &name, &username, &n)
		return map[string]any{"id": id, "name": name, "username": username, "messages": n}, err
	})
	if err != nil {
		return nil, err
	}

	return map[string]any{"days": days, "series": series, "totals": totals, "hours": hours, "topChats": topChats, "topSenders": topSenders}, nil
}

func (a *API) sessions(q *req) (any, error) {
	rows, err := a.DB.Query(q.ctx, `SELECT s.token_hash, s.user_id, u.name, u.username, u.role, `+agent+`,
		extract(epoch FROM s.created_at)::bigint, extract(epoch FROM s.last_active)::bigint, extract(epoch FROM s.expires_at)::bigint,
		s.last_active > now() - `+onlineWindow+`, coalesce(u.photo_id, 0)
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.expires_at > now() AND NOT u.is_system ORDER BY 10 DESC, s.last_active DESC LIMIT 2000`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var hash, name, username, role, ua string
		var user, created, active, expires, photo int64
		var online bool
		err := r.Scan(&hash, &user, &name, &username, &role, &ua, &created, &active, &expires, &online, &photo)
		out := map[string]any{"id": hash, "userId": user, "name": name, "username": username, "role": role, "userAgent": ua,
			"createdAt": created, "lastActive": active, "expiresAt": expires, "online": online, "isCurrent": hash == q.session}
		if photo != 0 {
			out["avatarPhotoId"] = strconv.FormatInt(photo, 10)
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessions": list}, nil
}

var hexSession = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (a *API) endSessions(q *req) (any, error) {
	id := q.r.PathValue("session")
	var n int64
	if id != "" {
		if !hexSession.MatchString(id) {
			return nil, fail(400, "Choose a session.")
		}
		if id == q.session {
			return nil, fail(400, "Your current session cannot be terminated here.")
		}
		err := a.tx(q.ctx, func(tx pgx.Tx) error {
			var owner int64
			if err := tx.QueryRow(q.ctx, "DELETE FROM sessions WHERE token_hash=$1 RETURNING user_id", id).Scan(&owner); err == pgx.ErrNoRows {
				return fail(404, "Session not found.")
			} else if err != nil {
				return err
			}
			n = 1
			return audit(q.ctx, tx, q.me.ID, "admin_sessions_revoked", owner, map[string]any{"revoked": 1, "scope": "selected"})
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "revoked": n}, nil
	}
	idle, ok := q.int("idleDays")
	if !ok || idle < 1 || idle > 365 {
		return nil, fail(400, "Choose between 1 and 365 idle days.")
	}
	err := a.tx(q.ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(q.ctx, "DELETE FROM sessions WHERE token_hash<>$1 AND last_active < now() - make_interval(days => $2)", q.session, int(idle))
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return audit(q.ctx, tx, q.me.ID, "admin_sessions_revoked", q.me.ID, map[string]any{"revoked": n, "scope": "idle", "idleDays": idle})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "revoked": n}, nil
}
