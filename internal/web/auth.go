// Package web serves the HTTP side: the sign-in API the Android app and
// the website call. Paths, JSON and cookies match greengram-web's.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/clientip"
)

type Auth struct {
	Accounts *account.Service
	// Bots checks mini app launch data; nil takes only the cookie.
	Bots *bots.Service
	// SecureCookies adds Secure; set when the site is served over HTTPS.
	SecureCookies bool
	// PhoneChanged tells the account's apps and contacts about a new number.
	PhoneChanged func(user int64)
}

func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/start", a.handle(a.start))
	mux.HandleFunc("POST /api/auth/intent", a.handle(a.intent))
	mux.HandleFunc("POST /api/auth/code", a.handle(a.code))
	mux.HandleFunc("POST /api/auth/password", a.handle(a.password))
	mux.HandleFunc("POST /api/auth/username", a.handle(a.username))
	mux.HandleFunc("POST /api/auth/register", a.handle(a.register))
	mux.HandleFunc("POST /api/auth/password/set", a.handle(a.setPassword))
	mux.HandleFunc("GET /api/account/deletion", a.handle(a.deletion))
	mux.HandleFunc("POST /api/account/deletion", a.handle(a.confirmDeletion))
	mux.HandleFunc("POST /api/account/phone", a.handle(a.changePhone))
	mux.HandleFunc("POST /api/auth/approve", a.handle(a.approve))
	mux.HandleFunc("GET /api/auth/status", a.handle(a.status))
	mux.HandleFunc("POST /api/auth/webtoken", a.handle(a.webToken))
	mux.HandleFunc("POST /api/login", a.handle(a.login))
	mux.HandleFunc("POST /api/register", a.handle(a.legacyRegister))
	mux.HandleFunc("GET /api/me", a.handle(a.me))
	mux.HandleFunc("POST /api/logout", a.handle(a.logout))
}

type req struct {
	r    *http.Request
	w    http.ResponseWriter
	ctx  context.Context
	body map[string]any
	slot int
}

func (q *req) str(key string) string {
	s, _ := q.body[key].(string)
	return s
}

func (q *req) client() account.Client {
	return account.Client{IP: clientip.From(q.r), UserAgent: q.r.UserAgent()}
}

// Up to six accounts per browser: slot N uses cookie "sessionN".
func (q *req) cookieName(name string) string {
	if q.slot == 1 {
		return name
	}
	return name + strconv.Itoa(q.slot)
}

func (q *req) cookie(name string) string {
	c, err := q.r.Cookie(q.cookieName(name))
	if err != nil {
		return ""
	}
	return c.Value
}

const sessionCookieAge = 365 * 24 * time.Hour

func (a *Auth) setCookie(q *req, name, value string, age time.Duration) {
	http.SetCookie(q.w, &http.Cookie{
		Name: q.cookieName(name), Value: value, Path: "/", MaxAge: int(age / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.SecureCookies,
	})
}

func (a *Auth) handle(fn func(*req) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		q := &req{r: r, w: w, ctx: r.Context(), body: map[string]any{}, slot: 1}
		if v := r.Header.Get("X-Greengram-Account"); v != "" || r.URL.Query().Has("account") {
			if v == "" {
				v = r.URL.Query().Get("account")
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 6 {
				writeJSON(w, 400, map[string]string{"error": "Choose an account slot between 1 and 6."})
				return
			}
			q.slot = n
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			data, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if err == nil && len(data) > 0 && json.Unmarshal(data, &q.body) != nil {
				writeJSON(w, 400, map[string]string{"error": "Send a JSON object."})
				return
			}
		}
		out, err := fn(q)
		var e *account.Error
		switch {
		case errors.As(err, &e):
			body := map[string]any{"error": e.Message}
			maps.Copy(body, e.Fields)
			writeJSON(w, e.Status, body)
		case err != nil:
			slog.Error("web: "+r.URL.Path, "err", err)
			writeJSON(w, 500, map[string]string{"error": "Something went wrong. Try again."})
		default:
			writeJSON(w, 200, out)
		}
	}
}

// writable is enforceAccountAccess(): a frozen account only reads.
func (q *req) writable(u account.User) error {
	switch q.r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if u.Frozen() {
		return account.ErrFrozen
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (a *Auth) start(q *req) (any, error) {
	mode := "qr"
	if q.str("mode") == "phone" {
		mode = "phone"
	}
	f, err := a.Accounts.StartFlow(q.ctx, mode, q.cookie("signin_flow"), q.client())
	if err != nil {
		return nil, err
	}
	a.setCookie(q, "signin_flow", f.Token, 10*time.Minute)
	out := map[string]any{"state": f.State, "expires": f.Expires.Unix()}
	if f.QR != "" {
		out["token"] = f.QR
	}
	return out, nil
}

func (a *Auth) intent(q *req) (any, error) {
	return ok(a.Accounts.SetIntent(q.ctx, q.cookie("signin_flow"), q.str("intent")))
}

func (a *Auth) code(q *req) (any, error) {
	state, made, err := a.Accounts.GiveNumber(q.ctx, q.cookie("signin_flow"), q.str("phone"), q.client())
	if err != nil {
		return nil, err
	}
	if made != "" {
		return map[string]string{"state": state, "phone": made}, nil
	}
	return map[string]string{"state": state}, nil
}

func (a *Auth) password(q *req) (any, error) {
	s, err := a.Accounts.SignInPhone(q.ctx, q.cookie("signin_flow"), q.str("password"), q.client())
	return a.ready(q, s, err)
}

func (a *Auth) username(q *req) (any, error) {
	s, err := a.Accounts.SignInUsername(q.ctx, q.cookie("signin_flow"), q.str("username"), q.str("password"), q.client())
	return a.ready(q, s, err)
}

func (a *Auth) register(q *req) (any, error) {
	s, err := a.Accounts.Register(q.ctx, q.cookie("signin_flow"), account.Registration{
		FirstName: q.str("firstName"), LastName: q.str("lastName"), Username: q.str("username"), Password: q.str("password"),
	}, q.client())
	return a.ready(q, s, err)
}

// ready is the end of a flow: session cookie in, flow cookie out.
func (a *Auth) ready(q *req, s account.Session, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	a.setCookie(q, "session", s.Token, sessionCookieAge)
	a.setCookie(q, "signin_flow", "", 0)
	return map[string]any{"state": "ready", "user": userJSON(s.User)}, nil
}

func (a *Auth) setPassword(q *req) (any, error) {
	return ok(a.Accounts.SetPassword(q.ctx, q.cookie("session"), q.str("currentPassword"), q.str("password"), q.client()))
}

// deletion tells the apps whether deleting the account will ask for the
// password; confirmDeletion checks it (see account.ConfirmDeletion), and
// account.deleteAccount follows over MTProto.
func (a *Auth) deletion(q *req) (any, error) {
	u, _, err := a.Accounts.SessionUser(q.ctx, q.cookie("session"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"hasPassword": u.HasPassword(), "windowSeconds": int(account.DeletionWindow.Seconds())}, nil
}

func (a *Auth) confirmDeletion(q *req) (any, error) {
	return ok(a.Accounts.ConfirmDeletion(q.ctx, q.cookie("session"), q.str("password"), q.client()))
}

// changePhone is the website's Change Number: the new number and the
// password, no code (see account.ChangePhoneWithPassword).
func (a *Auth) changePhone(q *req) (any, error) {
	u, err := a.Accounts.ChangePhoneWithPassword(q.ctx, q.cookie("session"), q.str("phone"), q.str("password"), q.client())
	if err != nil {
		return nil, err
	}
	if a.PhoneChanged != nil {
		go a.PhoneChanged(u.ID)
	}
	return map[string]any{"ok": true, "phoneNumber": u.Phone}, nil
}

func (a *Auth) approve(q *req) (any, error) {
	u, _, err := a.Accounts.SessionUser(q.ctx, q.cookie("session"))
	if err != nil {
		return nil, err
	}
	token := q.str("token")
	if len(strings.TrimPrefix(token, "tg://login?token=")) != 64 {
		return nil, &account.Error{Status: 400, Message: "Scan a valid sign-in QR code."}
	}
	return ok(a.Accounts.ApproveQR(q.ctx, u.ID, token))
}

func (a *Auth) status(q *req) (any, error) {
	state, s, err := a.Accounts.PollQR(q.ctx, q.cookie("signin_flow"), q.client())
	if err != nil {
		return nil, err
	}
	if s == nil {
		return map[string]string{"state": state}, nil
	}
	return a.ready(q, *s, nil)
}

// login and legacyRegister answer with the user itself, like the old API.
func (a *Auth) login(q *req) (any, error) {
	s, err := a.Accounts.Login(q.ctx, q.str("username"), q.str("password"), q.client())
	if err != nil {
		return nil, err
	}
	a.setCookie(q, "session", s.Token, sessionCookieAge)
	return userJSON(s.User), nil
}

func (a *Auth) legacyRegister(q *req) (any, error) {
	s, err := a.Accounts.RegisterUsername(q.ctx, q.str("username"), q.str("name"), q.str("password"), q.client())
	if err != nil {
		return nil, err
	}
	a.setCookie(q, "session", s.Token, sessionCookieAge)
	return userJSON(s.User), nil
}

func (a *Auth) me(q *req) (any, error) {
	u, _, err := a.Accounts.SessionUser(q.ctx, q.cookie("session"))
	if err != nil {
		return nil, err
	}
	return userJSON(u), nil
}

func (a *Auth) logout(q *req) (any, error) {
	if err := a.Accounts.Logout(q.ctx, q.cookie("session")); err != nil {
		return nil, err
	}
	a.setCookie(q, "session", "", 0)
	return map[string]bool{"ok": true}, nil
}

func ok(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// userJSON is the part of the web's safeUser() a signed-in client needs now;
// the rest comes back as the features do.
func userJSON(u account.User) map[string]any {
	now := time.Now()
	username := u.Username
	if !u.UsernamePublic {
		username = ""
	}
	return map[string]any{
		"id":           u.ID,
		"username":     username,
		"name":         u.Name,
		"role":         u.Role,
		"disabled":     u.Disabled,
		"isSystem":     u.IsSystem,
		"phoneNumber":  u.Phone,
		"bio":          u.Bio,
		"premiumUntil": u.PremiumUntil,
		"isPremium":    u.Premium(now),
		"accountState": u.State,
		"createdAt":    fmt.Sprint(u.CreatedAt.Unix()),
	}
}
