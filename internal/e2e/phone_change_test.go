package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// Bob changes his number the way the apps do it: a code from the service
// chat, a wrong guess, then the right one. The old number stops signing in
// and is free for someone new, the new one signs in with the same password,
// and both his apps and carol, who has him as a contact, hear about it.
func TestPhoneChange(t *testing.T) {
	s := start(t)
	const old, next, taken, pw = "+380501234512", "+380671112233", "+15550100", "correct horse bob"
	phoneSignUp(t, s, taken, "ada", "correct horse ada")
	b, bobID := phoneSignUp(t, s, old, "bob", pw)
	bob := person{id: bobID, storage: &session.StorageMemory{}}
	token := b.session()
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: bob.id, Bytes: []byte(token)})
		return err
	})
	carol := s.signUp(t, "carol")
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ContactsImportContacts(ctx, []tg.InputPhoneContact{{ClientID: 1, Phone: old, FirstName: "Bob"}})
		return err
	})
	mine, theirs := make(chan tg.UpdatesClass, 64), make(chan tg.UpdatesClass, 64)
	s.live(t, bob, mine)
	s.live(t, carol, theirs)

	codeRe := regexp.MustCompile(`: (\d{5})\b`)
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		send := func(phone string) (*tg.AuthSentCode, error) {
			res, err := c.AccountSendChangePhoneCode(ctx, &tg.AccountSendChangePhoneCodeRequest{PhoneNumber: phone})
			if err != nil {
				return nil, err
			}
			sent, ok := res.(*tg.AuthSentCode)
			if !ok {
				return nil, fmt.Errorf("sent code %T", res)
			}
			return sent, nil
		}
		for phone, want := range map[string]string{taken: "PHONE_NUMBER_OCCUPIED", old: "PHONE_NUMBER_OCCUPIED", "+12": "PHONE_NUMBER_INVALID"} {
			if _, err := send(phone); !tgerr.Is(err, want) {
				return fmt.Errorf("%s: %v, want %s", phone, err, want)
			}
		}
		sent, err := send(next)
		if err != nil {
			return err
		}
		if app, ok := sent.Type.(*tg.AuthSentCodeTypeApp); !ok || app.Length != 5 || sent.PhoneCodeHash == "" {
			return fmt.Errorf("sent %+v", sent)
		}
		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: 777000}, Limit: 5})
		if err != nil {
			return err
		}
		var code string
		if list, ok := h.(interface{ GetMessages() []tg.MessageClass }); ok && len(list.GetMessages()) > 0 {
			if m, ok := list.GetMessages()[0].(*tg.Message); ok && strings.Contains(m.Message, next) {
				if got := codeRe.FindStringSubmatch(m.Message); got != nil {
					code = got[1]
				}
			}
		}
		if code == "" {
			return fmt.Errorf("no code in the service chat: %T", h)
		}
		wrong := "00000"
		if code == wrong {
			wrong = "11111"
		}
		change := func(phone, hash, code string) (tg.UserClass, error) {
			return c.AccountChangePhone(ctx, &tg.AccountChangePhoneRequest{PhoneNumber: phone, PhoneCodeHash: hash, PhoneCode: code})
		}
		if _, err := change(next, sent.PhoneCodeHash, wrong); !tgerr.Is(err, "PHONE_CODE_INVALID") {
			return fmt.Errorf("wrong code: %v", err)
		}
		if _, err := change("+380671112234", sent.PhoneCodeHash, code); !tgerr.Is(err, "PHONE_CODE_EXPIRED") {
			return fmt.Errorf("another number: %v", err)
		}
		res, err := change(next, sent.PhoneCodeHash, code)
		if err != nil {
			return err
		}
		if u, ok := res.(*tg.User); !ok || u.Phone != strings.TrimPrefix(next, "+") || !u.Self {
			return fmt.Errorf("changed user %+v", res)
		}
		if _, err := change(next, sent.PhoneCodeHash, code); !tgerr.Is(err, "PHONE_CODE_EXPIRED") {
			return fmt.Errorf("code used twice: %v", err)
		}
		return nil
	})

	digits := strings.TrimPrefix(next, "+")
	waitPush(t, mine, "bob's new phone", func(x tg.UpdateClass) bool {
		p, ok := x.(*tg.UpdateUserPhone)
		return ok && p.UserID == bob.id && p.Phone == digits
	})
	waitPush(t, theirs, "carol told about bob", func(x tg.UpdateClass) bool {
		switch v := x.(type) {
		case *tg.UpdateUserPhone:
			return v.UserID == bob.id && v.Phone == digits
		case *tg.UpdateUser:
			return v.UserID == bob.id
		}
		return false
	})
	if _, code := phoneSignIn(t, s, old, pw); code != 401 {
		t.Fatalf("sign-in with the old number: %d", code)
	}
	if id, code := phoneSignIn(t, s, next, pw); id != bob.id {
		t.Fatalf("sign-in with the new number: %d %d", id, code)
	}
	// his session lived through it, and the old number is anyone's
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		return err
	})
	if _, id := phoneSignUp(t, s, old, "dan", "correct horse dan"); id == 0 || id == bob.id {
		t.Fatalf("the old number went to %d", id)
	}
	// the website's way: the password instead of a code
	const third = "+380931112233"
	web := s.browser(t)
	web.must("/api/auth/start", map[string]string{"mode": "phone"})
	web.must("/api/auth/code", map[string]string{"phone": next})
	web.must("/api/auth/password", map[string]string{"password": pw})
	for _, c := range []struct {
		phone, pw string
		code      int
	}{{third, "not the password", 400}, {old, pw, 400}, {"+12", pw, 400}} {
		if code, out := web.post("/api/account/phone", map[string]string{"phone": c.phone, "password": c.pw}); code != c.code {
			t.Fatalf("change to %s: %d %v", c.phone, code, out)
		}
	}
	if out := web.must("/api/account/phone", map[string]string{"phone": third, "password": pw}); out["phoneNumber"] != third {
		t.Fatalf("changed: %v", out)
	}
	if code, _ := s.browser(t).post("/api/account/phone", map[string]string{"phone": "+380931112244", "password": pw}); code != 401 {
		t.Fatalf("no session: %d", code)
	}
	if _, code := phoneSignIn(t, s, next, pw); code != 401 {
		t.Fatalf("sign-in with the second number: %d", code)
	}
	if id, code := phoneSignIn(t, s, third, pw); id != bob.id {
		t.Fatalf("sign-in with the third number: %d %d", id, code)
	}
	var left int
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM phone_changes").Scan(&left); err != nil || left != 0 {
		t.Fatalf("changes left: %d %v", left, err)
	}
}
