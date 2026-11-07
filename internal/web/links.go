package web

import (
	"net/http"
	"strconv"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/legacy"
)

// RegisterLinks keeps greengram-web's message links working after the
// import. /api/messages/<id>/link has the web's path and JSON, with this
// server's ids; /m/<id> (what the links point at) answers the same until
// the web client takes the route over and opens the chat itself.
func (a *Auth) RegisterLinks(mux *http.ServeMux, links *legacy.Links) {
	h := a.handle(func(q *req) (any, error) { return a.link(q, links) })
	mux.HandleFunc("GET /api/messages/{id}/link", h)
	mux.HandleFunc("GET /m/{id}", h)
}

func (a *Auth) link(q *req, links *legacy.Links) (any, error) {
	u, _, err := a.Accounts.SessionUser(q.ctx, q.cookie("session"))
	if err != nil {
		return nil, err
	}
	notFound := &account.Error{Status: 404, Message: "Message not found."}
	old, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil || old < 1 {
		return nil, notFound
	}
	l, ok, err := links.Resolve(q.ctx, u.ID, old)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound
	}
	scheme := "http"
	if a.SecureCookies {
		scheme = "https"
	}
	url := scheme + "://" + q.r.Host + "/m/" + strconv.FormatInt(old, 10)
	if q.slot > 1 {
		url += "?account=" + strconv.Itoa(q.slot)
	}
	return map[string]any{"url": url, "peerId": l.PeerID, "messageId": l.MessageID}, nil
}
