package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// An account with a bit of everything deletes itself: Stars, wallet coins
// in all three places, a collectible on sale, a Fragment name on sale, a
// group with an admin to take it over and a channel with nobody, a live app.
// Afterwards nothing is left on it, every ledger adds up to what it did,
// and its messages are still there under Deleted Account. Then the
// inactivity job takes someone who stayed away past their TTL.
func TestAccountDeletion(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada") // the first account, an admin
	carol := s.signUp(t, "carol")
	dave := s.signUp(t, "dave")
	// bob by hand: the password check needs his browser
	b := s.browser(t)
	u := b.must("/api/register", map[string]string{"username": "bob", "name": "Bob Test", "password": "correct horse bob"})
	bob := person{id: int64(u["id"].(float64)), storage: &session.StorageMemory{}}
	token := b.session()
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: bob.id, Bytes: []byte(token)})
		return err
	})

	k := newNFTKit(t, s, 10)
	k.fund(ada, 1000, "deletion-1")
	k.fund(bob, 1000, "deletion-2")
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		_, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: k.gift.ID})
		return err
	})
	var group, orphan *tg.Channel
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		if _, _, err := payResult(ctx, c, &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)}); err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
		if group, err = newGroup(ctx, c, t, "Kept", carol.id, dave.id); err != nil {
			return err
		}
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: carol.id},
			AdminRights: tg.ChatAdminRights{InviteUsers: true}}); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "bob was here", RandomID: 7}); err != nil {
			return err
		}
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Title: "Orphan", Broadcast: true})
		if err != nil {
			return err
		}
		orphan = channelOf(t, res)
		return nil
	})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Pool.Exec(context.Background(), q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	unique := k.int("SELECT unique_id FROM gift_receipts WHERE owner_id=$1 AND unique_id IS NOT NULL", bob.id)
	exec("UPDATE unique_gifts SET resale_stars=500, resale_since=1 WHERE id=$1", unique)
	exec("INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 5000000000), ($1, 'USDT', 7000000), ($2, 'TON', 1)", bob.id, carol.id)
	exec("INSERT INTO defi_accounts(user_id, created_at) VALUES ($1, 1)", bob.id)
	exec("INSERT INTO defi_balances(user_id, asset, units) VALUES ($1, 'TON', 1000)", bob.id)
	exec("INSERT INTO earn_accounts(user_id, campaign_id, units) VALUES ($1, 2, 3000)", bob.id)
	exec(`INSERT INTO fragment_items(id, kind, title, units, owner_id, listed, purchased_at, last_sale_units)
		VALUES ('username:bobcoin', 'username', '@bobcoin', 2000000000, $1, true, 100, 1000000000)`, bob.id)

	got := make(chan tg.UpdatesClass, 256)
	live := s.live(t, bob, got)

	starsBefore := k.total()
	coins := func(asset string) int64 {
		return k.int(`SELECT (coalesce((SELECT sum(units) FROM crypto_accounts WHERE asset=$1), 0) + coalesce((SELECT sum(units) FROM defi_balances WHERE asset=$1), 0)
			+ coalesce((SELECT sum(units) FROM earn_accounts WHERE campaign_id = CASE $1::text WHEN 'TON' THEN 2 WHEN 'USDT' THEN 1 END), 0))::bigint`, asset)
	}
	tonBefore, usdtBefore := coins("TON"), coins("USDT")
	bobStars := k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=$1", bob.id)
	if bobStars == 0 || tonBefore != 5000004001 {
		t.Fatalf("setup: stars %d, ton %d", bobStars, tonBefore)
	}

	// the password goes over HTTPS first, then account.deleteAccount
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: "bye"}); !tgerr.Is(err, "PASSWORD_REQUIRED") {
			return fmt.Errorf("unconfirmed delete: %v", err)
		}
		return nil
	})
	if code, _ := b.post("/api/account/deletion", map[string]string{"password": "wrong password!!"}); code != 400 {
		t.Fatalf("wrong password: %d", code)
	}
	b.must("/api/account/deletion", map[string]string{"password": "correct horse bob"})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		ok, err := c.AccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: "bye"})
		if err != nil || !ok {
			return fmt.Errorf("delete: %v %v", ok, err)
		}
		return nil
	})

	// the tombstone, and nothing left to sign in with
	if st := k.str("SELECT account_state || '/' || name || '/' || deletion_source || '/' || password || '/' || coalesce(phone, '') FROM users WHERE id=$1", bob.id); st != "deleted/Deleted Account/self//" {
		t.Fatalf("tombstone %q", st)
	}
	if n := k.int("SELECT (SELECT count(*) FROM sessions WHERE user_id=$1) + (SELECT count(*) FROM user_photos WHERE user_id=$1)", bob.id); n != 0 {
		t.Fatalf("%d sessions and photos left", n)
	}
	if code, _ := b.post("/api/auth/webtoken", map[string]string{}); code != 401 {
		t.Fatalf("web session after delete: %d", code)
	}
	// the live app is out: its key is gone, the next call can't go through
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, err := live.UpdatesGetState(ctx)
	cancel()
	if err == nil {
		t.Fatal("the deleted account's live app still works")
	}

	// chats: the group went to its admin, the channel has no one to own it
	if who := k.int("SELECT creator_id FROM channels WHERE id=$1", group.ID); who != carol.id {
		t.Fatalf("group owner %d", who)
	}
	if st := k.str("SELECT (SELECT status FROM channel_members WHERE channel_id=$1 AND user_id=$2) || '/' || (SELECT status FROM channel_members WHERE channel_id=$1 AND user_id=$3)",
		group.ID, bob.id, carol.id); st != "left/creator" {
		t.Fatalf("group members bob/carol %s", st)
	}
	if who := k.int("SELECT creator_id FROM channels WHERE id=$1 AND NOT deleted", orphan.ID); who != bob.id {
		t.Fatalf("ownerless channel creator %d", who)
	}

	// money: nothing on bob, all of it on Deleted Accounts, totals unchanged
	sys := k.int("SELECT user_id FROM system_accounts WHERE kind='erased_accounts'")
	if got := k.total(); got != starsBefore {
		t.Fatalf("Stars ledger total %d, was %d", got, starsBefore)
	}
	if got := k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=$1", bob.id); got != 0 {
		t.Fatalf("bob still has %d Stars", got)
	}
	if got := k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=$1", sys); got != bobStars {
		t.Fatalf("Deleted Accounts has %d Stars, want %d", got, bobStars)
	}
	if ton, usdt := coins("TON"), coins("USDT"); ton != tonBefore || usdt != usdtBefore {
		t.Fatalf("wallet totals TON %d (was %d), USDT %d (was %d)", ton, tonBefore, usdt, usdtBefore)
	}
	if left := k.int(`SELECT (coalesce((SELECT sum(units) FROM crypto_accounts WHERE user_id=$1), 0) + coalesce((SELECT sum(units) FROM defi_balances WHERE user_id=$1), 0)
		+ coalesce((SELECT sum(units) FROM earn_accounts WHERE user_id=$1), 0))::bigint`, bob.id); left != 0 {
		t.Fatalf("bob still holds %d coin units", left)
	}
	if ton := k.int("SELECT units::bigint FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", sys); ton != 5000004000 {
		t.Fatalf("Deleted Accounts TON %d", ton)
	}
	if st := k.str("SELECT r.owner_id || '/' || (u.resale_stars IS NULL) FROM gift_receipts r JOIN unique_gifts u ON u.id = r.unique_id WHERE u.id=$1", unique); st != fmt.Sprintf("%d/true", sys) {
		t.Fatalf("collectible %s", st)
	}
	if st := k.str("SELECT coalesce(owner_id::text, 'instance') || '/' || listed FROM fragment_items WHERE id='username:bobcoin'"); st != "instance/false" {
		t.Fatalf("fragment name %s", st)
	}
	if n := k.int("SELECT count(*) FROM audit_log WHERE action='account_deleted' AND target_id=$1 AND actor_id=$1", bob.id); n != 1 {
		t.Fatalf("%d audit rows", n)
	}

	// carol still reads bob's message, now from Deleted Account
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(group), Limit: 50})
		if err != nil {
			return err
		}
		h, ok := res.(*tg.MessagesChannelMessages)
		if !ok {
			return fmt.Errorf("history %T", res)
		}
		found := false
		for _, m := range h.Messages {
			if m, ok := m.(*tg.Message); ok && m.Message == "bob was here" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("bob's message is gone")
		}
		for _, u := range h.Users {
			if u, ok := u.(*tg.User); ok && u.ID == bob.id {
				if !u.Deleted || u.Username != "" || u.Status != nil {
					return fmt.Errorf("bob as carol sees him %+v", u)
				}
				return nil
			}
		}
		return fmt.Errorf("bob not in the history's users")
	})

	// the inactivity job: dave stayed away past his 30 days, ada is an
	// admin and is left alone
	exec("UPDATE users SET last_seen_at = now() - interval '40 days' WHERE id IN ($1, $2)", dave.id, ada.id)
	exec("INSERT INTO account_prefs(user_id, account_ttl_days) VALUES ($1, 30), ($2, 30)", dave.id, ada.id)
	n, err := s.erase.Sweep(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if st := k.str("SELECT string_agg(account_state || ':' || coalesce(deletion_source, ''), ',' ORDER BY id) FROM users WHERE id IN ($1, $2)", ada.id, dave.id); st != "active:,deleted:inactive" {
		t.Fatalf("after the sweep %s", st)
	}
	if got := k.total(); got != starsBefore {
		t.Fatalf("Stars ledger total %d after the sweep, was %d", got, starsBefore)
	}
}
