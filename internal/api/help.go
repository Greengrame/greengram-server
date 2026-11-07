// Package api holds the RPC method handlers.
package api

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/edge"
)

type Instance struct {
	DC      int
	Host    string // public address clients dial; must resolve to an IP
	Port    int
	BaseURL string // https://chat.example.org
	// TestMode: the instance takes clients on Telegram's test backend (our
	// Android build). help.getConfig only says so to a key made that way,
	// Web A shows "[T]" and test-only options for it.
	TestMode bool
}

// noBot breaks the username rules, so it never resolves. Telegram's @gif,
// @pic and @foursquare don't exist here, and an empty value would make the
// client fall back to them.
const noBot = "-"

func RegisterHelp(s *edge.Server, in Instance) { registerHelp(s, in, nil) }

// quickReaction is the signed-in user's own choice, nil for the default.
func registerHelp(s *edge.Server, in Instance, quickReaction func(context.Context) tg.ReactionClass) {
	s.Handle(tlprofile.SemanticMethodHelpGetConfig, func(ctx context.Context, _ bin.Object) (any, error) {
		cfg := in.config()
		if c := edge.CallFrom(ctx); c != nil && c.TestBackend != nil {
			cfg.TestMode = *c.TestBackend
		}
		if quickReaction != nil {
			if r := quickReaction(ctx); r != nil {
				cfg.ReactionsDefault = r
			}
		}
		return cfg, nil
	})
	s.Handle(tlprofile.SemanticMethodHelpGetNearestDC, func(ctx context.Context, _ bin.Object) (any, error) {
		return &tg.NearestDC{Country: "XX", ThisDC: in.DC, NearestDC: in.DC}, nil
	})
}

func (in Instance) config() *tg.Config {
	now := time.Now()
	return &tg.Config{
		Date:     int(now.Unix()),
		Expires:  int(now.Add(time.Hour).Unix()),
		TestMode: in.TestMode,
		ThisDC:   in.DC,
		DCOptions: []tg.DCOption{
			{ID: in.DC, IPAddress: publicIP(in.Host), Port: in.Port, Static: true},
		},
		DCTxtDomainName:         "localhost",
		ChatSizeMax:             200,
		MegagroupSizeMax:        200000,
		ForwardedCountMax:       100,
		OnlineUpdatePeriodMs:    210000,
		OfflineBlurTimeoutMs:    5000,
		OfflineIdleTimeoutMs:    30000,
		OnlineCloudTimeoutMs:    300000,
		NotifyCloudDelayMs:      30000,
		NotifyDefaultDelayMs:    1500,
		PushChatPeriodMs:        60000,
		PushChatLimit:           2,
		EditTimeLimit:           1 << 30,
		RevokeTimeLimit:         1 << 30,
		RevokePmTimeLimit:       1 << 30,
		RevokePmInbox:           true,
		RatingEDecay:            2419200,
		StickersRecentLimit:     200,
		ChannelsReadMediaPeriod: 604800,
		CallReceiveTimeoutMs:    20000,
		CallRingTimeoutMs:       90000,
		CallConnectTimeoutMs:    30000,
		CallPacketTimeoutMs:     10000,
		MeURLPrefix:             in.BaseURL + "/u/",
		CaptionLengthMax:        4096,
		MessageLengthMax:        4096,
		WebfileDCID:             in.DC,
		ReactionsDefault:        &tg.ReactionEmoji{Emoticon: "👍"},
		GifSearchUsername:       noBot,
		ImgSearchUsername:       noBot,
		VenueSearchUsername:     noBot,
	}
}

// The Android client's native layer only dials addresses; a name in
// dc_options replaces the working address it got at login with one it
// can't use.
//
// Every connection asks for the config, and Docker's resolver now and then
// eats a query, which held help.getConfig for Go's 5s retry. So the answer
// is kept for ipTTL and refreshed in the background; only the very first
// call waits on DNS.
func publicIP(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	ipCache.mu.Lock()
	e, ok := ipCache.m[host]
	stale := !ok || time.Since(e.at) > ipTTL
	if ok && stale && !e.refreshing {
		e.refreshing = true
		ipCache.m[host] = e
		go lookupPublicIP(host)
	}
	ipCache.mu.Unlock()
	if ok {
		return e.ip
	}
	return lookupPublicIP(host)
}

const ipTTL = 10 * time.Minute

type ipEntry struct {
	ip         string
	at         time.Time
	refreshing bool
}

var ipCache = struct {
	mu sync.Mutex
	m  map[string]ipEntry
}{m: map[string]ipEntry{}}

func lookupPublicIP(host string) string {
	ip := host
	if ips, err := net.LookupIP(host); err == nil {
		for _, a := range ips {
			if a.To4() != nil {
				ip = a.String()
				break
			}
		}
	}
	ipCache.mu.Lock()
	defer ipCache.mu.Unlock()
	if ip == host {
		// keep the last good address, try again next call
		if e, ok := ipCache.m[host]; ok {
			e.refreshing = false
			ipCache.m[host] = e
			return e.ip
		}
		return host
	}
	ipCache.m[host] = ipEntry{ip: ip, at: time.Now()}
	return ip
}
