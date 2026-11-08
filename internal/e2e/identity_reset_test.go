package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
)

// An admin resets bob's phone and nothing else. The real number goes from
// everywhere it was (account, kept aliases, a sign-in in progress), the
// audit row keeps it masked, bob's app and carol (who imported him by that
// number) are told, and bob still signs in with the new number, his
// username and the same password. Then a second reset frees the username.
// Last, sign-up with made-up numbers on.
func TestIdentityReset(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.IdentityReset = s.api.IdentityReset
	adm.Register(s.mux)

	const real, kept, pw = "+380501234512", "+380509876543", "correct horse bob"
	ada, _ := phoneSignUp(t, s, "+15550100", "ada", "correct horse ada")
	b, bobID := phoneSignUp(t, s, real, "bob", pw)
	bob := person{id: bobID, storage: &session.StorageMemory{}}
	token := b.session()
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: bob.id, Bytes: []byte(token)})
		return err
	})
	carol := s.signUp(t, "carol")
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ContactsImportContacts(ctx, []tg.InputPhoneContact{{ClientID: 1, Phone: real, FirstName: "Bob", LastName: "Real"}})
		if err == nil && len(res.Imported) != 1 {
			err = fmt.Errorf("import found %d", len(res.Imported))
		}
		return err
	})

	str := func(q string, args ...any) string {
		t.Helper()
		var v string
		if err := s.db.Pool.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	// a number kept after a swap, and someone half way through signing in
	// with the real one
	enc, _ := s.secret.EncryptPhone(kept, fmt.Sprintf("phone-alias:%d:%s", bob.id, s.secret.PhoneLookup(kept)))
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO phone_aliases(lookup, user_id, phone) VALUES ($1, $2, $3)", s.secret.PhoneLookup(kept), bob.id, enc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, "UPDATE users SET bio='about bob' WHERE id=$1", bob.id); err != nil {
		t.Fatal(err)
	}
	half := s.browser(t)
	half.must("/api/auth/start", map[string]string{"mode": "phone"})
	half.must("/api/auth/code", map[string]string{"phone": real})
	profile := "SELECT name || '/' || username || '/' || username_public || '/' || bio || '/' || coalesce(photo_id, 0) FROM users WHERE id=$1"
	before := str(profile, bob.id)

	mine, theirs := make(chan tg.UpdatesClass, 64), make(chan tg.UpdatesClass, 64)
	s.live(t, bob, mine)
	s.live(t, carol, theirs)

	path := fmt.Sprintf("/api/admin/users/%d/reset", bob.id)
	if code, _ := ada.post(path, map[string]any{"fields": []string{"phone"}, "confirmation": "bobby"}); code != 400 {
		t.Fatalf("wrong confirmation: %d", code)
	}
	if code, _ := ada.post(path, map[string]any{"fields": []string{}, "confirmation": "bob"}); code != 400 {
		t.Fatalf("nothing picked: %d", code)
	}
	out := ada.must(path, map[string]any{"fields": []string{"phone"}, "confirmation": "bob"})
	reset := out["reset"].(map[string]any)
	next, _ := reset["newPhone"].(string)
	if fmt.Sprint(reset["fields"]) != "[phone]" || !strings.HasPrefix(next, "+888") || len(next) != 12 || reset["oldPhone"] != "+38********12" {
		t.Fatalf("reset %v", reset)
	}

	// the phone and only the phone
	if after := str(profile, bob.id); after != before {
		t.Fatalf("profile changed: %q -> %q", before, after)
	}
	u, _, _ := s.accounts.User(ctx, bob.id)
	if u.Phone != next {
		t.Fatalf("phone %q", u.Phone)
	}
	for _, q := range []string{
		"SELECT count(*)::text FROM phone_aliases",
		"SELECT count(*)::text FROM fragment_assignments",
		"SELECT count(*)::text FROM auth_flows",
		"SELECT count(*)::text FROM audit_log WHERE details::text LIKE '%380501234512%' OR details::text LIKE '%380509876543%'",
	} {
		if n := str(q); n != "0" {
			t.Fatalf("%s: %s", q, n)
		}
	}
	if d := str("SELECT details::text FROM audit_log WHERE action='admin_identity_reset' AND target_id=$1", bob.id); !strings.Contains(d, `"+38********12"`) {
		t.Fatalf("audit %s", d)
	}
	for _, old := range []string{real, kept} {
		if _, found, _ := s.accounts.UserByPhone(ctx, old); found {
			t.Fatalf("%s still finds bob", old)
		}
		if _, code := phoneSignIn(t, s, old, pw); code != 401 {
			t.Fatalf("sign-in with %s: %d", old, code)
		}
	}
	if id, code := phoneSignIn(t, s, next, pw); id != bob.id {
		t.Fatalf("sign-in with the new number: %d %d", id, code)
	}
	if code, _ := s.browser(t).post("/api/login", map[string]string{"username": "bob", "password": pw}); code != 200 {
		t.Fatalf("username sign-in: %d", code)
	}
	digits := strings.TrimPrefix(next, "+")
	waitPush(t, mine, "bob's new phone", func(x tg.UpdateClass) bool {
		p, ok := x.(*tg.UpdateUserPhone)
		return ok && p.UserID == bob.id && p.Phone == digits
	})
	waitPush(t, theirs, "carol told about bob", func(x tg.UpdateClass) bool {
		switch v := x.(type) {
		case *tg.UpdateUserPhone:
			return v.UserID == bob.id && v.Phone != strings.TrimPrefix(real, "+")
		case *tg.UpdateUser:
			return v.UserID == bob.id
		}
		return false
	})

	// the username goes free and a hidden placeholder signs in
	out = ada.must(path, map[string]any{"fields": []string{"username", "name", "bio"}, "confirmation": "bob"})
	holder := out["reset"].(map[string]any)["username"].(string)
	want := fmt.Sprintf("User %d/%s/false//0", bob.id, holder)
	if after := str(profile, bob.id); after != want || u.Phone != next {
		t.Fatalf("second reset: %q, want %q", after, want)
	}
	if code, _ := s.browser(t).post("/api/login", map[string]string{"username": holder, "password": pw}); code != 200 {
		t.Fatalf("placeholder sign-in: %d", code)
	}
	if n := str("SELECT count(*)::text FROM users WHERE lower(username)='bob'"); n != "0" {
		t.Fatal("bob is still taken")
	}

	// sign-up hands out numbers: what was typed is ignored
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO instance_settings(key, value) VALUES ('signupGeneratedNumbers', 'true')`); err != nil {
		t.Fatal(err)
	}
	s.settings.Forget()
	d := s.browser(t)
	d.must("/api/auth/start", map[string]string{"mode": "phone"})
	d.must("/api/auth/intent", map[string]string{"intent": "register"})
	code := d.must("/api/auth/code", map[string]string{"phone": "+15550123"})
	made, _ := code["phone"].(string)
	reg := d.must("/api/auth/register", map[string]string{"firstName": "Dee", "username": "dee", "password": "correct horse dee"})
	if !strings.HasPrefix(made, "+888") || reg["user"].(map[string]any)["phoneNumber"] != made {
		t.Fatalf("generated sign-up: %v %v", code, reg)
	}
}
