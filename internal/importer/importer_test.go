package importer

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/importer/webfixture"
	"github.com/Greengrame/greengram-server/internal/secret"
)

func TestConvertEntities(t *testing.T) {
	raw := `[{"type":"MessageEntityBold","offset":0,"length":2},
		{"type":"MessageEntityMentionName","offset":3,"length":4,"userId":"7"},
		{"type":"MessageEntityCustomEmoji","offset":8,"length":2,"documentId":"4000000012"},
		{"type":"MessageEntityTextUrl","offset":1,"length":1,"url":"https://x.test"},
		{"type":"MessageEntityNope","offset":0,"length":1},
		{"type":"MessageEntityItalic","offset":-1,"length":1}]`
	enc, dropped := convertEntities(raw)
	if dropped != 2 {
		t.Fatalf("dropped %d, want 2", dropped)
	}
	b := &bin.Buffer{Buf: enc}
	n, err := b.VectorHeader()
	if err != nil || n != 4 {
		t.Fatalf("vector of %d, %v", n, err)
	}
	var got []tg.MessageEntityClass
	for range n {
		e, err := tg.DecodeMessageEntity(b)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if m, ok := got[1].(*tg.MessageEntityMentionName); !ok || m.UserID != 7 || m.Offset != 3 {
		t.Fatalf("mention %+v", got[1])
	}
	if e, ok := got[2].(*tg.MessageEntityCustomEmoji); !ok || e.DocumentID != 4000000012 {
		t.Fatalf("custom emoji %+v", got[2])
	}
	if enc, _ := convertEntities("[]"); enc != nil {
		t.Fatal("empty list should store NULL")
	}
	if _, dropped := convertEntities("{broken"); dropped != 1 {
		t.Fatal("broken JSON should count as dropped")
	}
}

func TestOldChatID(t *testing.T) {
	for peer, want := range map[int64]int64{-1000000000012: 12, -12: 12} {
		if got := oldChatID(peer); got != want {
			t.Errorf("oldChatID(%d) = %d, want %d", peer, got, want)
		}
	}
}

// The dry run needs no Postgres: it reads and converts the whole file.
func TestDryRun(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	path := filepath.Join(t.TempDir(), "web.sqlite3")
	webfixture.Build(t, path, secret.FromBytes(key))

	rep, err := Run(context.Background(), Options{SQLite: path, DryRun: true, Secret: secret.FromBytes(key)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Counts{}
	for _, s := range rep.Stages {
		got[s.Name] = s.Counts
	}
	want := map[string]Counts{
		"settings": {"settings": 2, "bad_json": 1},
		"users":    {"users": 7, "admins": 1, "phones": 1, "system": 1, "bots": 2, "builtin_bots": 1},
		"sessions": {"sessions": 1, "expired": 1, "disabled_account": 1, "bad_token": 1},
		"chats":    {"saved": 1, "direct": 2, "group": 2, "channel": 1, "admins": 1, "members": 8, "discussion_links": 1},
		"privacy":  {"privacy_rules": 2, "unknown_keys": 1, "chat_exceptions_dropped": 1, "contacts": 2, "contacts_skipped": 1},
		"blocks":   {"blocked_users": 1, "blocks_skipped": 1},
		// 11 private messages: 101 (one copy), 102 104 108 115 116 117 118
		// (two), 111 112 113 (two each, minus carol's two cleared)
		"private": {"messages": 11, "copies": 19, "cleared_copies": 2, "dialogs": 5, "boxes": 3, "service_as_text": 0,
			"entities_dropped": 1, "redacted": 0},
		"channel": {"messages": 6, "service": 1, "discussion_copies": 1, "read_marks": 2, "redacted": 1, "private_post_forwards": 1},
	}
	for stage, w := range want {
		for k, v := range w {
			if got[stage][k] != v {
				t.Errorf("%s %s = %d, want %d (all: %v)", stage, k, got[stage][k], v, got[stage])
			}
		}
	}

	// a different key must be caught before anything is written
	other := make([]byte, 32)
	rand.Read(other)
	rep, err = Run(context.Background(), Options{SQLite: path, DryRun: true, Secret: secret.FromBytes(other)})
	if err != nil {
		t.Fatal(err)
	}
	if n := rep.Stages[1].Counts["phones_unreadable"]; n != 1 {
		t.Fatalf("wrong key: phones_unreadable=%d", n)
	}
}
