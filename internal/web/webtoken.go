package web

// webToken hands the browser's session to Telegram Web A. The cookie is
// HttpOnly, so the page can't read it, but Web A has to send it in
// auth.importAuthorization like the apps do; after that its auth key in
// localStorage is the credential anyway, bound to this same session.
//
// No CSRF check needed: the cookie is SameSite=Strict, so a cross-site
// request arrives without it, and without CORS headers no other origin can
// read the answer.
func (a *Auth) webToken(q *req) (any, error) {
	token := q.cookie("session")
	u, _, err := a.Accounts.SessionUser(q.ctx, token)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": token, "userId": u.ID}, nil
}
