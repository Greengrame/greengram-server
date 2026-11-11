package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/erase"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/reports"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/stories"
	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/Greengrame/greengram-server/internal/webpage"
)

// API holds what the method handlers need.
type API struct {
	srv      *edge.Server
	in       Instance
	accounts *account.Service
	msgs     *messages.Service
	chans    *channels.Service
	media    *media.Service   // set by RegisterMedia
	pages    *webpage.Service // link previews, set by RegisterPreviews
	hub      *hub
	out      outbox
	presence presence
	stars    *stars.Service                 // nil until RegisterStars
	stories  *stories.Service               // nil until RegisterStories
	reports  *reports.Service               // nil until RegisterReports
	erase    *erase.Service                 // nil until RegisterErase
	topup    atomic.Pointer[wallet.Service] // set by RegisterTopup; nil sells no Stars
	// set by Bots; nil keeps bots off
	bots   *bots.Service
	botAPI *botapi.Server

	broadcasting atomic.Bool // an announcement is going out

	countries atomic.Pointer[func(context.Context) ([]Country, error)] // set by SetCountries; the draw reads it
	langs     langpacks

	seenIP sync.Map // session hash -> last IP written to its row, see stampIP
}

// Register installs every handler on the edge.
func Register(srv *edge.Server, in Instance, accounts *account.Service, msgs *messages.Service, chans *channels.Service) *API {
	a := &API{srv: srv, in: in, accounts: accounts, msgs: msgs, chans: chans, hub: newHub(srv)}
	registerHelp(srv, in, a.quickReaction)
	a.registerAuth()
	a.registerUsers()
	a.registerAccount()
	a.registerProfile()
	a.registerPhone()
	a.registerPeople()
	a.registerHelpData()
	a.registerSaved()
	a.registerUpdates()
	a.registerMessages()
	a.registerContacts()
	a.registerChannels()
	a.registerFolders()
	a.registerTopics()
	a.registerLookups()
	a.registerReactions()
	a.registerPaidReactions()
	a.registerTodo()
	a.registerPins()
	a.registerMentions()
	a.registerDrafts()
	a.registerNotify()
	a.registerAndroid()
	return a
}

var errUnregistered = tgerr.New(401, "AUTH_KEY_UNREGISTERED")

// me is the account the calling auth key is signed in as. Telegram's
// clients answer AUTH_KEY_UNREGISTERED by showing the sign-in screen.
func (a *API) me(ctx context.Context) (account.User, error) {
	u, _, err := a.session(ctx)
	return u, err
}

func callIP(c *edge.Call) string {
	if c.Remote == nil {
		return ""
	}
	ip, _, _ := net.SplitHostPort(c.Remote.String())
	// a local peer is a proxy that didn't say who's behind it (a docker port
	// mapping, say); whatever the row has already is better than that
	if a, err := netip.ParseAddr(ip); err != nil || a.Unmap().IsPrivate() || a.Unmap().IsLoopback() {
		return ""
	}
	return ip
}

// stampIP keeps "IP" in the sessions list current. Almost every call comes
// from the address already written down, so the database hears only changes.
func (a *API) stampIP(c *edge.Call, hash string) {
	ip := callIP(c)
	if ip == "" {
		return
	}
	if last, ok := a.seenIP.Load(hash); ok && last == ip {
		return
	}
	a.seenIP.Store(hash, ip)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.accounts.StampIP(ctx, hash, ip); err != nil {
			slog.Warn("api: stamp ip", "err", err)
		}
	}()
}

// session is me plus the session hash, for calls about sessions.
func (a *API) session(ctx context.Context) (account.User, string, error) {
	c := edge.CallFrom(ctx)
	if c == nil {
		return account.User{}, "", errUnregistered
	}
	var u account.User
	var hash string
	var epoch uint64
	if s, ok := ctx.Value(meKey{}).(signedIn); ok {
		u, hash, epoch = s.u, s.hash, s.epoch
	} else {
		epoch = c.Session().Epoch() // before the lookup, see hub.join
		var ok bool
		var err error
		u, hash, ok, err = a.accounts.KeyUser(ctx, c.PermKeyID)
		if err != nil {
			slog.Error("api: key user", "err", err)
			return account.User{}, "", tgerr.New(500, "INTERNAL")
		}
		if !ok {
			return account.User{}, "", errUnregistered
		}
	}
	a.stampIP(c, hash)
	// a signed-in session that wants updates joins the user's stream
	if c.WantsUpdates() {
		a.hub.join(u.ID, c.Session(), hash, epoch)
	}
	return u, hash, nil
}

// handle registers fn for a method with its typed canonical request.
// Methods not in frozenOK refuse frozen accounts.
func handle[T bin.Object](a *API, method tlprofile.SemanticID, fn func(context.Context, T) (any, error)) {
	guard := !frozenOK[method]
	a.srv.Handle(method, func(ctx context.Context, o bin.Object) (any, error) {
		r, ok := o.(T)
		if !ok {
			return nil, fmt.Errorf("api: %T for semantic %#x", o, uint64(method))
		}
		if guard {
			var err error
			if ctx, err = a.unfrozen(ctx, r); err != nil {
				return nil, err
			}
		}
		return fn(ctx, r)
	})
}

func nowTime() time.Time { return time.Now() }
