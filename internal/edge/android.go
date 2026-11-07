package edge

import (
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
)

// The Android app (DrKLO's TLRPC, exteraless) still sends some methods with
// constructors no official layer has: its own old ids. The dispatcher asks
// adaptAndroid about any id the client's layer doesn't know, so it can only
// widen what is served: an id a layer knows never gets here, and Web A and
// the desktop apps send nothing else.
func (s *Server) adaptAndroid(v tlprofile.UnknownMethodView) (tlprofile.OutboundCall, bool, error) {
	for _, o := range []tlprofile.ClientRPCOverlay{tlprofile.ClientRPCOverlayDrkloAndroid, tlprofile.ClientRPCOverlayDrkloAndroidTheme} {
		if call, ok, err := v.AdaptClientRPCOverlay(o); ok || err != nil {
			return call, ok, err
		}
	}
	conv, ok := androidLegacy[v.WireID()]
	if !ok {
		return tlprofile.OutboundCall{}, false, nil
	}
	b, err := v.Buffer()
	if err != nil {
		return tlprofile.OutboundCall{}, true, err
	}
	if _, err := b.ID(); err != nil {
		return tlprofile.OutboundCall{}, true, err
	}
	req, err := conv(b)
	if err != nil {
		return tlprofile.OutboundCall{}, true, err
	}
	if b.Len() != 0 {
		return tlprofile.OutboundCall{}, true, fmt.Errorf("edge: %d bytes after android method %#08x", b.Len(), v.WireID())
	}
	var canonical bin.Buffer
	if err := req.Encode(&canonical); err != nil {
		return tlprofile.OutboundCall{}, true, err
	}
	call, err := v.AdaptCanonical(&canonical)
	return call, true, err
}

// androidLegacy reads the old bodies the overlay doesn't cover into today's
// request. Nested types are the same at every layer we serve.
var androidLegacy = map[uint32]func(*bin.Buffer) (bin.Object, error){
	// account.deleteAccount#418d4e0b reason:string
	0x418d4e0b: func(b *bin.Buffer) (bin.Object, error) {
		reason, err := b.String()
		return &tg.AccountDeleteAccountRequest{Reason: reason}, err
	},
	// account.uploadWallPaper#dd853661 file:InputFile mime_type:string settings:WallPaperSettings
	0xdd853661: func(b *bin.Buffer) (bin.Object, error) {
		var r tg.AccountUploadWallPaperRequest
		var err error
		if r.File, err = tg.DecodeInputFile(b); err != nil {
			return nil, err
		}
		if r.MimeType, err = b.String(); err != nil {
			return nil, err
		}
		return &r, r.Settings.Decode(b)
	},
	// channels.getFutureCreatorAfterLeave#a00918af channel:InputChannel = User
	0xa00918af: func(b *bin.Buffer) (bin.Object, error) {
		ch, err := tg.DecodeInputChannel(b)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesGetFutureChatCreatorAfterLeaveRequest{Peer: channelPeer(ch)}, nil
	},
	// messages.reportReaction#61422a48 peer:InputPeer id:int user_id:InputUser
	0x61422a48: func(b *bin.Buffer) (bin.Object, error) {
		var r tg.MessagesReportReactionRequest
		var err error
		if r.Peer, err = tg.DecodeInputPeer(b); err != nil {
			return nil, err
		}
		if r.ID, err = b.Int(); err != nil {
			return nil, err
		}
		u, err := tg.DecodeInputUser(b)
		if err != nil {
			return nil, err
		}
		r.ReactionPeer = userPeer(u)
		return &r, nil
	},
	// messages.addChatUser#f24753e3 chat_id:long user_id:InputUser fwd_limit:int
	0xf24753e3: func(b *bin.Buffer) (bin.Object, error) {
		var r tg.MessagesAddChatUserRequest
		var err error
		if r.ChatID, err = b.Long(); err != nil {
			return nil, err
		}
		if r.UserID, err = tg.DecodeInputUser(b); err != nil {
			return nil, err
		}
		r.FwdLimit, err = b.Int()
		return &r, err
	},
	// messages.getStatsURL#812c2ae6: Telegram dropped it and so did the
	// schema. The app only sends it from a stats page nothing opens.
	0x812c2ae6: func(*bin.Buffer) (bin.Object, error) {
		return nil, tgerr.New(400, "METHOD_NOT_IMPLEMENTED")
	},
}

func channelPeer(c tg.InputChannelClass) tg.InputPeerClass {
	switch v := c.(type) {
	case *tg.InputChannel:
		return &tg.InputPeerChannel{ChannelID: v.ChannelID, AccessHash: v.AccessHash}
	case *tg.InputChannelFromMessage:
		return &tg.InputPeerChannelFromMessage{Peer: v.Peer, MsgID: v.MsgID, ChannelID: v.ChannelID}
	}
	return &tg.InputPeerEmpty{}
}

func userPeer(u tg.InputUserClass) tg.InputPeerClass {
	switch v := u.(type) {
	case *tg.InputUser:
		return &tg.InputPeerUser{UserID: v.UserID, AccessHash: v.AccessHash}
	case *tg.InputUserSelf:
		return &tg.InputPeerSelf{}
	case *tg.InputUserFromMessage:
		return &tg.InputPeerUserFromMessage{Peer: v.Peer, MsgID: v.MsgID, UserID: v.UserID}
	}
	return &tg.InputPeerEmpty{}
}
