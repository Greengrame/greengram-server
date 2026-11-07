package api

import (
	"context"
	"crypto/rand"
	"encoding/binary"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// RegisterMedia installs uploads, downloads, media messages, photos and
// stickers. It's separate from Register so the media store can be set up
// on its own (blob directory, DC).
func (a *API) RegisterMedia(m *media.Service) {
	a.media = m
	handle(a, tlprofile.SemanticMethodUploadSaveFilePart, a.saveFilePart)
	handle(a, tlprofile.SemanticMethodUploadSaveBigFilePart, a.saveBigFilePart)
	handle(a, tlprofile.SemanticMethodUploadGetFile, a.getFile)
	handle(a, tlprofile.SemanticMethodUploadGetFileHashes, a.getFileHashes)
	handle(a, tlprofile.SemanticMethodMessagesSendMedia, a.sendMedia)
	handle(a, tlprofile.SemanticMethodMessagesSendMultiMedia, a.sendMultiMedia)
	handle(a, tlprofile.SemanticMethodMessagesUploadMedia, a.uploadMedia)
	handle(a, tlprofile.SemanticMethodMessagesGetSearchCounters, a.searchCounters)
	handle(a, tlprofile.SemanticMethodChannelsEditPhoto, a.editChannelPhoto)
	a.registerPhotos()
	a.registerStickers()
	a.registerPolls()
	a.registerSavedDocs()
	a.registerScheduled()
}

var errPartsInvalid = tgerr.New(400, "FILE_PARTS_INVALID")

func (a *API) saveFilePart(ctx context.Context, r *tg.UploadSaveFilePartRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	return true, a.media.SavePart(ctx, me.ID, r.FileID, r.FilePart, 0, r.Bytes)
}

func (a *API) saveBigFilePart(ctx context.Context, r *tg.UploadSaveBigFilePartRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.FileTotalParts < 1 {
		return nil, errPartsInvalid
	}
	return true, a.media.SavePart(ctx, me.ID, r.FileID, r.FilePart, r.FileTotalParts, r.Bytes)
}

// locate finds the blob behind a file location. Peer photos have no access
// hash, so whose photo it is gets checked here.
func (a *API) locate(ctx context.Context, me account.User, loc tg.InputFileLocationClass) (media.File, error) {
	v, ok := loc.(*tg.InputPeerPhotoFileLocation)
	if !ok {
		return a.media.Locate(ctx, loc)
	}
	invalid := tgerr.New(400, "LOCATION_INVALID")
	to, err := a.peer(ctx, me, v.Peer)
	if err != nil {
		return media.File{}, invalid
	}
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil || c.PhotoID != v.PhotoID {
			return media.File{}, invalid
		}
	} else if ok, err := a.media.IsUserPhoto(ctx, to.user, v.PhotoID); err != nil || !ok || !a.photoVisible(ctx, to.user, me.ID) {
		return media.File{}, invalid
	}
	p, ok, err := a.media.Photo(ctx, v.PhotoID)
	if err != nil || !ok {
		return media.File{}, invalid
	}
	// Telegram's peer photo sizes are 160 and 640
	z := p.Fit(160)
	if v.Big {
		z = p.Fit(640)
	}
	return p.File(z), nil
}

func (a *API) getFile(ctx context.Context, r *tg.UploadGetFileRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	f, err := a.locate(ctx, me, r.Location)
	if err != nil {
		return nil, err
	}
	return a.media.Read(ctx, f, r.Offset, r.Limit, r.Precise)
}

func (a *API) getFileHashes(ctx context.Context, r *tg.UploadGetFileHashesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	f, err := a.locate(ctx, me, r.Location)
	if err != nil {
		return nil, err
	}
	return a.media.Hashes(ctx, f, r.Offset)
}

func (a *API) sendMedia(ctx context.Context, r *tg.MessagesSendMediaRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if wp, ok := r.Media.(*tg.InputMediaWebPage); ok && a.pages != nil {
		return a.sendWebPage(ctx, me, to, r, wp)
	}
	// before FromInput puts the upload together and stores it
	if err := a.mayWrite(ctx, me, to, true); err != nil {
		return nil, err
	}
	ref, err := a.mediaFromInput(ctx, me, r.Media)
	if err != nil {
		return nil, err
	}
	reply, topic := 0, 0
	if r.ReplyTo != nil {
		reply, topic = replyID(r.ReplyTo), replyTopic(r.ReplyTo)
	}
	a.presence.set(me.ID, true)
	out, err := a.sendOrSchedule(ctx, me, to, []outgoing{{text: r.Message, entities: r.Entities, replyTo: reply, topic: topic, randomID: r.RandomID, media: ref, silent: r.Silent}},
		r.ScheduleDate, r.ScheduleRepeatPeriod)
	if err == nil {
		a.noteSent(ctx, me, ref)
		if r.ClearDraft {
			a.dropSentDraft(ctx, me, to, r.ReplyTo)
		}
	}
	return out, err
}

// mediaFromInput is media.FromInput plus what needs accounts: a shared
// contact links to the account with that phone number, as in Telegram.
func (a *API) mediaFromInput(ctx context.Context, me account.User, in tg.InputMediaClass) (media.Ref, error) {
	// a Premium feature in Telegram too
	if _, ok := in.(*tg.InputMediaTodo); ok && !me.Premium(nowTime()) {
		return media.Ref{}, tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED")
	}
	ref, err := a.media.FromInput(ctx, me.ID, in)
	if err != nil {
		return ref, err
	}
	if c, ok := ref.TL.(*tg.MessageMediaContact); ok && c.PhoneNumber != "" {
		if u, found, err := a.accounts.UserByPhone(ctx, c.PhoneNumber); err == nil && found && !u.IsSystem {
			c.UserID = clientID(u.ID)
		}
	}
	return ref, nil
}

// groupedID ties an album's messages together; any random number will do.
func groupedID() int64 {
	var b [8]byte
	rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1)
}

// sendMultiMedia is an album. The apps upload each item with uploadMedia
// first, but uploaded files are taken too.
func (a *API) sendMultiMedia(ctx context.Context, r *tg.MessagesSendMultiMediaRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	switch n := len(r.MultiMedia); {
	case n == 0:
		return nil, tgerr.New(400, "MEDIA_EMPTY")
	case n > 10:
		return nil, tgerr.New(400, "MULTI_MEDIA_TOO_LONG")
	}
	reply, topic := 0, 0
	if r.ReplyTo != nil {
		reply, topic = replyID(r.ReplyTo), replyTopic(r.ReplyTo)
	}
	if err := a.mayWrite(ctx, me, to, true); err != nil {
		return nil, err
	}
	var group int64
	if len(r.MultiMedia) > 1 {
		group = groupedID()
	}
	list := make([]outgoing, 0, len(r.MultiMedia))
	for _, it := range r.MultiMedia {
		ref, err := a.mediaFromInput(ctx, me, it.Media)
		if err != nil {
			return nil, err
		}
		list = append(list, outgoing{text: it.Message, entities: it.Entities, replyTo: reply, topic: topic, randomID: it.RandomID, media: ref, grouped: group, silent: r.Silent})
	}
	a.presence.set(me.ID, true)
	out, err := a.sendOrSchedule(ctx, me, to, list, r.ScheduleDate, 0)
	if err == nil && r.ClearDraft {
		a.dropSentDraft(ctx, me, to, r.ReplyTo)
	}
	return out, err
}

func (a *API) uploadMedia(ctx context.Context, r *tg.MessagesUploadMediaRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if err := a.mayWrite(ctx, me, to, true); err != nil {
		return nil, err
	}
	ref, err := a.media.FromInput(ctx, me.ID, r.Media)
	if err != nil {
		return nil, err
	}
	return ref.TL, nil
}

// filterKinds maps a search filter to media kinds. ok is false for filters
// that can't match anything here yet (links, mentions, geo...).
func filterKinds(f tg.MessagesFilterClass) (kinds []string, ok bool) {
	switch f.(type) {
	case nil, *tg.InputMessagesFilterEmpty:
		return nil, true
	case *tg.InputMessagesFilterPhotos:
		return []string{media.KindPhoto}, true
	case *tg.InputMessagesFilterVideo:
		return []string{media.KindVideo}, true
	case *tg.InputMessagesFilterPhotoVideo:
		return []string{media.KindPhoto, media.KindVideo}, true
	case *tg.InputMessagesFilterDocument:
		return []string{media.KindFile}, true
	case *tg.InputMessagesFilterMusic:
		return []string{media.KindAudio}, true
	case *tg.InputMessagesFilterVoice:
		return []string{media.KindVoice}, true
	case *tg.InputMessagesFilterRoundVideo:
		return []string{media.KindRound}, true
	case *tg.InputMessagesFilterRoundVoice:
		return []string{media.KindVoice, media.KindRound}, true
	case *tg.InputMessagesFilterGif:
		return []string{media.KindGIF}, true
	case *tg.InputMessagesFilterURL:
		return []string{messages.KindLink}, true
	case *tg.InputMessagesFilterPoll:
		return []string{media.KindPoll}, true
	case *tg.InputMessagesFilterGeo:
		return []string{media.KindGeo}, true
	case *tg.InputMessagesFilterContacts:
		return []string{media.KindContact}, true
	}
	return nil, false
}

// searchCounters fills the shared-media tab counts.
func (a *API) searchCounters(ctx context.Context, r *tg.MessagesGetSearchCountersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	topic := 0
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		topic = searchTopic(c, r.TopMsgID)
	}
	out := make([]tg.MessagesSearchCounter, len(r.Filters))
	for i, f := range r.Filters {
		out[i] = tg.MessagesSearchCounter{Filter: f}
		kinds, ok := filterKinds(f)
		if !ok || len(kinds) == 0 {
			continue
		}
		var n int
		if to.channel != 0 {
			_, n, err = a.chans.SearchTopic(ctx, to.channel, topic, "", 0, 1, kinds...)
		} else {
			_, n, err = a.msgs.Search(ctx, me.ID, to.user, "", 0, 1, kinds...)
		}
		if err != nil {
			return nil, err
		}
		out[i].Count = n
	}
	return out, nil
}

func (a *API) editChannelPhoto(ctx context.Context, r *tg.ChannelsEditPhotoRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	var (
		photoID int64
		photo   tg.PhotoClass
	)
	switch v := r.Photo.(type) {
	case *tg.InputChatUploadedPhoto:
		// video avatars need a transcoder we don't have
		f, ok := v.GetFile()
		if !ok {
			return nil, tgerr.New(400, "PHOTO_FILE_MISSING")
		}
		p, err := a.media.UploadPhoto(ctx, me.ID, f)
		if err != nil {
			return nil, err
		}
		photoID, photo = p.ID, a.media.PhotoTL(p)
	case *tg.InputChatPhoto:
		p, err := a.media.InputPhoto(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		photoID, photo = p.ID, a.media.PhotoTL(p)
	}
	events, err := a.chans.EditPhoto(ctx, me.ID, id, photoID, photo)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

// chatPhoto is the small photo object chats and users carry.
func (a *API) chatPhoto(id int64) *tg.ChatPhoto {
	return &tg.ChatPhoto{PhotoID: id, DCID: a.in.DC}
}

// fullPhoto is the whole photo, for getFullChannel and getFullUser.
func (a *API) fullPhoto(ctx context.Context, id int64) tg.PhotoClass {
	if id == 0 || a.media == nil {
		return &tg.PhotoEmpty{}
	}
	p, ok, err := a.media.Photo(ctx, id)
	if err != nil || !ok {
		return &tg.PhotoEmpty{ID: id}
	}
	return a.media.PhotoTL(p)
}
