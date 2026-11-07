package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/iamxvbaba/td/tg"
)

// msg is one web message with what message_meta and friends add to it.
type msg struct {
	id, chat, from int64
	text           string
	date           int64
	replyTo        int64
	editDate       int64
	entities       []byte
	fwd            origin
	service        bool
}

// origin is a forward's header from message_forwards, in old ids: a
// person, a channel post (post is the old message id; 0 for a group's
// sender_chat) or a name only.
type origin struct {
	user, chat, post int64
	name             string
	date             int64
}

// parseOrigin reads one message_forwards.origin. A person whose forwards
// privacy isn't "everybody" comes over as a name: the web decided that per
// viewer, and here the header is fixed when the message is written.
func parseOrigin(raw string, r *run, people map[int64]person) (origin, bool) {
	var o struct {
		Type   string `json:"type"`
		Date   int64  `json:"date"`
		Sender struct {
			ID idText `json:"id"`
		} `json:"sender_user"`
		Name string `json:"sender_user_name"`
		Chat struct {
			ID    idText `json:"id"`
			Title string `json:"title"`
		} `json:"chat"`
		SenderChat struct {
			ID    idText `json:"id"`
			Title string `json:"title"`
		} `json:"sender_chat"`
		Post idText `json:"message_id"`
	}
	if json.Unmarshal([]byte(raw), &o) != nil {
		return origin{}, false
	}
	out := origin{date: o.Date}
	switch o.Type {
	case "user":
		id := int64(o.Sender.ID)
		p, ok := people[id]
		switch {
		case !r.users[id] || !ok:
			return out, false
		case p.deleted:
			out.name = "Deleted Account"
		case p.hidesForwards:
			out.name = p.name
		default:
			out.user = id
		}
	case "hidden_user":
		out.name = o.Name
	case "channel":
		out.chat, out.post, out.name = webChat(int64(o.Chat.ID)), int64(o.Post), o.Chat.Title
	case "chat":
		out.chat, out.name = webChat(int64(o.SenderChat.ID)), o.SenderChat.Title
	}
	return out, out.user != 0 || out.chat != 0 || out.name != ""
}

// dateOr is fwd_date: NULL for no forward, the message's own date when the
// web didn't keep the original one.
func (o origin) dateOr(sent int64) any {
	switch {
	case o.user == 0 && o.chat == 0 && o.name == "":
		return nil
	case o.date == 0:
		return sent
	}
	return o.date
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// chanAlive is whether an old chat became a channel here.
func chanAlive(ch *chat, r *run) bool {
	return ch != nil && ch.kind == "group" && r.users[ch.creator]
}

// webChat is the chat id in an origin: the web's own id, or a Bot API one.
func webChat(id int64) int64 {
	if id < 0 {
		return oldChatID(id)
	}
	return id
}

// person is what forward headers need about an old account.
type person struct {
	name          string
	deleted       bool
	hidesForwards bool
}

func loadPeople(ctx context.Context, s *source) (map[int64]person, error) {
	out := map[int64]person{}
	state := s.col("u", "users", "account_state", "''")
	rows, err := s.db.QueryContext(ctx, "SELECT u.id, u.name, "+state+" FROM users u")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var p person
		var st string
		if err := rows.Scan(&id, &p.name, &st); err != nil {
			rows.Close()
			return nil, err
		}
		p.deleted = st == "deleted"
		out[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil || !s.has("account_settings") {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, "SELECT user_id, value FROM account_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var v struct {
			Privacy struct {
				Forwards *struct {
					Visibility string `json:"visibility"`
				} `json:"forwards"`
			} `json:"privacy"`
		}
		if json.Unmarshal([]byte(raw), &v) != nil || v.Privacy.Forwards == nil {
			continue
		}
		if p, ok := out[id]; ok && v.Privacy.Forwards.Visibility != "" && v.Privacy.Forwards.Visibility != "everybody" {
			p.hidesForwards = true
			out[id] = p
		}
	}
	return out, rows.Err()
}

// eachMessage walks the messages of the given chat kinds in global id
// order, which is the order they were sent in. Redacted messages (from
// erased accounts) are left out; the web showed only a placeholder.
func eachMessage(ctx context.Context, r *run, kinds string, c Counts, fn func(m *msg) error) error {
	s := r.src
	meta, reply, edited, ents := "", "NULL", "NULL", "NULL"
	if s.has("message_meta") {
		meta = " LEFT JOIN message_meta mm ON mm.message_id = m.id"
		reply, edited, ents = s.col("mm", "message_meta", "reply_to", "NULL"), s.col("mm", "message_meta", "edited_at", "NULL"), s.col("mm", "message_meta", "entities", "NULL")
	}
	fwd, originCol := "", "NULL"
	if s.has("message_forwards") {
		fwd, originCol = " LEFT JOIN message_forwards f ON f.message_id = m.id", "f.origin"
	}
	service := "0"
	if s.has("message_service_events") {
		service = "EXISTS(SELECT 1 FROM message_service_events e WHERE e.message_id = m.id)"
	}
	live := "1"
	if s.hasCol("messages", "is_redacted") {
		live = "m.is_redacted = 0"
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM messages m JOIN chats c ON c.id = m.chat_id WHERE c.kind IN ("+kinds+") AND m.is_redacted <> 0").Scan(&n); err != nil {
			return err
		}
		c["redacted"] = n
	}
	q := fmt.Sprintf(`SELECT m.id, m.chat_id, m.user_id, m.body, m.created_at, %s, %s, %s, %s, %s
		FROM messages m JOIN chats c ON c.id = m.chat_id%s%s
		WHERE c.kind IN (%s) AND %s ORDER BY m.id`, reply, edited, ents, originCol, service, meta, fwd, kinds, live)
	people, err := loadPeople(ctx, s)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var m msg
	for rows.Next() {
		var replyTo, editDate sql.NullInt64
		var entities, forward sql.NullString
		var svc int64
		if err := rows.Scan(&m.id, &m.chat, &m.from, &m.text, &m.date, &replyTo, &editDate, &entities, &forward, &svc); err != nil {
			return err
		}
		m.replyTo, m.editDate, m.service = replyTo.Int64, editDate.Int64, svc != 0
		var dropped int
		m.entities, dropped = convertEntities(entities.String)
		c["entities_dropped"] += dropped
		m.fwd = origin{}
		if forward.Valid {
			if o, ok := parseOrigin(forward.String, r, people); ok {
				m.fwd = o
			} else {
				c["forward_origins_dropped"]++
			}
		}
		if !r.users[m.from] {
			c["unknown_sender"]++
			continue
		}
		if err := fn(&m); err != nil {
			return err
		}
	}
	return rows.Err()
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

type boxKey struct{ old, owner int64 }

type dialog struct {
	top, topDate, readIn, readOut int32
	pinned                        int32
}

// importPrivate splits every saved and direct chat into per-user boxes.
// Each message gets a new id in each box it lands in; shared_id is the old
// global id, which links the copies and keeps old links resolvable.
func importPrivate(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	_, chats, err := loadChats(ctx, r)
	if err != nil {
		return nil, err
	}
	prefs, err := loadPrefs(ctx, r.src)
	if err != nil {
		return nil, err
	}
	// "delete for me" on the web hid the chat and moved clear_until up to
	// the newest message; those copies stay out of that user's box
	cleared := map[prefKey]int64{}
	if r.src.has("chat_visibility") {
		rows, err := r.src.db.QueryContext(ctx, "SELECT user_id, chat_id, clear_until FROM chat_visibility WHERE clear_until > 0")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k prefKey
			var v int64
			if err := rows.Scan(&k.user, &k.chat, &v); err != nil {
				rows.Close()
				return nil, err
			}
			cleared[k] = v
		}
		rows.Close()
	}

	next := map[int64]int32{}
	box := map[boxKey]int32{}
	dialogs := map[[2]int64]*dialog{}
	out := newBatch(w, "messages", "owner_id", "id", "peer_id", "from_id", "out", "shared_id", "date", "text", "entities",
		"reply_to", "fwd_from_id", "fwd_date", "edit_date", "fwd_from_chan", "fwd_from_name")
	mapped := newBatch(w, "import_box_messages", "old_id", "owner_id", "id")
	badChat := map[int64]bool{}
	err = eachMessage(ctx, r, "'saved', 'direct'", c, func(m *msg) error {
		ch := chats[m.chat]
		owners := ch.members
		if ch.kind == "saved" && len(owners) == 0 && r.users[ch.creator] {
			owners = []int64{ch.creator}
		}
		if ch.kind == "saved" && len(owners) != 1 || ch.kind == "direct" && (len(owners) != 2 || owners[0] == owners[1]) {
			if !badChat[ch.id] {
				badChat[ch.id] = true
				c["chats_bad_members"]++
			}
			c["skipped_bad_chat"]++
			return nil
		}
		if m.from != owners[0] && (len(owners) == 1 || m.from != owners[1]) {
			c["sender_not_member"]++
			return nil
		}
		if m.service {
			// private messages have no action column in main yet
			c["service_as_text"]++
		}
		c["messages"]++
		// the post id is the channel stage's to fill in, once posts have ids
		fwd := m.fwd
		if fwd.chat != 0 && !chanAlive(chats[fwd.chat], r) {
			fwd.chat = 0
		}
		if fwd.user == 0 && fwd.chat == 0 && fwd.name == "" && m.fwd != (origin{}) {
			c["forward_origins_dropped"]++
		}
		for i, owner := range owners {
			if cleared[prefKey{owner, ch.id}] >= m.id {
				c["cleared_copies"]++
				continue
			}
			peer := owner
			if len(owners) == 2 {
				peer = owners[1-i]
			}
			if next[owner] == 0 {
				next[owner] = 1
			}
			id := next[owner]
			next[owner]++
			var reply any
			if m.replyTo != 0 {
				if v, ok := box[boxKey{m.replyTo, owner}]; ok {
					reply = v
				} else {
					c["replies_lost"]++
				}
			}
			box[boxKey{m.id, owner}] = id
			if err := out.add(ctx, owner, id, peer, m.from, m.from == owner, m.id, int32(m.date), m.text, m.entities,
				reply, nullInt(fwd.user), fwd.dateOr(m.date), nullInt(m.editDate), nullInt(fwd.chat), nullText(fwd.name)); err != nil {
				return err
			}
			if err := mapped.add(ctx, m.id, owner, id); err != nil {
				return err
			}
			d := dialogs[[2]int64{owner, peer}]
			if d == nil {
				d = &dialog{}
				dialogs[[2]int64{owner, peer}] = d
			}
			d.top, d.topDate = id, int32(m.date)
			mine := prefs[prefKey{owner, ch.id}]
			switch {
			case owner == peer:
				d.readIn, d.readOut = id, id
			case m.id <= mine.lastRead:
				d.readIn = id
			}
			if m.from == owner && owner != peer && m.id <= prefs[prefKey{peer, ch.id}].lastRead {
				d.readOut = id
			}
			if mine.pinned {
				d.pinned = max(d.pinned, pinnedAt(ch.id))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c["copies"] = out.n
	if err := out.flush(ctx); err != nil {
		return nil, err
	}
	if err := mapped.flush(ctx); err != nil {
		return nil, err
	}

	dl := newBatch(w, "dialogs", "owner_id", "peer_id", "top_id", "top_date", "read_inbox_max", "read_outbox_max", "pinned_at")
	keys := make([][2]int64, 0, len(dialogs))
	for k := range dialogs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		d := dialogs[k]
		var pin any
		if d.pinned != 0 {
			pin = d.pinned
		}
		if err := dl.add(ctx, k[0], k[1], d.top, d.topDate, d.readIn, d.readOut, pin); err != nil {
			return nil, err
		}
	}
	c["dialogs"] = dl.n
	if err := dl.flush(ctx); err != nil {
		return nil, err
	}
	// a fresh journal: pts 0 like a new account, next_id after the copies
	boxes := newBatch(w, "user_boxes", "user_id", "next_id", "pts")
	owners := make([]int64, 0, len(next))
	for u := range next {
		owners = append(owners, u)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	for _, u := range owners {
		if err := boxes.add(ctx, u, next[u], 0); err != nil {
			return nil, err
		}
	}
	c["boxes"] = boxes.n
	return c, boxes.flush(ctx)
}

type chanPos struct {
	ch int64
	id int32
}

// importChannelMessages renumbers each group's and channel's messages from 1
// in the order they were sent, and sets top, next id and read marks.
func importChannelMessages(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	list, chats, err := loadChats(ctx, r)
	if err != nil {
		return nil, err
	}
	prefs, err := loadPrefs(ctx, r.src)
	if err != nil {
		return nil, err
	}
	pairs := func(table, a, b string) (map[int64]int64, error) {
		out := map[int64]int64{}
		if !r.src.has(table) {
			return out, nil
		}
		rows, err := r.src.db.QueryContext(ctx, "SELECT "+a+", "+b+" FROM "+table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var x, y int64
			if err := rows.Scan(&x, &y); err != nil {
				return nil, err
			}
			out[x] = y
		}
		return out, rows.Err()
	}
	// group copy of a channel post -> the post
	posts, err := pairs("discussion_posts", "group_message_id", "channel_message_id")
	if err != nil {
		return nil, err
	}
	// comment -> the post copy its thread hangs off
	threads, err := pairs("message_threads", "message_id", "root_id")
	if err != nil {
		return nil, err
	}

	alive := map[int64]bool{}
	for _, ch := range list {
		if ch.kind == "group" && r.users[ch.creator] {
			alive[ch.id] = true
		}
	}
	next := map[int64]int32{}
	pos := map[int64]chanPos{}
	sent := map[int64][]int64{} // channel -> old ids in order
	topDate := map[int64]int32{}
	out := newBatch(w, "channel_messages", "channel_id", "id", "from_id", "date", "post", "text", "entities", "action",
		"reply_to", "fwd_from_id", "fwd_date", "edit_date", "fwd_channel_id", "fwd_channel_msg", "fwd_from_chan", "fwd_from_post", "fwd_from_name")
	mapped := newBatch(w, "import_channel_messages", "old_id", "channel_id", "id")
	// a forwarded post's new id, if it came over; forwards come after their post
	postID := func(o origin) any {
		if p, ok := pos[o.post]; ok && o.post != 0 && p.ch == o.chat {
			return p.id
		}
		return nil
	}
	err = eachMessage(ctx, r, "'group'", c, func(m *msg) error {
		if !alive[m.chat] {
			c["skipped_no_channel"]++
			return nil
		}
		if next[m.chat] == 0 {
			next[m.chat] = 1
		}
		id := next[m.chat]
		next[m.chat]++
		pos[m.id] = chanPos{m.chat, id}
		sent[m.chat] = append(sent[m.chat], m.id)
		topDate[m.chat] = int32(m.date)

		var reply any
		if p, ok := pos[m.replyTo]; ok && p.ch == m.chat {
			reply = p.id
		} else if m.replyTo != 0 {
			c["replies_lost"]++
		}
		if root, ok := threads[m.id]; ok && reply == nil {
			// comments without a quote answer the post they're under
			if p, ok := pos[root]; ok && p.ch == m.chat {
				reply = p.id
			}
		}
		var fwdChan, fwdMsg any
		if post, ok := posts[m.id]; ok {
			if p, ok := pos[post]; ok {
				fwdChan, fwdMsg = p.ch, p.id
				c["discussion_copies"]++
			}
		}
		text, action := m.text, []byte(nil)
		if m.service {
			action, text = encode(&tg.MessageActionCustomAction{Message: m.text}), ""
			c["service"]++
		}
		fwd := m.fwd
		if fwd.chat != 0 && !alive[fwd.chat] {
			fwd.chat = 0
		}
		if err := out.add(ctx, m.chat, id, m.from, int32(m.date), chats[m.chat].broadcast, text, m.entities, action,
			reply, nullInt(fwd.user), fwd.dateOr(m.date), nullInt(m.editDate), fwdChan, fwdMsg, nullInt(fwd.chat), postID(fwd), nullText(fwd.name)); err != nil {
			return err
		}
		return mapped.add(ctx, m.id, m.chat, id)
	})
	if err != nil {
		return nil, err
	}
	c["messages"] = out.n
	if err := out.flush(ctx); err != nil {
		return nil, err
	}
	if err := mapped.flush(ctx); err != nil {
		return nil, err
	}
	if n, err := privatePostForwards(ctx, r, w, alive, postID); err != nil {
		return nil, err
	} else if n > 0 {
		c["private_post_forwards"] = n
	}

	if err := w.exec(ctx, `CREATE TEMP TABLE import_tops (id bigint, next_id int, top_id int, top_date int, read_outbox_max int) ON COMMIT DROP;
		CREATE TEMP TABLE import_reads (channel_id bigint, user_id bigint, read_inbox_max int) ON COMMIT DROP`); err != nil {
		return nil, err
	}
	tops := newBatch(w, "import_tops", "id", "next_id", "top_id", "top_date", "read_outbox_max")
	reads := newBatch(w, "import_reads", "channel_id", "user_id", "read_inbox_max")
	for _, ch := range list {
		if !alive[ch.id] || next[ch.id] == 0 {
			continue
		}
		olds := sent[ch.id]
		outbox := int32(0)
		for _, u := range ch.members {
			last := prefs[prefKey{u, ch.id}].lastRead
			// new ids follow old ids, so "read up to old id X" is the
			// number of messages at or below X
			n := int32(sort.Search(len(olds), func(i int) bool { return olds[i] > last }))
			if n == 0 {
				continue
			}
			outbox = max(outbox, n)
			if err := reads.add(ctx, ch.id, u, n); err != nil {
				return nil, err
			}
		}
		top := next[ch.id] - 1
		if err := tops.add(ctx, ch.id, next[ch.id], top, topDate[ch.id], outbox); err != nil {
			return nil, err
		}
	}
	if err := tops.flush(ctx); err != nil {
		return nil, err
	}
	if err := reads.flush(ctx); err != nil {
		return nil, err
	}
	c["read_marks"] = reads.n
	return c, w.exec(ctx, `UPDATE channels c SET next_id = t.next_id, top_id = t.top_id, top_date = t.top_date, read_outbox_max = t.read_outbox_max
			FROM import_tops t WHERE c.id = t.id;
		UPDATE channel_members m SET read_inbox_max = t.read_inbox_max
			FROM import_reads t WHERE m.channel_id = t.channel_id AND m.user_id = t.user_id`)
}

// privatePostForwards gives private forwards of channel posts the post's
// new id, which the private stage couldn't know yet. Keyed by shared_id
// (the old message id), so a resumed run sets the same ids again.
func privatePostForwards(ctx context.Context, r *run, w sink, alive map[int64]bool, postID func(origin) any) (int, error) {
	if !r.src.has("message_forwards") {
		return 0, nil
	}
	rows, err := r.src.db.QueryContext(ctx, `SELECT f.message_id, f.origin FROM message_forwards f
		JOIN messages m ON m.id = f.message_id JOIN chats c ON c.id = m.chat_id WHERE c.kind IN ('saved', 'direct')`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var shared []int64
	var posts []int32
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, err
		}
		o, ok := parseOrigin(raw, r, nil)
		if !ok || o.chat == 0 || !alive[o.chat] {
			continue
		}
		if p, ok := postID(o).(int32); ok {
			shared, posts = append(shared, id), append(posts, p)
		}
	}
	if err := rows.Err(); err != nil || len(shared) == 0 {
		return 0, err
	}
	return len(shared), w.exec(ctx, `UPDATE messages m SET fwd_from_post = v.post FROM unnest($1::bigint[], $2::int[]) AS v(shared, post)
		WHERE m.shared_id = v.shared AND m.fwd_from_chan IS NOT NULL`, shared, posts)
}

// finish points the sequences past the imported ids.
func finish(ctx context.Context, r *run, w sink) (Counts, error) {
	return Counts{}, w.exec(ctx, `SELECT setval(pg_get_serial_sequence('users', 'id'), (SELECT coalesce(max(id), 0) + 1 FROM users), false);
		SELECT setval('message_shared_ids', (SELECT coalesce(max(shared_id), 0) + 1 FROM messages), false);
		SELECT setval('channel_ids', greatest((SELECT coalesce(max(id), 0) + 1 FROM channels), 1000), false)`)
}
