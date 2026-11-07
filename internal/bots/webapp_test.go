package bots

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInitDataRoundTrip(t *testing.T) {
	key := WebAppKey("123:secret")
	now := time.Unix(1_800_000_000, 0)
	raw := InitData(key, url.Values{"user": {`{"id":5,"first_name":"Ada"}`}, "start_param": {"x"}}, now)

	v, err := CheckInitData(key, raw, time.Hour, now.Add(time.Minute))
	if err != nil || v.Get("start_param") != "x" || v.Get("auth_date") != "1800000000" {
		t.Fatalf("check: %v %v", v, err)
	}
	if _, err := CheckInitData(WebAppKey("123:other"), raw, time.Hour, now); err == nil {
		t.Fatal("another bot's key passed")
	}
	if _, err := CheckInitData(key, strings.Replace(raw, "Ada", "Eve", 1), time.Hour, now); err == nil {
		t.Fatal("edited data passed")
	}
	if _, err := CheckInitData(key, raw, time.Hour, now.Add(2*time.Hour)); err == nil {
		t.Fatal("old data passed")
	}
}

// The check string and keys follow core.telegram.org/bots/webapps, done
// by hand here so a change to sign() can't agree with itself.
func TestInitDataMatchesTelegram(t *testing.T) {
	token := "42:telegram"
	raw := InitData(WebAppKey(token), url.Values{"query_id": {"AAA"}, "user": {`{"id":1}`}}, time.Unix(100, 0))
	v, _ := url.ParseQuery(raw)

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	m := hmac.New(sha256.New, secret.Sum(nil))
	m.Write([]byte("auth_date=100\nquery_id=AAA\nuser={\"id\":1}"))
	if want := hex.EncodeToString(m.Sum(nil)); v.Get("hash") != want {
		t.Fatalf("hash %s, want %s", v.Get("hash"), want)
	}
}
