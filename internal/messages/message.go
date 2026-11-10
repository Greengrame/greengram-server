package messages

import (
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
)

// Msg is one row: a message as one box sees it.
type Msg struct {
	Topic    int
	Owner    int64
	ID       int
	Peer     int64
	From     int64
	Out      bool
	Shared   int64
	Date     int
	Text     string
	Entities []tg.MessageEntityClass
	ReplyTo  int
	Fwd      Origin
	EditDate int
	Media    media.Ref
	Grouped  int64 // album
	// a bot's keyboard; nil for people
	ReplyMarkup tg.ReplyMarkupClass
	Pinned      bool
	Silent      bool
	// set on a service message (a gift, a pin); Text then only feeds search
	Action tg.MessageActionClass
	// an article: encoded richMessage, Text is its plain words
	Rich  []byte
	Quote []byte
	// sent through this bot's inline mode
	ViaBot int64
}

const msgCols = "owner_id, id, peer_id, from_id, out, shared_id, date, text, entities, reply_to, fwd_from_id, fwd_date, edit_date, media, media_kind, media_id, coalesce(grouped_id, 0), reply_markup, " +
	"coalesce(fwd_from_chan, 0), coalesce(fwd_from_post, 0), coalesce(fwd_from_name, ''), pinned, silent, action, rich, coalesce(via_bot_id, 0), reply_quote, bot_topic_id"

func scanMsg(row pgx.Row) (Msg, error) {
	var (
		m        Msg
		ent      []byte
		reply    *int
		fwdFrom  *int64
		fwdDate  *int
		editDate *int
		raw      []byte
		kind     *string
		mediaID  *int64
		markup   []byte
		action   []byte
	)
	if err := row.Scan(&m.Owner, &m.ID, &m.Peer, &m.From, &m.Out, &m.Shared, &m.Date, &m.Text, &ent, &reply, &fwdFrom, &fwdDate, &editDate,
		&raw, &kind, &mediaID, &m.Grouped, &markup, &m.Fwd.Chan, &m.Fwd.Post, &m.Fwd.Name, &m.Pinned, &m.Silent, &action, &m.Rich, &m.ViaBot, &m.Quote, &m.Topic); err != nil {
		return m, err
	}
	if reply != nil {
		m.ReplyTo = *reply
	}
	if fwdFrom != nil {
		m.Fwd.User = *fwdFrom
	}
	if fwdDate != nil {
		m.Fwd.Date = *fwdDate
	}
	if editDate != nil {
		m.EditDate = *editDate
	}
	var err error
	if m.Entities, err = decodeEntities(ent); err != nil {
		return m, err
	}
	if m.ReplyMarkup, err = DecodeMarkup(markup); err != nil {
		return m, err
	}
	if m.Media, err = media.Load(raw, kind, mediaID); err != nil {
		return m, err
	}
	if len(action) > 0 {
		m.Action, err = tg.DecodeMessageAction(&bin.Buffer{Buf: action})
	}
	return m, err
}

func collect(rows pgx.Rows) ([]Msg, error) {
	defer rows.Close()
	var out []Msg
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func encodeEntities(list []tg.MessageEntityClass) ([]byte, error) {
	if len(list) == 0 {
		return nil, nil
	}
	var b bin.Buffer
	b.PutVectorHeader(len(list))
	for _, e := range list {
		if err := e.Encode(&b); err != nil {
			return nil, err
		}
	}
	return b.Buf, nil
}

func decodeEntities(raw []byte) ([]tg.MessageEntityClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: raw}
	n, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	out := make([]tg.MessageEntityClass, 0, n)
	for range n {
		e, err := tg.DecodeMessageEntity(b)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func encodeAction(a tg.MessageActionClass) ([]byte, error) {
	if a == nil {
		return nil, nil
	}
	var b bin.Buffer
	if err := a.Encode(&b); err != nil {
		return nil, err
	}
	return b.Buf, nil
}

// TL is the message as its owner's clients see it.
func (m Msg) TL() tg.MessageClass {
	if m.Action != nil {
		s := &tg.MessageService{ID: m.ID, Out: m.Out, PeerID: &tg.PeerUser{UserID: ids.Client(m.Peer)}, Date: m.Date, Action: m.Action, Silent: m.Silent}
		s.SetFromID(&tg.PeerUser{UserID: ids.Client(m.From)})
		if m.ReplyTo != 0 || m.Topic != 0 && m.ID != m.Topic {
			s.SetReplyTo(m.privateReplyHeader())
		}
		if p, ok := m.SavedPeer(); ok {
			s.SetSavedPeerID(SavedPeerTL(p, m.Owner))
		}
		return s
	}
	out := &tg.Message{
		Noforwards: media.WidgetKey(m.Media) != "",
		Pinned:     m.Pinned,
		Silent:     m.Silent,
		ID:         m.ID,
		Out:        m.Out,
		FromID:     &tg.PeerUser{UserID: ids.Client(m.From)},
		PeerID:     &tg.PeerUser{UserID: ids.Client(m.Peer)},
		Date:       m.Date,
		Message:    m.Text,
	}
	if m.ViaBot != 0 {
		out.SetViaBotID(ids.Client(m.ViaBot))
	}
	if len(m.Entities) > 0 {
		out.SetEntities(m.Entities)
	}
	if m.ReplyTo != 0 || m.Topic != 0 && m.ID != m.Topic {
		out.SetReplyTo(m.privateReplyHeader())
	}
	if h, ok := m.Fwd.TL(); ok {
		out.SetFwdFrom(h)
	}
	if m.EditDate != 0 {
		out.SetEditDate(m.EditDate)
	}
	if !m.Media.Empty() {
		out.SetMedia(m.Media.TL)
	}
	if m.Grouped != 0 {
		out.SetGroupedID(m.Grouped)
	}
	if m.ReplyMarkup != nil {
		out.SetReplyMarkup(m.ReplyMarkup)
	}
	if r, ok := media.DecodeRich(m.Rich); ok {
		out.SetRichMessage(r)
	}
	if p, ok := m.SavedPeer(); ok {
		out.SetSavedPeerID(SavedPeerTL(p, m.Owner))
	}
	return out
}

// HiddenAuthor is the saved dialog of forwards that only carry a name;
// clients know Telegram's id for it.
const HiddenAuthor = 2666000

// SavedPeer is which saved dialog a Saved Messages message belongs to,
// the same rule as the saved_peer column (0132): a user id, -channel id,
// or 0 for a hidden author. ok is false outside Saved Messages.
func (m Msg) SavedPeer() (int64, bool) {
	switch {
	case m.Owner != m.Peer:
		return 0, false
	case m.Fwd.Chan != 0:
		return -m.Fwd.Chan, true
	case m.Fwd.User != 0:
		return m.Fwd.User, true
	case m.Fwd.Name != "":
		return 0, true
	}
	return m.Owner, true
}

// SavedPeerTL is a saved_peer value as a client peer.
func SavedPeerTL(p, owner int64) tg.PeerClass {
	switch {
	case p < 0:
		return &tg.PeerChannel{ChannelID: -p}
	case p == 0:
		return &tg.PeerUser{UserID: HiddenAuthor}
	}
	return &tg.PeerUser{UserID: ids.Client(p)}
}

// Users are the ids a client needs user objects for to show these messages.
func Users(list []Msg) []int64 {
	seen := map[int64]bool{}
	var out []int64
	add := func(id int64) {
		if id != 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, m := range list {
		add(m.From)
		add(m.Peer)
		add(m.Fwd.User)
		add(m.ViaBot)
		for _, id := range media.RichUsers(m.Rich) {
			add(ids.Server(id))
		}
		for _, id := range actionUsers(m.Action) {
			add(ids.Server(id))
		}
	}
	return out
}

// actionUsers are the people a gift's service message names: who sent it,
// who holds the collectible and who its original details mention. Web A
// draws the gift sheet's From and Owner rows only for peers it has.
func actionUsers(a tg.MessageActionClass) []int64 {
	var out []int64
	user := func(p tg.PeerClass) {
		if u, ok := p.(*tg.PeerUser); ok {
			out = append(out, u.UserID)
		}
	}
	switch v := a.(type) {
	case *tg.MessageActionStarGift:
		user(v.FromID)
		user(v.Peer)
	case *tg.MessageActionStarGiftUnique:
		user(v.FromID)
		user(v.Peer)
		if g, ok := v.Gift.(*tg.StarGiftUnique); ok {
			user(g.OwnerID)
			for _, at := range g.Attributes {
				if od, ok := at.(*tg.StarGiftAttributeOriginalDetails); ok {
					user(od.SenderID)
					user(od.RecipientID)
				}
			}
		}
	}
	return out
}

// Origin is where a forward came from: a user, a channel post, or only a
// name when the author's forwards privacy hides the link. Zero is not a
// forward.
type Origin struct {
	User int64
	Chan int64
	Post int // the post in Chan; 0 for a group message
	Name string
	Date int
}

func (o Origin) Empty() bool { return o.User == 0 && o.Chan == 0 && o.Name == "" }

func (o Origin) TL() (tg.MessageFwdHeader, bool) {
	var h tg.MessageFwdHeader
	switch {
	case o.Chan != 0:
		h.SetFromID(&tg.PeerChannel{ChannelID: o.Chan})
		if o.Post != 0 {
			h.SetChannelPost(o.Post)
		}
	case o.User != 0:
		h.SetFromID(&tg.PeerUser{UserID: ids.Client(o.User)})
	case o.Name != "":
		h.SetFromName(o.Name)
	default:
		return h, false
	}
	h.Date = o.Date
	return h, true
}
