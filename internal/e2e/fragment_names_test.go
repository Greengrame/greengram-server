package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/web"
)

// Short and digit-only names: only an admin puts them up, the buyer can
// resell them and wear them; toggling makes one resolve to its owner.
func TestFragmentShortNames(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	svc := fragment.New(s.db.Pool, s.secret, nil)
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts}, Fragment: svc}).Register(s.mux)

	boss, _ := phoneSignUp(t, s, "+1 555 0201", "boss", "correct horse 1")
	for _, name := range []string{"@ab", "@7", "@777"} {
		boss.must("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": name, "amount": "1"})
	}
	if code, out := boss.post("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": "@__", "amount": "1"}); code != 400 {
		t.Fatalf("underscores only: %d %v", code, out)
	}
	alice, aid := phoneSignUp(t, s, "+1 555 0202", "alicex", "correct horse 2")
	if code, out := alice.post("/api/fragment/listing", map[string]any{"kind": "username", "value": "zz", "amount": "1"}); code != 400 {
		t.Fatalf("member listing a short name: %d %v", code, out)
	}
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 20000000000)", aid); err != nil {
		t.Fatal(err)
	}
	q := alice.must("/api/fragment/quote", map[string]any{"itemId": "username:ab"})
	alice.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]})

	u, _, err := s.accounts.User(ctx, aid)
	if err != nil || len(u.Aliases) != 1 || u.Aliases[0].Name != "ab" || u.Aliases[0].Active {
		t.Fatalf("bought name isn't an inactive alias: %v %v", u.Aliases, err)
	}
	if free, err := s.accounts.CheckUsername(ctx, aid, "ab"); err != nil || !free {
		t.Fatalf("owner can't take their own collectible: %v %v", free, err)
	}
	if _, found, _ := s.accounts.UserByAlias(ctx, "ab"); found {
		t.Fatal("inactive alias resolves")
	}
	if err := s.accounts.ToggleUsername(ctx, u, "ab", true); err != nil {
		t.Fatal(err)
	}
	if who, found, err := s.accounts.UserByAlias(ctx, "ab"); err != nil || !found || who.ID != aid {
		t.Fatalf("active alias resolves to %d, %v, %v", who.ID, found, err)
	}
	if err := s.accounts.ToggleUsername(ctx, u, "777", true); err == nil {
		t.Fatal("toggled a name they don't own")
	}

	// the owner may resell it, which takes it off the profile
	alice.must("/api/fragment/listing", map[string]any{"kind": "username", "value": "ab", "amount": "3"})
	if _, found, _ := s.accounts.UserByAlias(ctx, "ab"); found {
		t.Fatal("alias still resolves while up for sale")
	}
}

// A channel's creator sells its public username; the sale makes it private.
func TestFragmentChannelHandle(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	svc := fragment.New(s.db.Pool, s.secret, nil)
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts}, Fragment: svc}).Register(s.mux)

	phoneSignUp(t, s, "+1 555 0301", "boss", "correct horse 1")
	bob, bid := phoneSignUp(t, s, "+1 555 0302", "bobx1", "correct horse 2")
	carol, cid := phoneSignUp(t, s, "+1 555 0303", "carolx", "correct horse 3")
	var channel int64
	if err := s.db.Pool.QueryRow(ctx, `INSERT INTO channels(title, username, megagroup, creator_id, created_at)
		VALUES ('News', 'BobNews', false, $1, 1) RETURNING id`, bid).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO channel_members(channel_id, user_id, status, date) VALUES ($1, $2, 'creator', 1), ($1, $3, 'member', 1)`, channel, bid, cid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 20000000000)", cid); err != nil {
		t.Fatal(err)
	}

	_, cat := bob.fragmentCall("GET", "/api/fragment/catalog", nil)
	found := false
	for _, a := range cat["ownedAssets"].([]any) {
		m := a.(map[string]any)
		found = found || m["title"] == "@bobnews" && m["channelTitle"] == "News"
	}
	if !found {
		t.Fatalf("channel handle not among the assets: %v", cat["ownedAssets"])
	}
	listing := map[string]any{"kind": "username", "value": "@bobnews", "amount": "2"}
	if code, out := carol.post("/api/fragment/listing", listing); code != 409 {
		t.Fatalf("member selling someone's channel: %d %v", code, out)
	}
	if code, out := bob.post("/api/fragment/listing", listing); code != 400 {
		t.Fatalf("no password: %d %v", code, out)
	}
	listing["password"] = "correct horse 2"
	bob.must("/api/fragment/listing", listing)

	// listed, not sold: the channel keeps its link
	var name *string
	s.db.Pool.QueryRow(ctx, "SELECT username FROM channels WHERE id=$1", channel).Scan(&name)
	if name == nil {
		t.Fatal("listing took the link away")
	}
	q := carol.must("/api/fragment/quote", map[string]any{"itemId": "username:bobnews"})
	carol.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]})
	s.db.Pool.QueryRow(ctx, "SELECT username FROM channels WHERE id=$1", channel).Scan(&name)
	if name != nil {
		t.Fatalf("channel kept the sold link: %s", *name)
	}
	var owner int64
	var units string
	s.db.Pool.QueryRow(ctx, "SELECT owner_id FROM fragment_items WHERE id='username:bobnews'").Scan(&owner)
	s.db.Pool.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", bid).Scan(&units)
	if owner != cid || units != "2000000000" {
		t.Fatalf("owner %d, seller got %s", owner, units)
	}
}

// seedChannel makes a channel with a public username and its creator.
func seedChannel(t *testing.T, s *stack, title, username string, creator int64, members ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	var channel int64
	if err := s.db.Pool.QueryRow(ctx, `INSERT INTO channels(title, username, megagroup, creator_id, created_at)
		VALUES ($1, $2, true, $3, 1) RETURNING id`, title, username, creator).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO channel_members(channel_id, user_id, status, date) VALUES ($1, $2, 'creator', 1)", channel, creator); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := s.db.Pool.Exec(ctx, "INSERT INTO channel_members(channel_id, user_id, status, date) VALUES ($1, $2, 'member', 1)", channel, m); err != nil {
			t.Fatal(err)
		}
	}
	return channel
}

func channelName(t *testing.T, s *stack, channel int64) string {
	t.Helper()
	var name *string
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT username FROM channels WHERE id=$1", channel).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name == nil {
		return ""
	}
	return *name
}

func aliasNames(t *testing.T, s *stack, user int64) []string {
	t.Helper()
	u, _, err := s.accounts.User(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, al := range u.Aliases {
		out = append(out, al.Name)
	}
	return out
}

func itemRows(t *testing.T, s *stack, id string) int {
	t.Helper()
	var n int
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM fragment_items WHERE id=$1", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// tonTotal is every account's GRAM, which a sale only moves around.
func tonTotal(t *testing.T, s *stack) string {
	t.Helper()
	var total string
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT coalesce(sum(units), 0)::text FROM crypto_accounts WHERE asset='TON'").Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

// Unlisting a channel's name leaves it the channel's: not the seller's
// collectible, on neither their profile nor their list of names to reuse.
func TestFragmentChannelUnlist(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	svc := fragment.New(s.db.Pool, s.secret, nil)
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts}, Fragment: svc}).Register(s.mux)

	boss, _ := phoneSignUp(t, s, "+1 555 0601", "boss", "correct horse 1")
	bob, bid := phoneSignUp(t, s, "+1 555 0602", "bobx2", "correct horse 2")
	carol, cid := phoneSignUp(t, s, "+1 555 0603", "carolx2", "correct horse 3")
	channel := seedChannel(t, s, "Tuffs", "Tuffs", bid, cid)
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 20000000000)", cid); err != nil {
		t.Fatal(err)
	}

	listing := map[string]any{"kind": "username", "value": "@tuffs", "amount": "2", "password": "correct horse 2"}
	bob.must("/api/fragment/listing", listing)
	if code, out := carol.fragmentCall("DELETE", "/api/fragment/listing", map[string]any{"itemId": "username:tuffs"}); code != 403 {
		t.Fatalf("someone else unlisting: %d %v", code, out)
	}
	quote := carol.must("/api/fragment/quote", map[string]any{"itemId": "username:tuffs"})
	if code, out := bob.fragmentCall("DELETE", "/api/fragment/listing", map[string]any{"itemId": "username:tuffs"}); code != 200 {
		t.Fatalf("unlist: %d %v", code, out)
	}
	if got := aliasNames(t, s, bid); len(got) != 0 {
		t.Fatalf("channel's name on the seller's profile: %v", got)
	}
	if channelName(t, s, channel) != "Tuffs" || itemRows(t, s, "username:tuffs") != 0 {
		t.Fatalf("channel name %q, items %d", channelName(t, s, channel), itemRows(t, s, "username:tuffs"))
	}
	if code, out := bob.fragmentCall("DELETE", "/api/fragment/listing", map[string]any{"itemId": "username:tuffs"}); code != 404 {
		t.Fatalf("unlist twice: %d %v", code, out)
	}
	// the quote from before can't buy it, and nobody else can take the name
	if code, out := carol.post("/api/fragment/purchase", map[string]any{"quoteId": quote["id"]}); code == 200 {
		t.Fatalf("old quote bought an unlisted name: %v", out)
	}
	if free, err := s.accounts.CheckUsername(ctx, cid, "tuffs"); err != nil || free {
		t.Fatalf("channel's name free for others: %v %v", free, err)
	}
	if free, err := s.accounts.CheckUsername(ctx, bid, "tuffs"); err != nil || free {
		t.Fatalf("channel's name free for its creator: %v %v", free, err)
	}
	if err := func() error {
		u, _, err := s.accounts.User(ctx, bid)
		if err != nil {
			return err
		}
		return s.accounts.ToggleUsername(ctx, u, "tuffs", true)
	}(); err == nil {
		t.Fatal("wore a channel's name")
	}
	_, cat := bob.fragmentCall("GET", "/api/fragment/catalog", nil)
	found := false
	for _, a := range cat["ownedAssets"].([]any) {
		m := a.(map[string]any)
		found = found || m["title"] == "@tuffs" && m["channelTitle"] == "Tuffs" && m["listed"] == false
	}
	if !found {
		t.Fatalf("still the channel's to sell: %v", cat["ownedAssets"])
	}

	// and it sells as before: the channel goes private, the money only moves
	bob.must("/api/fragment/listing", listing)
	before := tonTotal(t, s)
	q := carol.must("/api/fragment/quote", map[string]any{"itemId": "username:tuffs"})
	first := carol.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]})
	if again := carol.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]}); again["balance"] != first["balance"] {
		t.Fatalf("replayed purchase: %v then %v", first, again)
	}
	if channelName(t, s, channel) != "" || tonTotal(t, s) != before {
		t.Fatalf("after the sale: channel %q, total %s was %s", channelName(t, s, channel), tonTotal(t, s), before)
	}
	if got := aliasNames(t, s, cid); len(got) != 1 || got[0] != "tuffs" {
		t.Fatalf("buyer's names: %v", got)
	}

	// a bought name on your own channel stays yours: off the profile while
	// the channel uses it, back when it stops, through a list and unlist too
	boss.must("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": "@perks", "amount": "1"})
	q = carol.must("/api/fragment/quote", map[string]any{"itemId": "username:perks"})
	carol.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]})
	second := seedChannel(t, s, "Perks", "perks", cid)
	if got := aliasNames(t, s, cid); len(got) != 1 || got[0] != "tuffs" {
		t.Fatalf("bought name on a channel still on the profile: %v", got)
	}
	carol.must("/api/fragment/listing", map[string]any{"kind": "username", "value": "@perks", "amount": "3", "password": "correct horse 3"})
	if code, out := carol.fragmentCall("DELETE", "/api/fragment/listing", map[string]any{"itemId": "username:perks"}); code != 200 {
		t.Fatalf("unlist: %d %v", code, out)
	}
	if itemRows(t, s, "username:perks") != 1 {
		t.Fatal("unlisting lost a bought name")
	}
	if got := aliasNames(t, s, cid); len(got) != 1 {
		t.Fatalf("bought name on a channel back on the profile: %v", got)
	}
	if _, err := s.db.Pool.Exec(ctx, "UPDATE channels SET username=NULL WHERE id=$1", second); err != nil {
		t.Fatal(err)
	}
	if got := aliasNames(t, s, cid); len(got) != 2 {
		t.Fatalf("name the channel let go of: %v", got)
	}
}

// What an unlist left behind before: the migration clears exactly those rows.
func TestFragmentChannelItemsMigration(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	_, bid := phoneSignUp(t, s, "+1 555 0701", "boss", "correct horse 1")
	seedChannel(t, s, "Stale", "stale", bid)
	seedChannel(t, s, "Bought", "bought", bid)
	seedChannel(t, s, "Selling", "selling", bid)
	for _, row := range []struct {
		name   string
		listed bool
		bought any
	}{{"stale", false, nil}, {"bought", false, 5}, {"selling", true, nil}, {"oldname", false, nil}} {
		if _, err := s.db.Pool.Exec(ctx, "INSERT INTO fragment_items(id, kind, title, units, owner_id, listed, purchased_at) VALUES ($1, 'username', $2, 1, $3, $4, $5)",
			"username:"+row.name, "@"+row.name, bid, row.listed, row.bought); err != nil {
			t.Fatal(err)
		}
	}
	if got := aliasNames(t, s, bid); len(got) != 1 || got[0] != "oldname" {
		t.Fatalf("names on channels read as the person's: %v", got)
	}
	raw, err := os.ReadFile("../store/migrations/0400_channel_name_items.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"stale": 0, "bought": 1, "selling": 1, "oldname": 1} {
		if got := itemRows(t, s, "username:"+name); got != want {
			t.Fatalf("%s: %d rows, want %d", name, got, want)
		}
	}
}

// A channel can move to a new name as part of listing its old one.
func TestFragmentChannelReplacement(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	svc := fragment.New(s.db.Pool, s.secret, nil)
	var released []int64
	svc.ChannelReleased = func(id int64) { released = append(released, id) }
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts}, Fragment: svc}).Register(s.mux)

	boss, _ := phoneSignUp(t, s, "+1 555 0801", "boss", "correct horse 1")
	bob, bid := phoneSignUp(t, s, "+1 555 0802", "bobx3", "correct horse 2")
	carol, cid := phoneSignUp(t, s, "+1 555 0803", "carolx3", "correct horse 3")
	channel := seedChannel(t, s, "Old", "OldName", bid, cid)
	seedChannel(t, s, "Other", "othername", cid)
	boss.must("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": "@forsale", "amount": "1"})
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 20000000000)", cid); err != nil {
		t.Fatal(err)
	}
	listing := func(next string, password bool) map[string]any {
		out := map[string]any{"kind": "username", "value": "@oldname", "amount": "2", "channelUsername": next}
		if password {
			out["password"] = "correct horse 2"
		}
		return out
	}

	for name, c := range map[string]struct {
		who  *browser
		body map[string]any
		code int
	}{
		"bad shape":         {bob, listing("a!", true), 400},
		"too short":         {bob, listing("abc", true), 400},
		"its own name":      {bob, listing("@OLDNAME", true), 400},
		"a person's":        {bob, listing("carolx3", true), 409},
		"another channel":   {bob, listing("othername", true), 409},
		"up for sale":       {bob, listing("forsale", true), 409},
		"reserved":          {bob, listing("botfather", true), 409},
		"announcements":     {bob, listing("announcements", true), 409},
		"no password":       {bob, listing("newname", false), 400},
		"not the creator":   {carol, listing("newname", true), 409},
		"not their channel": {carol, map[string]any{"kind": "username", "value": "@oldname", "amount": "2", "channelUsername": "newname", "password": "correct horse 3"}, 409},
		"no channel":        {bob, map[string]any{"kind": "username", "value": "@nobodys", "amount": "2", "channelUsername": "newname", "password": "correct horse 2"}, 400},
		"the admin":         {boss, map[string]any{"kind": "username", "value": "@nobodys2", "amount": "2", "channelUsername": "newname"}, 400},
	} {
		path := "/api/fragment/listing"
		if c.who == boss {
			path = "/api/admin/fragment/listing"
		}
		if code, out := c.who.post(path, c.body); code != c.code {
			t.Fatalf("%s: %d %v", name, code, out)
		}
		if channelName(t, s, channel) != "OldName" || itemRows(t, s, "username:oldname") != 0 || len(released) != 0 {
			t.Fatalf("%s changed something: channel %q, items %d, released %v", name, channelName(t, s, channel), itemRows(t, s, "username:oldname"), released)
		}
	}

	bob.must("/api/fragment/listing", listing("@NewName", true))
	if channelName(t, s, channel) != "NewName" || itemRows(t, s, "username:oldname") != 1 {
		t.Fatalf("after listing: channel %q, items %d", channelName(t, s, channel), itemRows(t, s, "username:oldname"))
	}
	if len(released) != 1 || released[0] != channel {
		t.Fatalf("members not told: %v", released)
	}
	// the old name is out of reach, and a replay changes nothing
	if free, err := s.accounts.CheckUsername(ctx, cid, "oldname"); err != nil || free {
		t.Fatalf("old name free: %v %v", free, err)
	}
	if code, out := bob.post("/api/fragment/listing", listing("anothername", true)); code != 400 {
		t.Fatalf("replay with another name: %d %v", code, out)
	}
	if channelName(t, s, channel) != "NewName" || len(released) != 1 {
		t.Fatalf("replay moved the channel to %q, released %v", channelName(t, s, channel), released)
	}

	// unlisted, it's the seller's name to keep or put back
	if code, out := bob.fragmentCall("DELETE", "/api/fragment/listing", map[string]any{"itemId": "username:oldname"}); code != 200 {
		t.Fatalf("unlist: %d %v", code, out)
	}
	if got := aliasNames(t, s, bid); len(got) != 1 || got[0] != "oldname" {
		t.Fatalf("seller's names: %v", got)
	}
	if channelName(t, s, channel) != "NewName" {
		t.Fatalf("unlist moved the channel to %q", channelName(t, s, channel))
	}
	chans := channels.New(s.db.Pool)
	if free, err := chans.UsernameFreeFor(ctx, bid, "oldname", channel); err != nil || !free {
		t.Fatalf("can't put it back: %v %v", free, err)
	}
	if free, err := chans.UsernameFreeFor(ctx, cid, "oldname", channel); err != nil || free {
		t.Fatalf("someone else can take it: %v %v", free, err)
	}

	// sold: the channel keeps its new name, the money only moves
	bob.must("/api/fragment/listing", map[string]any{"kind": "username", "value": "@oldname", "amount": "2"})
	before := tonTotal(t, s)
	q := carol.must("/api/fragment/quote", map[string]any{"itemId": "username:oldname"})
	carol.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]})
	var owner int64
	var paid string
	s.db.Pool.QueryRow(ctx, "SELECT owner_id FROM fragment_items WHERE id='username:oldname'").Scan(&owner)
	s.db.Pool.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", bid).Scan(&paid)
	if owner != cid || paid != "2000000000" || channelName(t, s, channel) != "NewName" || tonTotal(t, s) != before {
		t.Fatalf("owner %d, seller got %s, channel %q, total %s was %s", owner, paid, channelName(t, s, channel), tonTotal(t, s), before)
	}
	if len(released) != 1 {
		t.Fatalf("a sale with nothing on the channel told its members: %v", released)
	}
}

func TestAdminPasswordReset(t *testing.T) {
	s := start(t)
	s.mountAdmin()
	boss, bossID := phoneSignUp(t, s, "+1 555 0401", "boss", "correct horse 1")
	alice, aid := phoneSignUp(t, s, "+1 555 0402", "alicey", "correct horse 2")

	path := fmt.Sprintf("/api/admin/users/%d/password", aid)
	if code, out := alice.post(path, map[string]any{"password": "brand new password"}); code != 403 {
		t.Fatalf("member resetting: %d %v", code, out)
	}
	if code, out := boss.post(path, map[string]any{"password": "short"}); code != 400 {
		t.Fatalf("short password: %d %v", code, out)
	}
	if code, out := boss.post(fmt.Sprintf("/api/admin/users/%d/password", bossID), map[string]any{"password": "brand new password"}); code != 400 {
		t.Fatalf("admin target: %d %v", code, out)
	}
	boss.must(path, map[string]any{"password": "brand new password"})
	if id, code := phoneSignIn(t, s, "+15550402", "correct horse 2"); code != 401 {
		t.Fatalf("old password still works: %d", id)
	}
	if id, _ := phoneSignIn(t, s, "+15550402", "brand new password"); id != aid {
		t.Fatalf("new password: %d", id)
	}
	if code, _ := alice.fragmentCall("GET", "/api/me", nil); code != 401 {
		t.Fatalf("old session survived: %d", code)
	}
}

// The panel's Published and Access switches reach the people the packs go to.
func TestPackSwitches(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	phoneSignUp(t, s, "+1 555 0501", "boss", "correct horse 1")
	_, aid := phoneSignUp(t, s, "+1 555 0502", "aliceq", "correct horse 2")
	const pack = 777001
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO sticker_sets(id, access_hash, short_name, title, kind, is_default, created_at)
		VALUES ($1, 1, 'switchtest', 'Switch Test', 'emoji', true, 1)`, pack); err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		t.Helper()
		list, err := s.media.Installed(ctx, aid, media.SetEmoji)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range list {
			if v.ID == pack {
				return true
			}
		}
		return false
	}
	set := func(sql string) {
		t.Helper()
		if _, err := s.db.Pool.Exec(ctx, "UPDATE sticker_sets SET "+sql+" WHERE id=$1", pack); err != nil {
			t.Fatal(err)
		}
	}
	if !has() {
		t.Fatal("default pack not installed")
	}
	set("enabled=false")
	if has() {
		t.Fatal("unpublished pack still installed")
	}
	if _, _, found, err := s.media.StickerSet(ctx, aid, &tg.InputStickerSetShortName{ShortName: "switchtest"}); err != nil || found {
		t.Fatalf("unpublished pack found: %v %v", found, err)
	}
	set("enabled=true, premium_only=true")
	if has() {
		t.Fatal("Premium pack installed without Premium")
	}
	if err := s.media.Install(ctx, aid, pack, true); err == nil {
		t.Fatal("installed a Premium pack without Premium")
	}
	if _, err := s.db.Pool.Exec(ctx, "UPDATE users SET premium_until=4000000000 WHERE id=$1", aid); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("Premium pack hidden from a Premium user")
	}
}
