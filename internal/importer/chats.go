package importer

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// chat is one row of the web's chats with its profile.
type chat struct {
	id        int64
	title     string
	kind      string // saved, direct, group
	creator   int64
	broadcast bool // chat_profiles.is_channel
	about     string
	username  string // only when public
	members   []int64
}

// loadChats reads every chat with its members, in id order.
func loadChats(ctx context.Context, r *run) ([]*chat, map[int64]*chat, error) {
	s := r.src
	prof, isChan, about, username, vis := "", "0", "''", "NULL", "'private'"
	if s.has("chat_profiles") {
		prof = " LEFT JOIN chat_profiles p ON p.chat_id = c.id"
		isChan, about = "coalesce("+s.col("p", "chat_profiles", "is_channel", "0")+", 0)", "coalesce("+s.col("p", "chat_profiles", "about", "''")+", '')"
		username, vis = s.col("p", "chat_profiles", "username", "NULL"), "coalesce("+s.col("p", "chat_profiles", "visibility", "'private'")+", 'private')"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT c.id, c.title, c.kind, c.created_by, %s, %s, %s, %s FROM chats c%s ORDER BY c.id",
		isChan, about, username, vis, prof))
	if err != nil {
		return nil, nil, err
	}
	var list []*chat
	byID := map[int64]*chat{}
	for rows.Next() {
		ch := &chat{}
		var isChannel int64
		var name sql.NullString
		var visibility string
		if err := rows.Scan(&ch.id, &ch.title, &ch.kind, &ch.creator, &isChannel, &ch.about, &name, &visibility); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ch.broadcast = ch.kind == "group" && isChannel != 0
		if ch.kind == "group" && visibility == "public" && name.Valid {
			ch.username = name.String
		}
		list = append(list, ch)
		byID[ch.id] = ch
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.QueryContext(ctx, "SELECT chat_id, user_id FROM members ORDER BY chat_id, user_id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, uid int64
		if err := rows.Scan(&cid, &uid); err != nil {
			return nil, nil, err
		}
		if ch := byID[cid]; ch != nil && r.users[uid] {
			ch.members = append(ch.members, uid)
		}
	}
	return list, byID, rows.Err()
}

// prefs is chat_preferences: the read position (an old global message id)
// and the pin flag, per user and chat.
type prefKey struct{ user, chat int64 }

type pref struct {
	lastRead int64
	pinned   bool
}

func loadPrefs(ctx context.Context, src *source) (map[prefKey]pref, error) {
	out := map[prefKey]pref{}
	if !src.has("chat_preferences") {
		return out, nil
	}
	rows, err := src.db.QueryContext(ctx, "SELECT user_id, chat_id, last_read_id, pinned FROM chat_preferences")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k prefKey
		var last, pinned int64
		if err := rows.Scan(&k.user, &k.chat, &last, &pinned); err != nil {
			return nil, err
		}
		out[k] = pref{lastRead: last, pinned: pinned != 0}
	}
	return out, rows.Err()
}

// pinnedAt orders pinned dialogs the way the web listed them (newest chat
// first). Pins made on this server use the clock, so they land on top.
func pinnedAt(chatID int64) int32 { return int32(1_000_000_000 + chatID) }

func encode(o bin.Encoder) []byte {
	var b bin.Buffer
	if err := o.Encode(&b); err != nil {
		panic(err) // fixed structs; can't fail
	}
	return b.Buf
}

var (
	creatorRights = tg.ChatAdminRights{ChangeInfo: true, PostMessages: true, EditMessages: true, DeleteMessages: true, BanUsers: true,
		InviteUsers: true, PinMessages: true, AddAdmins: true, ManageCall: true, Other: true, ManageTopics: true,
		PostStories: true, EditStories: true, DeleteStories: true}
	// a web admin could do everything but promote others (owner only)
	adminRights = tg.ChatAdminRights{ChangeInfo: true, PostMessages: true, EditMessages: true, DeleteMessages: true, BanUsers: true,
		InviteUsers: true, PinMessages: true, ManageCall: true, Other: true}
)

// importChats makes every web group a channel with the same id: a
// megagroup, or a broadcast channel when chat_profiles.is_channel is set.
func importChats(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	list, _, err := loadChats(ctx, r)
	if err != nil {
		return nil, err
	}
	prefs, err := loadPrefs(ctx, r.src)
	if err != nil {
		return nil, err
	}
	admins := map[prefKey]bool{}
	if r.src.has("chat_admins") {
		rows, err := r.src.db.QueryContext(ctx, "SELECT chat_id, user_id FROM chat_admins")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k prefKey
			if err := rows.Scan(&k.chat, &k.user); err != nil {
				rows.Close()
				return nil, err
			}
			admins[k] = true
		}
		rows.Close()
	}
	// web chats have no creation date; their first message is close enough
	born := map[int64]int64{}
	rows, err := r.src.db.QueryContext(ctx, "SELECT chat_id, min(created_at) FROM messages GROUP BY chat_id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		born[id] = at
	}
	rows.Close()

	chans := newBatch(w, "channels", "id", "title", "about", "username", "megagroup", "creator_id", "created_at", "default_banned", "pts")
	members := newBatch(w, "channel_members", "channel_id", "user_id", "status", "admin_rights", "date", "pinned_at")
	mapped := newBatch(w, "import_chats", "old_id", "kind", "channel_id", "members")
	noRights := encode(&tg.ChatBannedRights{})
	creatorRaw, adminRaw := encode(&creatorRights), encode(&adminRights)
	for _, ch := range list {
		switch ch.kind {
		case "saved", "direct":
			if err := mapped.add(ctx, ch.id, ch.kind, nil, ch.members); err != nil {
				return nil, err
			}
			c[ch.kind]++
			continue
		case "group":
		default:
			c["unknown_kind"]++
			continue
		}
		if !r.users[ch.creator] {
			c["no_creator"]++
			continue
		}
		at := born[ch.id]
		if at == 0 {
			at = r.now.Unix()
		}
		var name any
		if ch.username != "" {
			name = ch.username
		}
		// pts 1 like a freshly created channel; the journal starts empty
		if err := chans.add(ctx, ch.id, ch.title, ch.about, name, !ch.broadcast, ch.creator, int32(at), noRights, 1); err != nil {
			return nil, err
		}
		kind := "group"
		if ch.broadcast {
			kind = "channel"
		}
		c[kind]++
		if err := mapped.add(ctx, ch.id, kind, ch.id, []int64{}); err != nil {
			return nil, err
		}
		creatorIn := false
		for _, u := range ch.members {
			status, rights := "member", []byte(nil)
			switch {
			case u == ch.creator:
				status, rights, creatorIn = "creator", creatorRaw, true
			case admins[prefKey{u, ch.id}]:
				status, rights = "admin", adminRaw
				c["admins"]++
			}
			var pin any
			if prefs[prefKey{u, ch.id}].pinned {
				pin = pinnedAt(ch.id)
			}
			if err := members.add(ctx, ch.id, u, status, rights, int32(at), pin); err != nil {
				return nil, err
			}
		}
		if !creatorIn {
			c["creator_not_member"]++
		}
	}
	c["members"] = members.n
	for _, b := range []*batch{chans, members, mapped} {
		if err := b.flush(ctx); err != nil {
			return nil, err
		}
	}

	if r.src.has("discussion_links") {
		rows, err := r.src.db.QueryContext(ctx, "SELECT channel_id, group_id FROM discussion_links")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var ch, g int64
			if err := rows.Scan(&ch, &g); err != nil {
				return nil, err
			}
			if err := w.exec(ctx, `UPDATE channels SET linked_id = CASE id WHEN $1 THEN $2 ELSE $1 END
				WHERE id IN ($1, $2) AND EXISTS(SELECT 1 FROM channels WHERE id=$1) AND EXISTS(SELECT 1 FROM channels WHERE id=$2)`, ch, g); err != nil {
				return nil, err
			}
			c["discussion_links"]++
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return c, nil
}
