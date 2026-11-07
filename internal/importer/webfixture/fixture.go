// Package webfixture writes a small greengram-web database for the import
// tests: the web's own CREATE TABLE statements (src/database.js, with the
// columns its startup migrations add folded in) and a handful of people,
// chats and messages that hit the import's corner cases.
package webfixture

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Greengrame/greengram-server/internal/password"
	"github.com/Greengrame/greengram-server/internal/secret"
)

// What the tests sign in with and look for.
const (
	AdaPhone    = "+15550100"
	AdaPassword = "ada password 1"
	BobPassword = "bob password 22"
	// AdaToken is a live web session cookie of Ada's.
	AdaToken = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
)

// User ids; chats keep theirs as channel ids.
const (
	Ada, Bob, Carol, Helper, Gone, System         = 1, 2, 3, 4, 5, 6
	Saved, AdaBob, Team, News, NewsChat, BobCarol = 10, 11, 12, 13, 14, 15
	BotFather                                     = 7 // the web's built-in, made by ensureBuiltinBots
)

const schema = `
PRAGMA journal_mode = WAL;
CREATE TABLE users (
 id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL COLLATE NOCASE,
 name TEXT NOT NULL, password TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'member',
 disabled INTEGER NOT NULL DEFAULT 0, premium_until INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL DEFAULT (unixepoch()),
 account_state TEXT NOT NULL DEFAULT 'active' CHECK(account_state IN ('active','frozen','locked','banned','deleted')),
 phone TEXT, is_system INTEGER NOT NULL DEFAULT 0, is_suspended INTEGER NOT NULL DEFAULT 0,
 phone_lookup TEXT, premium_starts_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE system_accounts (
 kind TEXT PRIMARY KEY,user_id INTEGER UNIQUE NOT NULL REFERENCES users(id)
);
CREATE TABLE sessions (
 token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at INTEGER NOT NULL
);
CREATE TABLE session_metadata (
 token TEXT PRIMARY KEY REFERENCES sessions(token) ON DELETE CASCADE,
 user_agent TEXT NOT NULL, created_at INTEGER NOT NULL DEFAULT (unixepoch()),
 last_active INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE profile_photos (
 id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 bytes BLOB NOT NULL, mime TEXT NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL,
 created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE TABLE user_profiles (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 bio TEXT NOT NULL DEFAULT '', username_public INTEGER NOT NULL DEFAULT 1,
 avatar_id INTEGER REFERENCES profile_photos(id) ON DELETE SET NULL
);
CREATE TABLE chats (
 id INTEGER PRIMARY KEY, title TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('saved','direct','group')),
 created_by INTEGER NOT NULL REFERENCES users(id)
);
CREATE TABLE members (
 chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id), PRIMARY KEY(chat_id,user_id)
);
CREATE TABLE messages (
 id INTEGER PRIMARY KEY, chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id), body TEXT NOT NULL,
 created_at INTEGER NOT NULL DEFAULT (unixepoch()),
 is_redacted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE message_files (
 id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id),
 chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE, filename TEXT NOT NULL,
 mime TEXT NOT NULL, size INTEGER NOT NULL, bytes BLOB NOT NULL, kind TEXT NOT NULL,
 width INTEGER NOT NULL DEFAULT 0,height INTEGER NOT NULL DEFAULT 0,duration REAL NOT NULL DEFAULT 0,waveform TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE message_meta (
 message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 reply_to INTEGER REFERENCES messages(id) ON DELETE SET NULL,file_id INTEGER REFERENCES message_files(id),
 edited_at INTEGER,pinned INTEGER NOT NULL DEFAULT 0,entities TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE message_service_events (
 message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,reference TEXT NOT NULL UNIQUE
);
CREATE TABLE message_forwards (
 message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,origin TEXT NOT NULL
);
CREATE TABLE discussion_links (
 channel_id INTEGER PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
 group_id INTEGER UNIQUE NOT NULL REFERENCES chats(id) ON DELETE CASCADE
);
CREATE TABLE discussion_posts (
 channel_message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 group_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 group_message_id INTEGER UNIQUE NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 PRIMARY KEY(channel_message_id,group_id)
);
CREATE TABLE message_threads (
 message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 root_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE
);
CREATE TABLE chat_preferences (
 user_id INTEGER NOT NULL REFERENCES users(id), chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 archived INTEGER NOT NULL DEFAULT 0,pinned INTEGER NOT NULL DEFAULT 0,muted_until INTEGER NOT NULL DEFAULT 0,
 last_read_id INTEGER NOT NULL DEFAULT 0,draft TEXT,
 notify_override INTEGER NOT NULL DEFAULT 0, notify_previews INTEGER, PRIMARY KEY(user_id,chat_id)
);
CREATE TABLE settings (
 key TEXT PRIMARY KEY, value TEXT NOT NULL
);
CREATE TABLE chat_profiles (
 chat_id INTEGER PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
 is_channel INTEGER NOT NULL DEFAULT 0, about TEXT NOT NULL DEFAULT '',
 username TEXT UNIQUE COLLATE NOCASE, visibility TEXT NOT NULL DEFAULT 'private',
 is_megagroup INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE chat_admins (
 chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id), PRIMARY KEY(chat_id,user_id)
);
CREATE TABLE bots (
 user_id INTEGER PRIMARY KEY REFERENCES users(id),owner_id INTEGER REFERENCES users(id),
 kind TEXT NOT NULL,token_hash TEXT UNIQUE,state TEXT NOT NULL DEFAULT '{}',description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE chat_visibility (
 user_id INTEGER NOT NULL REFERENCES users(id),chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 clear_until INTEGER NOT NULL DEFAULT 0,hidden INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(user_id,chat_id)
);
CREATE TABLE account_settings (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,value TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE blocked_users (
 blocker_id INTEGER NOT NULL REFERENCES users(id), blocked_id INTEGER NOT NULL REFERENCES users(id),
 PRIMARY KEY(blocker_id,blocked_id)
);
`

// T0 is when the fixture's messages start.
const T0 = 1_700_000_000

// Build writes the fixture to path. Phones are encrypted with key, like the
// web server would with its instance-secret.key.
func Build(t testing.TB, path string, key *secret.Key) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(schema)
	hash := func(pw string) string {
		h, err := password.Hash(pw)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	phone, err := key.EncryptPhone(AdaPhone, fmt.Sprintf("user:%d", Ada))
	if err != nil {
		t.Fatal(err)
	}
	user := `INSERT INTO users(id, username, name, password, role, disabled, premium_until, created_at, account_state, phone, phone_lookup, is_system)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	exec(user, Ada, "ada", "Ada Lovelace", hash(AdaPassword), "admin", 0, 2_000_000_000, T0-1000, "active", phone, key.PhoneLookup(AdaPhone), 0)
	exec(user, Bob, "bob", "Bob", hash(BobPassword), "member", 0, 0, T0-900, "active", nil, nil, 0)
	exec(user, Carol, "carol", "Carol", hash("carol password 3"), "member", 0, 0, T0-800, "active", nil, nil, 0)
	exec(user, Helper, "helperbot", "Helper", "", "member", 0, 0, T0-700, "active", nil, nil, 0)
	exec(user, Gone, "deleted_5_abcdef", "Deleted Account", "!", "member", 1, 0, T0-600, "deleted", nil, nil, 0)
	exec(user, System, "instance_premium_payments", "Premium", "!system-account", "member", 0, 0, T0-500, "active", nil, nil, 1)
	exec("INSERT INTO system_accounts VALUES ('premium_payments', ?)", System)
	exec("INSERT INTO bots(user_id, owner_id, kind) VALUES (?, ?, 'custom')", Helper, Ada)
	exec(user, BotFather, "botfather", "BotFather", "!service-account", "member", 0, 0, T0-400, "active", nil, nil, 0)
	exec("INSERT INTO bots(user_id, kind, description) VALUES (?, 'botfather', 'old text')", BotFather)
	exec("INSERT INTO user_profiles(user_id, bio, username_public) VALUES (?, 'hi there', 1), (?, '', 0)", Ada, Carol)

	now := time.Now().Unix()
	sess := func(token string, user int64, expires int64) {
		exec("INSERT INTO sessions VALUES (?, ?, ?)", token, user, expires)
		exec("INSERT INTO session_metadata(token, user_agent, created_at, last_active) VALUES (?, 'Firefox', ?, ?)", token, now-3600, now-60)
	}
	sess(TokenHash(AdaToken), Ada, now+86400)
	sess(TokenHash("expired-session"), Ada, now-10)
	sess(TokenHash("system-session"), System, now+86400)
	exec("INSERT INTO sessions VALUES ('not-a-hash', ?, ?)", Bob, now+86400)

	exec(`INSERT INTO account_settings VALUES (?, ?)`, Ada, `{"privacy":{"phoneNumber":{"visibility":"nobody","allowUserIds":["2"],"allowChatIds":["-1000000000012","-999"]},`+
		`"lastSeen":{"visibility":"contacts","isUnspecified":true},"madeUp":{"visibility":"nobody"}},"contacts":["2","77","3"],"contactNames":{"2":"Bobby Builder"}}`)
	exec(`INSERT INTO settings VALUES ('sessionDays', '30'), ('registrationEnabled', 'true'), ('broken', '{nope')`)

	chat := func(id int64, title, kind string, by int64, members ...int64) {
		exec("INSERT INTO chats VALUES (?, ?, ?, ?)", id, title, kind, by)
		for _, m := range members {
			exec("INSERT INTO members VALUES (?, ?)", id, m)
		}
	}
	chat(Saved, "Saved Messages", "saved", Ada, Ada)
	chat(AdaBob, "", "direct", Ada, Ada, Bob)
	chat(Team, "Team", "group", Ada, Ada, Bob, Carol)
	chat(News, "News", "group", Bob, Bob, Ada)
	chat(NewsChat, "News chat", "group", Bob, Bob, Ada, Carol)
	chat(BobCarol, "", "direct", Bob, Bob, Carol)
	exec("INSERT INTO chat_admins VALUES (?, ?)", Team, Carol)
	exec("INSERT INTO chat_profiles(chat_id, is_channel, about, username, visibility) VALUES (?, 1, 'daily news', 'newsroom', 'public')", News)
	exec("INSERT INTO chat_profiles(chat_id, is_channel, is_megagroup) VALUES (?, 0, 1)", NewsChat)
	exec("INSERT INTO discussion_links VALUES (?, ?)", News, NewsChat)

	msg := func(id, chat, from int64, body string) {
		exec("INSERT INTO messages(id, chat_id, user_id, body, created_at) VALUES (?, ?, ?, ?, ?)", id, chat, from, body, T0+id)
		exec("INSERT INTO message_meta(message_id, entities) VALUES (?, '[]')", id)
	}
	msg(101, Saved, Ada, "note to self")
	msg(102, AdaBob, Ada, "hi bob")
	msg(103, Team, Ada, "hello team")
	msg(104, AdaBob, Bob, "hi ada")
	exec(`UPDATE message_meta SET reply_to = 102, entities = '[{"type":"MessageEntityBold","offset":0,"length":2},{"type":"MessageEntityWhatever","offset":0,"length":1}]' WHERE message_id = 104`)
	msg(105, News, Bob, "first post")
	msg(106, NewsChat, Bob, "first post")
	exec("INSERT INTO discussion_posts VALUES (105, ?, 106)", NewsChat)
	msg(107, NewsChat, Carol, "nice post")
	exec("INSERT INTO message_threads VALUES (107, 106)")
	msg(108, AdaBob, Bob, "edited later")
	exec("UPDATE message_meta SET edited_at = ? WHERE message_id = 108", T0+500)
	msg(109, Team, Carol, "welcome")
	exec("UPDATE message_meta SET reply_to = 103 WHERE message_id = 109")
	msg(110, Team, Gone, "gone")
	exec("UPDATE messages SET is_redacted = 1 WHERE id = 110")
	msg(111, BobCarol, Bob, "secret 1")
	msg(112, BobCarol, Carol, "carol reply")
	exec("INSERT INTO chat_visibility VALUES (?, ?, 112, 1)", Carol, BobCarol)
	msg(113, BobCarol, Bob, "after clear")
	msg(114, Team, Ada, "Ada pinned a message")
	exec("INSERT INTO message_service_events VALUES (114, 'pin', 'ref-114')")
	msg(115, AdaBob, Ada, "fwd text")
	exec(`INSERT INTO message_forwards VALUES (115, '{"type":"user","date":1690000000,"sender_user":{"id":3,"first_name":"Carol"}}')`)
	msg(116, AdaBob, Bob, "unread one")
	msg(117, BobCarol, Carol, "fwd post")
	exec(`INSERT INTO message_forwards VALUES (117, ?)`, fmt.Sprintf(`{"type":"channel","date":%d,"chat":{"id":%d,"type":"channel","title":"News"},"message_id":105}`, T0+105, News))
	msg(118, BobCarol, Bob, "fwd hidden")
	exec(`INSERT INTO message_forwards VALUES (118, '{"type":"hidden_user","date":1690000001,"sender_user_name":"Someone Else"}')`)

	exec("INSERT INTO blocked_users VALUES (?, ?), (?, 99)", Carol, Bob, Carol)

	pref := func(user, chat, lastRead, pinned int64) {
		exec("INSERT INTO chat_preferences(user_id, chat_id, last_read_id, pinned) VALUES (?, ?, ?, ?)", user, chat, lastRead, pinned)
	}
	pref(Ada, AdaBob, 108, 1)
	pref(Bob, AdaBob, 102, 0)
	pref(Ada, Team, 114, 0)
	pref(Carol, Team, 103, 0)
	pref(Ada, News, 0, 1)
}

// TokenHash is how both servers store a session cookie.
func TokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
