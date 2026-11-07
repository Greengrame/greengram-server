package bots

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// InitData is the signed launch data a mini app gets in #tgWebAppData,
// checked the way core.telegram.org/bots/webapps describes: the fields
// sorted as key=value lines, HMAC-SHA256 under the bot's WebAppKey.
func InitData(key []byte, fields url.Values, now time.Time) string {
	v := url.Values{}
	for k, list := range fields {
		v[k] = list
	}
	v.Set("auth_date", strconv.FormatInt(now.Unix(), 10))
	v.Del("hash")
	v.Set("hash", hex.EncodeToString(sign(key, v)))
	return v.Encode()
}

func sign(key []byte, v url.Values) []byte {
	keys := make([]string, 0, len(v))
	for k := range v {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + v.Get(k)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join(lines, "\n")))
	return m.Sum(nil)
}

var ErrInitData = errors.New("invalid init data")

// CheckInitData verifies raw against key and returns its fields. Data
// older than maxAge is refused.
func CheckInitData(key []byte, raw string, maxAge time.Duration, now time.Time) (url.Values, error) {
	v, err := url.ParseQuery(raw)
	if err != nil || len(key) == 0 {
		return nil, ErrInitData
	}
	got, err := hex.DecodeString(v.Get("hash"))
	if err != nil || !hmac.Equal(got, sign(key, v)) {
		return nil, ErrInitData
	}
	at, err := strconv.ParseInt(v.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(at, 0)) > maxAge {
		return nil, ErrInitData
	}
	return v, nil
}

// SessionTag ties a launch to the session that opened it without giving
// the page the session hash; it goes in initData as gg_session.
func SessionTag(key []byte, sessionHash string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("session\x00" + sessionHash))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// LaunchAge is how long a mini app launch stays good for API calls.
const LaunchAge = 24 * time.Hour

// Launch is a checked initData: who opened the app, and which of their
// sessions did.
type Launch struct {
	User   int64 // server id
	Fields url.Values
	key    []byte
}

// Session picks the launching session out of the user's live ones; ""
// if it has ended or the launch didn't say.
func (l Launch) Session(hashes map[string]bool) string {
	tag := l.Fields.Get("gg_session")
	if tag == "" {
		return ""
	}
	for h := range hashes {
		if hmac.Equal([]byte(SessionTag(l.key, h)), []byte(tag)) {
			return h
		}
	}
	return ""
}

// Launched checks initData signed by the built-in bot of this kind.
func (s *Service) Launched(ctx context.Context, kind, raw string) (Launch, error) {
	if raw == "" {
		return Launch{}, ErrInitData
	}
	b, ok, err := s.ByKind(ctx, kind)
	if err != nil {
		return Launch{}, err
	}
	if !ok {
		return Launch{}, ErrInitData
	}
	fields, err := CheckInitData(b.WebAppKey, raw, LaunchAge, time.Now())
	if err != nil {
		return Launch{}, err
	}
	var who struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal([]byte(fields.Get("user")), &who) != nil || who.ID <= 0 {
		return Launch{}, ErrInitData
	}
	return Launch{User: ids.Server(who.ID), Fields: fields, key: b.WebAppKey}, nil
}
