// Package admin is the HTTP API behind the admin panel (admin/ in this
// repository). Routes and JSON follow greengram-web's src/admin-*.js and the
// admin parts of its src/api.js, which the panel was first written against.
//
// Pieces owned by other features (stars, bots, stickers, fragment, crypto)
// are not served here; they register their own /api/admin/... routes and
// can add their part of GET /api/admin through Extra.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/erase"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/settings"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// API leaves /api/admin/broadcasts to internal/web.Broadcasts.
type API struct {
	DB       *pgxpool.Pool
	Accounts *account.Service
	Settings *settings.Source
	Channels *channels.Service

	// optional
	// StarsAdjust is stars.Service.Adjust: the panel's Stars transactions,
	// one ledger entry per request id. It returns the ledger transaction id
	// and the new balance; refusals are *account.Error.
	StarsAdjust func(ctx context.Context, actor, recipient, amount int64, note, requestID string) (string, int64, error)
	// Gifts is the gift catalog; without it /api/admin/gifts isn't served.
	Gifts *stars.Service
	// Media serves pictures to the panel and backs its Stickers & Emoji page.
	Media *media.Service
	// Extra adds keys to GET /api/admin (bots, starTransactions, ...).
	Extra []func(ctx context.Context, out map[string]any) error
	// AccessChanged tells someone's apps their account state changed.
	AccessChanged func(user int64)
	// Erased signs a deleted account's apps out and delivers its chat
	// updates (api.AccountErased).
	Erased func(erase.Result)
	// IdentityReset tells the account's apps and the people who see it
	// that its profile was reset (api.IdentityReset).
	IdentityReset func(user int64)
	// AnswerAppeal has @SpamBot tell someone what was decided.
	AnswerAppeal func(ctx context.Context, user int64, approved bool, reason string) error
	// Moderator carries out the Reports page's deletes and bans.
	Moderator Moderator

	started time.Time
	metrics metrics
}

func New(db *pgxpool.Pool, accounts *account.Service, set *settings.Source, chans *channels.Service) *API {
	return &API{DB: db, Accounts: accounts, Settings: set, Channels: chans, started: time.Now()}
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin", a.handle(a.overview))
	mux.HandleFunc("PATCH /api/admin", a.handle(a.action))
	mux.HandleFunc("GET /api/admin/users/{id}", a.handle(a.userDetail))
	mux.HandleFunc("PATCH /api/admin/users/{id}", a.handle(a.editUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}/access", a.handle(a.access))
	mux.HandleFunc("GET /api/admin/appeals", a.handle(a.appeals))
	a.registerReports(mux)
	mux.HandleFunc("POST /api/admin/appeals/{id}/approve", a.handle(a.approveAppeal))
	mux.HandleFunc("POST /api/admin/appeals/{id}/reject", a.handle(a.rejectAppeal))
	mux.HandleFunc("POST /api/admin/users/{id}/password", a.handle(a.resetPassword))
	mux.HandleFunc("POST /api/admin/users/{id}/reset", a.handle(a.resetIdentity))
	mux.HandleFunc("DELETE /api/admin/users/{id}/sessions", a.handle(a.endUserSessions))
	mux.HandleFunc("DELETE /api/admin/users/{id}/sessions/{session}", a.handle(a.endUserSessions))
	mux.HandleFunc("GET /api/admin/bots/{id}", a.handle(a.botDetail))
	mux.HandleFunc("PATCH /api/admin/bots/{id}", a.handle(a.editBot))
	mux.HandleFunc("PATCH /api/admin/bots/{id}/verifier", a.handle(a.editBotVerifier))
	mux.HandleFunc("PATCH /api/admin/verification", a.handle(a.verification))
	mux.HandleFunc("GET /api/admin/sessions", a.handle(a.sessions))
	mux.HandleFunc("DELETE /api/admin/sessions", a.handle(a.endSessions))
	mux.HandleFunc("DELETE /api/admin/sessions/{session}", a.handle(a.endSessions))
	mux.HandleFunc("GET /api/admin/health", a.handle(a.health))
	mux.HandleFunc("GET /api/admin/stats", a.handle(a.stats))
	mux.HandleFunc("GET /api/admin/star-transactions", a.handle(a.starJournal))
	mux.HandleFunc("POST /api/admin/star-transactions", a.handle(a.starTransaction))
	if a.Gifts != nil {
		mux.HandleFunc("GET /api/admin/gifts", a.handle(a.gifts))
		mux.HandleFunc("POST /api/admin/gifts", a.handle(a.addGift))
		mux.HandleFunc("PATCH /api/admin/gifts/{id}", a.handle(a.editGift))
		mux.HandleFunc("DELETE /api/admin/gifts/{id}", a.handle(a.removeGift))
		mux.HandleFunc("GET /api/admin/collectibles", a.handle(a.collectibles))
		mux.HandleFunc("GET /api/admin/gift-market", a.handle(a.giftMarket))
		mux.HandleFunc("PATCH /api/admin/gift-market", a.handle(a.editGiftMarket))
	}
	if a.Media != nil {
		mux.HandleFunc("GET /api/admin/photos/{id}", a.file(a.photo))
		mux.HandleFunc("GET /api/admin/sticker-packs", a.handle(a.stickerPacks))
		mux.HandleFunc("GET /api/admin/sticker-packs/{id}/items", a.handle(a.stickerPackItems))
		mux.HandleFunc("PATCH /api/admin/sticker-packs/{id}", a.handle(a.editStickerPack))
		mux.HandleFunc("DELETE /api/admin/sticker-packs/{id}", a.handle(a.deleteStickerPack))
		mux.HandleFunc("GET /api/sticker-media/{id}", a.file(a.stickerFile))
	}
	a.registerCirculation(mux)
	mux.HandleFunc("GET /api/admin/settings", a.handle(a.getSettings))
	mux.HandleFunc("PATCH /api/admin/settings", a.handle(a.saveSettings))
	mux.HandleFunc("POST /api/admin/countries", a.handle(a.saveCountry))
	mux.HandleFunc("DELETE /api/admin/countries", a.handle(a.deleteCountry))
	mux.HandleFunc("DELETE /api/admin/conversations/{id}", a.handle(a.deleteConversation))
	mux.HandleFunc("POST /api/admin/branding/logo", a.handle(a.uploadLogo))
	mux.HandleFunc("DELETE /api/admin/branding/logo", a.handle(a.resetLogo))

	// public, the panel and the sign-in page read them signed out too
	mux.HandleFunc("GET /api/branding", a.public(a.branding))
	mux.HandleFunc("GET /api/countries", a.public(a.countries))
	mux.HandleFunc("GET /branding/logo", a.logo)
}

type req struct {
	r       *http.Request
	w       http.ResponseWriter
	ctx     context.Context
	body    map[string]any
	me      account.User
	session string // hash of the admin's own session
}

func (q *req) has(key string) bool { _, ok := q.body[key]; return ok }

func (q *req) str(key string) (string, bool) {
	s, ok := q.body[key].(string)
	return s, ok
}

// int reads a JSON number that must be a whole number.
func (q *req) int(key string) (int64, bool) {
	f, ok := q.body[key].(float64)
	if !ok || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

func (q *req) boolean(key string) (bool, bool) {
	b, ok := q.body[key].(bool)
	return b, ok
}

func (q *req) pathID(name string) (int64, error) {
	n, err := strconv.ParseInt(q.r.PathValue(name), 10, 64)
	if err != nil || n < 1 {
		return 0, fail(404, "Account not found.")
	}
	return n, nil
}

func fail(status int, msg string) error { return &account.Error{Status: status, Message: msg} }

var errAdmin = fail(403, "Administrator access is required.")

// sessionCookie is the cookie of the account slot the panel was opened in
// (X-Greengram-Account / ?account=, same as internal/web).
func sessionCookie(r *http.Request) (string, error) {
	v := r.Header.Get("X-Greengram-Account")
	if v == "" {
		v = r.URL.Query().Get("account")
	}
	name := "session"
	if v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 6 {
			return "", fail(400, "Choose an account slot between 1 and 6.")
		}
		if n > 1 {
			name += strconv.Itoa(n)
		}
	}
	c, err := r.Cookie(name)
	if err != nil {
		return "", nil
	}
	return c.Value, nil
}

// Admin resolves the request to a signed-in administrator; the page
// handler for /admin uses it too.
func (a *API) Admin(r *http.Request) (account.User, string, error) {
	token, err := sessionCookie(r)
	if err != nil {
		return account.User{}, "", err
	}
	u, hash, err := a.Accounts.SessionUser(r.Context(), token)
	if err != nil {
		return u, "", err
	}
	if u.Role != "admin" || u.Disabled {
		return u, "", errAdmin
	}
	return u, hash, nil
}

func (a *API) handle(fn func(*req) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		q := &req{r: r, w: w, ctx: r.Context(), body: map[string]any{}}
		var err error
		q.me, q.session, err = a.Admin(r)
		if err == nil && r.Method != http.MethodGet && r.URL.Path != "/api/admin/branding/logo" {
			data, rerr := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if rerr == nil && len(data) > 0 && json.Unmarshal(data, &q.body) != nil {
				err = fail(400, "Send a JSON object.")
			}
		}
		var out any
		if err == nil {
			out, err = fn(q)
		}
		reply(w, r, out, err)
	}
}

// file answers with raw bytes for an <img> in the panel, admins only. The
// mime is the handler's, and the headers keep it from running as a page.
func (a *API) file(fn func(*req) ([]byte, string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := &req{r: r, w: w, ctx: r.Context(), body: map[string]any{}}
		var err error
		var data []byte
		var mime string
		q.me, q.session, err = a.Admin(r)
		if err == nil {
			data, mime, err = fn(q)
		}
		if err != nil {
			reply(w, r, nil, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", mime)
		h.Set("Cache-Control", "private, max-age=86400")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Write(data)
	}
}

func (a *API) public(fn func(*req) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out, err := fn(&req{r: r, w: w, ctx: r.Context()})
		reply(w, r, out, err)
	}
}

func reply(w http.ResponseWriter, r *http.Request, out any, err error) {
	var e *account.Error
	var inv *settings.Invalid
	switch {
	case errors.As(err, &e):
		writeJSON(w, e.Status, map[string]string{"error": e.Message})
	case errors.As(err, &inv):
		writeJSON(w, 400, map[string]string{"error": inv.Message})
	case err != nil:
		slog.Error("admin: "+r.URL.Path, "err", err)
		writeJSON(w, 500, map[string]string{"error": "Something went wrong. Try again."})
	default:
		writeJSON(w, 200, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (a *API) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, a.DB, fn)
}

// audit records an admin action in tx; details become the entry's JSON.
func audit(ctx context.Context, tx pgx.Tx, actor int64, action string, target int64, details any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_log(actor_id, action, target_id, details) VALUES ($1, $2, $3, $4)", actor, action, target, raw)
	return err
}

func ok() map[string]bool { return map[string]bool{"ok": true} }
