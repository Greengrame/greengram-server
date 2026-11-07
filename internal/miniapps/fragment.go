package miniapps

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Greengrame/greengram-server/internal/account"
)

// The Fragment page is fragment.com's own markup (index.html, listing.html
// and the username/number pages) cut down to a fixed-price shop, as
// greengram-web's fragment-page.js built it, driven by its frontend
// fragment.js.

var fragmentRoute = regexp.MustCompile(`^/(?:fragment/)?(username|number)/([a-zA-Z0-9_]+)$`)

func (s *Server) fragmentFile(key string) (*nethtml.Node, error) {
	l, ok := s.lock("fragment")
	if !ok {
		return nil, fmt.Errorf("fragment lock missing")
	}
	a, ok := l.Assets[key]
	if !ok {
		return nil, fmt.Errorf("fragment %s is not pinned", key)
	}
	data, err := s.read("fragment", key, a)
	if err != nil {
		return nil, err
	}
	return nethtml.Parse(strings.NewReader(string(data)))
}

// fragmentShell is the document: fragment.com's head and styles, with the
// content and footer left for the adapter to fill in.
func fragmentShell(page []byte) ([]byte, error) {
	doc, err := nethtml.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, err
	}
	content := first(doc, func(n *nethtml.Node) bool { return attr(n, "id") == "aj_content" })
	if content == nil {
		return nil, fmt.Errorf("fragment page has no #aj_content")
	}
	for content.FirstChild != nil {
		content.RemoveChild(content.FirstChild)
	}
	removeAll(doc, classIs("tm-footer"))
	if head := first(doc, tagIs("head")); head != nil {
		head.AppendChild(&nethtml.Node{Type: nethtml.ElementNode, Data: "meta", DataAtom: atom.Meta,
			Attr: []nethtml.Attribute{{Key: "name", Val: "color-scheme"}, {Key: "content", Val: "dark"}}})
	}
	return []byte(outer(doc)), nil
}

var fragmentAssets = regexp.MustCompile(`((?:src|href)=["'])/(css|js|img|fonts)/`)

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func truthy(m map[string]any, k string) bool {
	v, _ := m[k].(bool)
	return v
}

const fragmentConfirm = `<div id="fragment-confirm" class="popup-container hide" role="dialog" aria-modal="true" aria-hidden="true" aria-label="Confirm purchase"><div class="popup"><div class="popup-body"><section><h4>Confirm purchase</h4><p class="popup-text" id="fragment-confirm-text"></p><p class="help-block" id="fragment-confirm-error" role="alert"></p><div class="popup-buttons"><button class="btn btn-primary" id="fragment-pay" type="button">Buy</button><button class="btn btn-default" type="button" data-popup-close="fragment-confirm">Cancel</button></div></section></div></div></div>`

// fragmentPage is the signed-in page. slot is the site's account slot the
// page's /api/fragment calls name; the launch data overrides it in the app.
func (s *Server) fragmentPage(r *http.Request, u account.User, slot int) (pageView, error) {
	ctx := r.Context()
	catalog, err := s.Fragment.Catalog(ctx, u.ID, false)
	if err != nil {
		return pageView{}, err
	}
	var selected map[string]any
	if m := fragmentRoute.FindStringSubmatch(r.URL.Path); m != nil {
		if selected, err = s.Fragment.Item(ctx, u.ID, m[1], m[2]); err != nil {
			return pageView{}, err
		}
	}
	index, err := s.fragmentFile("index.html")
	if err != nil {
		return pageView{}, err
	}
	listingDoc, err := s.fragmentFile("listing.html")
	if err != nil {
		return pageView{}, err
	}
	sell := first(listingDoc, classIs("js-sell-username-popup"))
	header := first(index, func(n *nethtml.Node) bool { return n.Data == "header" && hasClass(n, "tm-header") })
	main := first(index, func(n *nethtml.Node) bool { return n.Data == "main" && hasClass(n, "tm-main") })
	if sell == nil || header == nil || main == nil {
		return pageView{}, fmt.Errorf("the pinned Fragment layout changed")
	}

	// sale popup
	listing := clone(sell)
	setAttr(listing, "id", "fragment-selling")
	setAttr(listing, "role", "dialog")
	setAttr(listing, "aria-hidden", "true")
	if f := first(listing, classIs("js-sell-username-form")); f != nil {
		setAttr(f, "id", "fragment-listing-form")
	}
	var options strings.Builder
	owned, _ := catalog["ownedAssets"].([]map[string]any)
	for _, a := range owned {
		if truthy(a, "listed") {
			continue
		}
		label := str(a, "displayTitle")
		if label == "" {
			label = str(a, "title")
		}
		if truthy(a, "isPrimary") {
			label += " — Primary"
		}
		if t := str(a, "channelTitle"); t != "" {
			label += " — " + t
		}
		options.WriteString(`<option value="` + html.EscapeString(str(a, "title")) + `">` + html.EscapeString(label) + `</option>`)
	}
	if in := first(listing, func(n *nethtml.Node) bool { return n.Data == "input" && attr(n, "name") == "username" }); in != nil {
		replace(in, `<div class="form-group"><label for="fragment-identifier">Asset</label><div class="form-control-wrap"><select name="username" id="fragment-identifier" class="form-control" required style="color:var(--field-color);background-color:var(--field-bg-color);color-scheme:dark">`+options.String()+`</select></div></div><input name="kind" type="hidden" value="username">`)
	}
	for _, n := range all(listing, classIs("tm-form-control-hint")) {
		setInner(n, `<p id="fragment-selling-error" role="alert"></p>`)
	}
	listingHTML := outer(listing)
	listingHTML = strings.Replace(listingHTML, "</form>", `<div class="form-main-button"><button type="button" class="btn btn-default btn-block" data-popup-close="fragment-selling">Cancel</button></div></form>`, 1)
	listingHTML = strings.Replace(listingHTML, `<div class="form-main-button">`, `<div id="fragment-number-options" hidden=""><div class="form-group"><label>Country code</label><div class="form-control-wrap"><input class="form-control" name="countryCode" inputmode="numeric"></div></div><div class="form-group"><label>Display format</label><div class="form-control-wrap"><input class="form-control" name="displayFormat" placeholder="+888 1234 5678"></div></div></div><div class="form-main-button">`, 1)
	listingHTML = strings.Replace(listingHTML, `<div class="form-main-button">`, `<div id="fragment-primary-options" hidden=""><div class="form-group" id="fragment-alternative-group"><label for="fragment-alternative">Replacement primary identifier</label><select class="form-control" name="alternativeId" id="fragment-alternative" style="color:var(--field-color);background-color:var(--field-bg-color);color-scheme:dark"></select></div><div class="form-group" id="fragment-channel-name-group"><label for="fragment-channel-name">New username (optional)</label><div class="form-control-wrap"><input name="channelUsername" id="fragment-channel-name" class="form-control" autocomplete="off" spellcheck="false" maxlength="32"></div></div><div class="form-group"><label for="fragment-sale-password">Current sign-in password</label><div class="form-control-wrap"><input name="password" id="fragment-sale-password" type="password" autocomplete="current-password" class="form-control"></div></div></div><div class="form-main-button">`, 1)

	// number assignment popup
	assign := clone(sell)
	setAttr(assign, "id", "fragment-assignment")
	setAttr(assign, "role", "dialog")
	setAttr(assign, "aria-hidden", "true")
	if h := first(assign, tagIs("h2")); h != nil {
		setText(h, "Assign number to account")
	}
	if p := first(assign, classIs("popup-text")); p != nil {
		setText(p, "Choose the primary number shown on your account. Your owned, unlisted numbers remain usable for sign-in with the same password. This session stays signed in; other sessions are revoked.")
	}
	if f := first(assign, classIs("js-sell-username-form")); f != nil {
		replace(f, `<form id="fragment-assignment-form"><div class="form-group"><label for="fragment-assignment-password">Current sign-in password</label><div class="form-control-wrap"><input id="fragment-assignment-password" name="password" type="password" class="form-control" autocomplete="current-password" required></div></div><p class="help-block" id="fragment-assignment-error" role="alert"></p><div class="form-main-button"><button class="btn btn-primary btn-block" type="submit">Confirm</button></div><div class="form-main-button"><button type="button" class="btn btn-default btn-block" data-popup-close="fragment-assignment">Cancel</button></div></form>`)
	}

	fragmentHeader(header, `<span class="tm-header-button" id="fragment-balance">`+html.EscapeString(str(catalog, "balance"))+` GRAM</span>`, slot, selected == nil)

	// listings table; fragment.js fills the rows from a template
	if h := first(main, classIs("tm-main-intro-header")); h != nil {
		setText(h, "Buy and Sell Usernames")
	}
	if p := first(main, classIs("tm-main-intro-text")); p != nil {
		setText(p, "Collectibles for this instance. Buy from other people or administrator listings using your Wallet balance. Purchases do not transfer real Telegram assets or telephone service.")
	}
	if q := first(main, func(n *nethtml.Node) bool { return n.Data == "input" && attr(n, "name") == "query" }); q != nil {
		setAttr(q, "id", "fragment-search")
		setAttr(q, "placeholder", "Search listings…")
	}
	table := first(main, func(n *nethtml.Node) bool { return n.Data == "table" && hasClass(n, "tm-table") })
	if table == nil {
		return pageView{}, fmt.Errorf("the pinned Fragment layout changed")
	}
	table = clone(table)
	for _, th := range all(table, tagIs("th")) {
		t := inner(th)
		switch {
		case strings.Contains(t, "Username"):
			setText(th, "Username")
		case strings.Contains(t, "Minimum bid"):
			setText(th, "Price")
		default:
			setText(th, "Status")
		}
	}
	row := first(table, func(n *nethtml.Node) bool { return n.Data == "tr" && hasClass(n, "tm-row-selectable") })
	if row == nil {
		return pageView{}, fmt.Errorf("the pinned Fragment layout changed")
	}
	row = clone(row)
	for _, n := range all(row, func(n *nethtml.Node) bool {
		return hasClass(n, "table-cell-value") || hasClass(n, "table-cell-desc") || hasClass(n, "table-cell-status-thin")
	}) {
		setInner(n, "")
	}
	for _, a := range all(row, tagIs("a")) {
		setAttr(a, "href", "#")
	}
	if tb := first(table, tagIs("tbody")); tb != nil {
		setAttr(tb, "id", "fragment-items")
		setInner(tb, "")
	}
	if res := first(main, classIs("js-search-results")); res != nil {
		setInner(res, `<div class="tm-section-header"><h2 class="tm-section-header-text" id="fragment-heading">Usernames</h2></div><p id="fragment-status" role="status"></p><div class="tm-table-wrap">`+outer(table)+`</div>`)
	}
	mainHTML := strings.Replace(outer(main), "</main>", `<section class="tm-section"><button class="btn btn-default" type="button" id="fragment-create-listing">List an asset</button></section></main>`, 1)

	if selected != nil {
		if mainHTML, err = s.fragmentDetail(selected, ""); err != nil {
			return pageView{}, err
		}
	}
	title := "Fragment"
	if selected != nil {
		title = str(selected, "title")
	}
	fix := func(s string) string { return fragmentAssets.ReplaceAllString(s, "${1}/__miniapps/fragment/${2}/") }
	return pageView{
		Title:  title,
		Markup: fix(outer(header) + mainHTML),
		Extra:  fix(fragmentConfirm + listingHTML + outer(assign) + `<template id="fragment-row-template">` + outer(row) + `</template>`),
		Config: map[string]any{"slot": slot, "catalog": catalog, "selectedItem": selected},
	}, nil
}

// fragmentHeader trims fragment.com's header to the logo and action, with
// the category tabs on the list page.
func fragmentHeader(header *nethtml.Node, action string, slot int, tabs bool) {
	if t := first(header, classIs("tm-header-tabs")); t != nil {
		setInner(t, `<a href="#username" data-category="username" class="tm-header-tab tab-active">Usernames</a><a href="#number" data-category="number" class="tm-header-tab">Numbers</a><a href="#owned" data-category="owned" class="tm-header-tab">My assets</a>`)
	}
	for _, n := range all(header, func(n *nethtml.Node) bool {
		return hasClass(n, "tm-header-actions") || hasClass(n, "tm-header-menu") || hasClass(n, "tm-header-menu-button")
	}) {
		if hasClass(n, "tm-header-actions-wide") {
			setInner(n, action)
		} else {
			detach(n)
		}
	}
	home := "/fragment"
	if slot > 1 {
		home += "?account=" + strconv.Itoa(slot)
	}
	for _, a := range all(header, func(n *nethtml.Node) bool { return attr(n, "href") == "/" }) {
		setAttr(a, "href", home)
	}
	if !tabs {
		removeAll(header, classIs("tm-header-tabs-wrap"))
		setAttr(header, "class", "tm-header")
	}
}

// fragmentDetail is the page of one username or number, from
// fragment.com's auction page. signIn, for a signed-out visitor, takes
// the place of the buttons.
func (s *Server) fragmentDetail(it map[string]any, signIn string) (string, error) {
	kind := str(it, "kind")
	if kind != "username" && kind != "number" {
		return "", fmt.Errorf("fragment item kind %q", kind)
	}
	doc, err := s.fragmentFile(kind + ".html")
	if err != nil {
		return "", err
	}
	main := first(doc, tagIs("main"))
	if main == nil {
		return "", fmt.Errorf("the pinned Fragment layout changed")
	}
	removeAll(main, func(n *nethtml.Node) bool { return n.Data == "section" && !hasClass(n, "tm-auction-section") })
	display := str(it, "displayTitle")
	if display == "" {
		display = str(it, "title")
	}
	listed, owned, assigned := truthy(it, "listed"), truthy(it, "isOwned"), truthy(it, "isAssigned")
	for _, n := range all(main, classIs("tm-section-header-domain")) {
		setText(n, display)
	}
	status := "Not for sale"
	switch {
	case listed:
		status = "For sale"
	case assigned:
		status = "Primary number"
	case owned:
		status = "Owned"
	case str(it, "purchasedAt") != "" && str(it, "purchasedAt") != "0":
		status = "Sold"
	}
	for _, n := range all(main, classIs("tm-section-header-status")) {
		setText(n, status)
	}
	removeAll(main, func(n *nethtml.Node) bool {
		return hasClass(n, "tm-section-subscribe") || hasClass(n, "tm-section-countdown-wrap") || hasClass(n, "tm-bid-info-text")
	})
	usd := ""
	if v := str(it, "usdAmount"); v != "" {
		usd = "~ $" + html.EscapeString(v)
	}
	for _, n := range all(main, classIs("tm-section-bid-info")) {
		setInner(n, `<table class="table tm-table tm-table-fixed"><thead><tr><th>Sale price</th></tr></thead><tbody><tr><td><div class="table-cell"><div class="table-cell-value tm-value icon-before icon-ton">`+html.EscapeString(str(it, "amount"))+`</div><div class="table-cell-desc">`+usd+`</div></div></td></tr></tbody></table>`)
	}
	label := "Username"
	if kind == "number" {
		label = "Collectible Number"
	}
	for _, n := range all(main, classIs("tm-section-auction-info")) {
		rows := `<dl class="tm-list-item"><dt class="tm-list-item-title">` + label + `</dt><dd class="tm-list-item-value"><span class="accent-color">` + html.EscapeString(str(it, "title")) + `</span></dd></dl>`
		if v := str(it, "ownerName"); v != "" {
			rows += `<dl class="tm-list-item"><dt class="tm-list-item-title">Owner</dt><dd class="tm-list-item-value">` + html.EscapeString(v) + `</dd></dl>`
		}
		if at, _ := it["purchasedAt"].(int); at > 0 {
			rows += `<dl class="tm-list-item"><dt class="tm-list-item-title">Purchased</dt><dd class="tm-list-item-value">` + time.Unix(int64(at), 0).UTC().Format("Jan 2, 2006") + `</dd></dl>`
		}
		setInner(n, rows)
	}
	action, disabled := "Buy now", ""
	if owned {
		action = "Sell"
		if listed {
			action = "Unlist"
		}
	} else if !listed {
		disabled = ` disabled`
	}
	extra := ""
	if kind == "number" && owned && !listed && !assigned {
		extra = `<button type="button" class="btn btn-default" id="fragment-assign-number">Use as primary number</button>`
	}
	buttons := `<button class="btn btn-primary" type="button" id="fragment-detail-action"` + disabled + `>` + action + `</button>` + extra
	if signIn != "" {
		buttons = `<a class="btn btn-primary" href="` + html.EscapeString(signIn) + `">Sign in</a>`
	}
	for _, n := range all(main, classIs("tm-section-buttons")) {
		setInner(n, buttons)
	}
	return strings.Replace(outer(main), "</main>", `<section class="tm-section"><p class="help-block" id="fragment-status" role="status"></p></section></main>`, 1), nil
}

// fragmentDirect is a Fragment page opened in a browser tab, not launched
// from the app (Web A's "Learn more" on a collectible). Who is looking is
// the site's session cookie for ?account=N, as /api/fragment takes it when
// there is no launch data; without one, a username or number shows its
// public page read-only.
func (s *Server) fragmentDirect(w http.ResponseWriter, r *http.Request) {
	slot := 1
	if v := r.URL.Query().Get("account"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 6 {
			fail(w, 400, "Choose an account slot between 1 and 6.")
			return
		}
		slot = n
	}
	cookie := "session"
	if slot > 1 {
		cookie += strconv.Itoa(slot)
	}
	var u account.User
	var err error
	var refusal *account.Error
	signedIn := false
	if c, cerr := r.Cookie(cookie); cerr == nil {
		u, _, err = s.Accounts.SessionUser(r.Context(), c.Value)
		signedIn = err == nil && !u.IsBot
		if errors.As(err, &refusal) {
			err = nil
		}
	}
	var v pageView
	switch {
	case err != nil:
	case signedIn:
		v, err = s.fragmentPage(r, u, slot)
	default:
		v, err = s.fragmentPublic(r, slot)
	}
	switch {
	case errors.As(err, &refusal):
		fail(w, refusal.Status, refusal.Message)
	case err != nil:
		slog.Error("miniapps: page "+r.URL.Path, "err", err)
		fail(w, 500, "Something went wrong. Try again.")
	default:
		reply(w, 200, v)
	}
}

// fragmentPublic is the signed-out page: one collectible read-only, or
// for the list a note to sign in. Nothing on it can buy or sell.
func (s *Server) fragmentPublic(r *http.Request, slot int) (pageView, error) {
	next := url.Values{"next": {r.URL.RequestURI()}}
	if slot > 1 {
		next.Set("account", strconv.Itoa(slot))
	}
	signIn := "/login?" + next.Encode()
	index, err := s.fragmentFile("index.html")
	if err != nil {
		return pageView{}, err
	}
	header := first(index, func(n *nethtml.Node) bool { return n.Data == "header" && hasClass(n, "tm-header") })
	if header == nil {
		return pageView{}, fmt.Errorf("the pinned Fragment layout changed")
	}
	var item map[string]any
	if m := fragmentRoute.FindStringSubmatch(r.URL.Path); m != nil {
		if item, err = s.Fragment.Public(r.Context(), m[1], m[2]); err != nil {
			return pageView{}, err
		}
	}
	fragmentHeader(header, `<a class="tm-header-button" href="`+html.EscapeString(signIn)+`">Sign in</a>`, slot, false)
	title := "Fragment"
	mainHTML := `<main class="tm-main"><section class="tm-section"><div class="tm-section-header"><h2 class="tm-section-header-text">Buy and Sell Usernames</h2></div><p>Sign in to see the listings, buy with your Wallet balance and sell your own collectibles.</p><a class="btn btn-primary" href="` + html.EscapeString(signIn) + `">Sign in</a></section></main>`
	if item != nil {
		title = str(item, "title")
		if mainHTML, err = s.fragmentDetail(item, signIn); err != nil {
			return pageView{}, err
		}
	}
	fix := func(s string) string { return fragmentAssets.ReplaceAllString(s, "${1}/__miniapps/fragment/${2}/") }
	return pageView{
		Title:  title,
		Markup: fix(outer(header) + mainHTML),
		Config: map[string]any{"slot": slot, "public": true, "selectedItem": item},
	}, nil
}
