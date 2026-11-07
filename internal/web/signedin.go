package web

import (
	"net/http"
	"strconv"
)

// SignedIn: the request carries a live session for the account slot it
// opens (?account=N reads cookie sessionN, no number is slot 1), the same
// one Web A would pick up through /api/auth/webtoken.
func (a *Auth) SignedIn(r *http.Request) bool {
	q := &req{r: r, slot: 1}
	if v := r.URL.Query().Get("account"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 6 {
			return false
		}
		q.slot = n
	}
	_, _, err := a.Accounts.SessionUser(r.Context(), q.cookie("session"))
	return err == nil
}
