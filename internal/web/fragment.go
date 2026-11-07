package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/fragment"
)

// Fragment serves the Fragment page's API, paths as in greengram-web's api.js.
type Fragment struct {
	Auth     *Auth
	Fragment *fragment.Service
}

func (f *Fragment) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/fragment/catalog", f.Auth.handle(f.signedIn(f.catalog)))
	mux.HandleFunc("POST /api/fragment/quote", f.Auth.handle(f.signedIn(f.quote)))
	mux.HandleFunc("POST /api/fragment/purchase", f.Auth.handle(f.signedIn(f.purchase)))
	mux.HandleFunc("POST /api/fragment/listing", f.Auth.handle(f.signedIn(f.publish)))
	mux.HandleFunc("DELETE /api/fragment/listing", f.Auth.handle(f.signedIn(f.cancel)))
	mux.HandleFunc("POST /api/fragment/assign", f.Auth.handle(f.signedIn(f.assign)))
	mux.HandleFunc("POST /api/admin/fragment/listing", f.Auth.handle(f.signedIn(f.reserve)))
	mux.HandleFunc("DELETE /api/admin/fragment/listing", f.Auth.handle(f.signedIn(f.unreserve)))
}

type fragmentFn func(q *req, u account.User, sessionHash string) (any, error)

// signedIn resolves the mini app launch or the session cookie. handle() only reads POST bodies;
// DELETE carries one here too.
func (f *Fragment) signedIn(fn fragmentFn) func(*req) (any, error) {
	return func(q *req) (any, error) {
		if q.r.Method == http.MethodDelete {
			data, err := io.ReadAll(io.LimitReader(q.r.Body, 64<<10))
			if err == nil && len(data) > 0 && json.Unmarshal(data, &q.body) != nil {
				return nil, &account.Error{Status: 400, Message: "Send a JSON object."}
			}
		}
		if q.r.Header.Get("X-Telegram-Init-Data") == "" && q.r.Method != http.MethodGet && !sameOrigin(q.r) {
			return nil, &account.Error{Status: 403, Message: "Open Fragment on this site to continue."}
		}
		u, hash, err := f.Auth.appUser(q, bots.Fragment)
		if err != nil {
			return nil, err
		}
		return fn(q, u, hash)
	}
}

// sameOrigin backs up the SameSite=Strict session cookie for the Fragment
// page opened straight in a browser tab: a write signed in by the cookie
// has to come from a page on this site. Browsers send Sec-Fetch-Site or
// Origin on every POST and DELETE; a client sending neither isn't a
// browser, so carries no ambient cookie to abuse.
func sameOrigin(r *http.Request) bool {
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" {
		return v == "same-origin"
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func (f *Fragment) admin(u account.User) error {
	if u.Role != "admin" {
		return &account.Error{Status: 403, Message: "Administrator access is required."}
	}
	return nil
}

// str is the old code's String(data.x || ""): numbers keep their
// shortest form, false/null/missing become "".
func (f *Fragment) str(q *req, key string) string {
	switch v := q.body[key].(type) {
	case string:
		return v
	case float64:
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
	}
	return ""
}

// optional is a field that is only applied when the request sent it.
func (f *Fragment) optional(q *req, key string) *string {
	if _, ok := q.body[key]; !ok {
		return nil
	}
	v := f.str(q, key)
	return &v
}

func (f *Fragment) isTrue(q *req, key string) bool {
	v, _ := q.body[key].(bool)
	return v
}

func (f *Fragment) listing(q *req) fragment.Listing {
	kind, _ := q.body["kind"].(string)
	return fragment.Listing{
		Kind: kind, Value: f.str(q, "value"), Amount: f.str(q, "amount"),
		CountryCode: f.optional(q, "countryCode"), DisplayFormat: f.optional(q, "displayFormat"),
		AlternativeID: f.str(q, "alternativeId"), ChannelUsername: f.str(q, "channelUsername"), Password: q.str("password"),
	}
}

func (f *Fragment) catalog(q *req, u account.User, _ string) (any, error) {
	return f.Fragment.Catalog(q.ctx, u.ID, false)
}

func (f *Fragment) quote(q *req, u account.User, _ string) (any, error) {
	return f.Fragment.Quote(q.ctx, u.ID, f.str(q, "itemId"))
}

func (f *Fragment) purchase(q *req, u account.User, _ string) (any, error) {
	return f.Fragment.Purchase(q.ctx, u.ID, f.str(q, "quoteId"))
}

func (f *Fragment) publish(q *req, u account.User, hash string) (any, error) {
	return f.Fragment.Publish(q.ctx, u.ID, hash, f.listing(q))
}

func (f *Fragment) cancel(q *req, u account.User, _ string) (any, error) {
	return f.Fragment.Cancel(q.ctx, u.ID, f.str(q, "itemId"), false, false)
}

func (f *Fragment) assign(q *req, u account.User, hash string) (any, error) {
	return f.Fragment.Assign(q.ctx, u.ID, hash, f.str(q, "itemId"), q.str("password"), f.isTrue(q, "clear"))
}

func (f *Fragment) reserve(q *req, u account.User, _ string) (any, error) {
	if err := f.admin(u); err != nil {
		return nil, err
	}
	return f.Fragment.Reserve(q.ctx, u.ID, f.listing(q))
}

func (f *Fragment) unreserve(q *req, u account.User, _ string) (any, error) {
	if err := f.admin(u); err != nil {
		return nil, err
	}
	return f.Fragment.Cancel(q.ctx, u.ID, f.str(q, "itemId"), true, f.isTrue(q, "remove"))
}
