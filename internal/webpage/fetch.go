// Package webpage is link previews: the first link of a message, fetched
// server-side for its OpenGraph tags and kept for a while. Fetches only
// reach public addresses, so a message can't make the server talk to
// itself or the network it sits in.
package webpage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
)

const (
	fetchTimeout = 5 * time.Second
	maxPageBytes = 512 << 10
	maxImgBytes  = 5 << 20
	maxRedirects = 3
	userAgent    = "Mozilla/5.0 (compatible; GreengramBot/1.0; link preview)"
)

var errBlocked = errors.New("webpage: address not allowed")

// blocked are the ranges besides the ones netip names (loopback,
// private, link-local, multicast, unspecified) that aren't the internet.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach v4 private space
	netip.MustParsePrefix("2001:db8::/32"),
}

// Public is whether ip is somewhere a fetch may go.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Fetcher gets pages and images over a client that checks every address
// it connects to, after DNS, so a name that resolves somewhere private
// (or changes its mind between lookups) gets nowhere.
type Fetcher struct {
	client *http.Client
}

func NewFetcher() *Fetcher { return NewFetcherFor(Public) }

// NewFetcherFor checks addresses with allow instead; tests use it to
// reach their own loopback server.
func NewFetcherFor(allow func(netip.Addr) bool) *Fetcher {
	d := &net.Dialer{Timeout: fetchTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !allow(ap.Addr()) {
			return errBlocked
		}
		return nil
	}}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   fetchTimeout,
		ResponseHeaderTimeout: fetchTimeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Fetcher{client: &http.Client{
		Transport: tr,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("webpage: too many redirects")
			}
			return checkURL(req.URL)
		},
	}}
}

func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errBlocked
	}
	return nil
}

func (f *Fetcher) get(ctx context.Context, raw string, limit int64, accept func(mediaType string) bool) ([]byte, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	if err := checkURL(u); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,image/*;q=0.8,*/*;q=0.5")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("webpage: status %d", resp.StatusCode)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !accept(mt) {
		return nil, nil, fmt.Errorf("webpage: content type %q", mt)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > limit {
		// a page's head is enough; an image cut short is no image
		if !strings.HasPrefix(mt, "text/") && mt != "application/xhtml+xml" {
			return nil, nil, errors.New("webpage: too big")
		}
		body = body[:limit]
	}
	return body, resp.Request.URL, nil
}

// Page is what a preview shows.
type Page struct {
	URL         string // where the fetch ended up
	Type        string
	SiteName    string
	Title       string
	Description string
	Image       string
}

// Fetch reads a page's OpenGraph tags (falling back to <title> and the
// description meta).
func (f *Fetcher) Fetch(ctx context.Context, raw string) (Page, error) {
	body, final, err := f.get(ctx, raw, maxPageBytes, func(mt string) bool { return mt == "text/html" || mt == "application/xhtml+xml" })
	if err != nil {
		return Page{}, err
	}
	p := parse(body)
	p.URL = final.String()
	if p.Image != "" {
		if iu, err := final.Parse(p.Image); err == nil && checkURL(iu) == nil {
			p.Image = iu.String()
		} else {
			p.Image = ""
		}
	}
	if p.SiteName == "" {
		p.SiteName = strings.TrimPrefix(final.Hostname(), "www.")
	}
	return p, nil
}

// Image downloads a preview's picture.
func (f *Fetcher) Image(ctx context.Context, raw string) ([]byte, error) {
	body, _, err := f.get(ctx, raw, maxImgBytes, func(mt string) bool { return strings.HasPrefix(mt, "image/") })
	return body, err
}

const maxField = 1024

func clean(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if r := []rune(s); len(r) > maxField {
		s = string(r[:maxField])
	}
	return s
}

func parse(body []byte) Page {
	var p Page
	var title string
	meta := map[string]string{}
	z := html.NewTokenizer(strings.NewReader(string(body)))
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			goto done
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "body":
				goto done
			case "title":
				inTitle = tt == html.StartTagToken
			case "meta":
				var key, content string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch strings.ToLower(string(k)) {
					case "property", "name":
						key = strings.ToLower(string(v))
					case "content":
						content = string(v)
					}
				}
				if key != "" && meta[key] == "" {
					meta[key] = content
				}
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "head" {
				goto done
			} else if string(name) == "title" {
				inTitle = false
			}
		case html.TextToken:
			if inTitle && title == "" {
				title = string(z.Text())
			}
		}
	}
done:
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := clean(meta[k]); v != "" {
				return v
			}
		}
		return ""
	}
	p.Title = pick("og:title", "twitter:title")
	if p.Title == "" {
		p.Title = clean(title)
	}
	p.Description = pick("og:description", "twitter:description", "description")
	p.SiteName = pick("og:site_name")
	p.Image = pick("og:image", "og:image:url", "twitter:image")
	p.Type = "article"
	if t := pick("og:type"); strings.HasPrefix(t, "video") {
		p.Type = "video"
	} else if p.Image != "" && p.Title == "" && p.Description == "" {
		p.Type = "photo"
	}
	return p
}
