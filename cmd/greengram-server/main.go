// greengram-server is the Greengram instance: one process that speaks
// MTProto to stock Telegram clients and serves the admin API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/api"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/builtin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/config"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/erase"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/layer"
	"github.com/Greengrame/greengram-server/internal/legacy"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/miniapps"
	"github.com/Greengrame/greengram-server/internal/reports"
	instsecret "github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/settings"
	"github.com/Greengrame/greengram-server/internal/site"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/store"
	"github.com/Greengrame/greengram-server/internal/stories"
	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/Greengrame/greengram-server/internal/web"
	"github.com/Greengrame/greengram-server/internal/webpage"
)

func main() {
	if os.Getenv("GREENGRAM_DEBUG") != "" {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if err := run(); err != nil {
		slog.Error("greengram-server", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	rsaKey, err := edge.LoadRSAKey(cfg.DataDir)
	if err != nil {
		return err
	}
	secret, err := edge.LoadSecret(cfg.DataDir)
	if err != nil {
		return err
	}

	instanceKey, err := instsecret.Load(cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	set := settings.New(db.Pool)
	accounts := account.New(db.Pool, instanceKey, set)

	blobs, err := media.OpenBlobs(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	chans := channels.New(db.Pool)

	srv := edge.New(edge.Config{RSA: rsaKey, Secret: secret, Keys: db, ExchangeDC: cfg.ExchangeDC})
	a := api.Register(srv, api.Instance{
		DC:       cfg.DC,
		Host:     cfg.PublicHost,
		Port:     cfg.PublicPort,
		BaseURL:  cfg.BaseURL,
		TestMode: cfg.ExchangeDC >= 10000,
	}, accounts, messages.New(db.Pool), chans)
	mediaSvc := media.New(db.Pool, media.Config{Blobs: blobs, Parts: filepath.Join(cfg.DataDir, "uploads"), DC: cfg.DC})
	a.RegisterMedia(mediaSvc)
	a.RegisterPreviews(webpage.New(db.Pool, webpage.NewFetcher(), a.PreviewPhoto))
	starSvc := stars.New(db.Pool, set)
	a.RegisterStars(starSvc)
	a.RegisterStories(stories.New(db.Pool))
	a.RegisterReports(reports.New(db.Pool))
	eraser := erase.New(db.Pool, set)
	a.RegisterErase(eraser)
	// accounts away longer than their account.setAccountTTL period
	go eraser.Every(ctx, time.Hour)
	market := wallet.NewMarket(wallet.MarketOptions{})
	w := wallet.New(db.Pool, set, market, a.Notify).WithPhotos(miniapps.PhotoURL)
	a.RegisterTopup(w)
	frag := fragment.New(db.Pool, instanceKey, func() (string, bool) {
		q, ok := market.Read().Fresh("TON", time.Now())
		return q.Price, ok
	})
	frag.LastPrice = func() (string, bool) {
		m := market.Read()
		if _, fresh := m.Fresh("TON", time.Now()); !fresh {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				market.Refresh(ctx)
			}()
		}
		q, ok := m.Assets["TON"]
		return q.Price, ok && q.Price != ""
	}
	a.RegisterFragment(frag)
	frag.UsernamesChanged = a.UsernamesChanged
	frag.ChannelReleased = a.ChannelUsernameReleased
	botSvc := bots.New(db.Pool)
	botSvc.TokenKey = instanceKey
	botAPI, err := a.Bots(ctx, botSvc, cfg.SiteName)
	if err != nil {
		return err
	}
	if err := builtin.SeedAvatars(ctx, builtin.AvatarsDir(cfg.MiniAppsDir), botSvc, mediaSvc); err != nil {
		// not worth refusing to start over; the next start tries again
		slog.Warn("built-in bot avatars", "err", err)
	}
	if err := builtin.SeedSystemAvatars(ctx, starSvc, mediaSvc); err != nil {
		slog.Warn("payment account avatars", "err", err)
	}
	go botSvc.PruneEvery(ctx, 10*time.Minute)
	go a.Tick(ctx) // scheduled messages, polls closing on time
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				srv.Sweep(30 * time.Minute)
			}
		}
	}()

	mtLn, err := net.Listen("tcp", cfg.MTProtoAddr)
	if err != nil {
		return err
	}
	httpLn, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "database down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.Handle("/apiws", srv.WebSocket(ctx, httpLn.Addr()))
	auth := &web.Auth{Accounts: accounts, Bots: botSvc, SecureCookies: cfg.SecureCookies}
	auth.Register(mux)
	(&web.Fragment{Auth: auth, Fragment: frag}).Register(mux)
	(&web.Wallet{Auth: auth, Wallet: w, StarsChanged: a.PushStarsBalance}).Register(mux)
	(&web.Profiles{Auth: auth, Wallet: w}).Register(mux)
	auth.RegisterLinks(mux, legacy.New(db.Pool))
	mux.Handle("GET /.well-known/greengram-gateway.json", api.Discovery(cfg.DC, cfg.PublicHost, cfg.PublicPort, rsaKey))
	(&miniapps.Server{Dir: cfg.MiniAppsDir, Bots: botSvc, Accounts: accounts, Fragment: frag, Media: mediaSvc}).Register(mux)
	(&web.Broadcasts{Accounts: accounts, Bots: botSvc, Sender: a}).Register(mux)
	adm := admin.New(db.Pool, accounts, set, chans)
	adm.StarsAdjust = starSvc.Adjust
	adm.Gifts = starSvc
	adm.Media = mediaSvc
	adm.AccessChanged = a.AccessChanged
	adm.Erased = a.AccountErased
	adm.IdentityReset = a.IdentityReset
	adm.AnswerAppeal = a.AnswerAppeal
	adm.Moderator = a
	adm.Extra = append(adm.Extra, func(ctx context.Context, out map[string]any) error {
		items, err := frag.Inventory(ctx)
		out["fragmentInventory"] = items
		return err
	})
	a.SetCountries(func(ctx context.Context) ([]api.Country, error) {
		list, err := adm.Countries(ctx)
		out := make([]api.Country, 0, len(list))
		for _, c := range list {
			out = append(out, api.Country{ISO2: c.ISO2, Name: c.Name, Code: c.CountryCode, Patterns: c.Patterns})
		}
		return out, err
	})
	a.SetLangpackDir(cfg.LangpackDir)
	adm.Register(mux)
	(&site.Site{WebDir: cfg.WebDir, AdminDir: cfg.AdminDir, Admin: adm.Admin, SignedIn: auth.SignedIn,
		Info: func(ctx context.Context) site.Landing {
			l := site.Landing{Name: cfg.SiteName, Signup: true, Contact: cfg.AdminContact}
			if doc, err := set.All(ctx); err == nil {
				if v, ok := doc["siteName"].(string); ok && v != "" {
					l.Name = v
				}
				if v, ok := doc["registrationEnabled"].(bool); ok {
					l.Signup = v
				}
			}
			return l
		}}).Register(mux)
	hs := &http.Server{Handler: botAPI.Wrap(adm.Count(mux)), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shut)
	}()
	go func() {
		if err := hs.Serve(httpLn); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http", "err", err)
			stop()
		}
	}()

	slog.Info("listening", "mtproto", mtLn.Addr().String(), "http", httpLn.Addr().String(),
		"dc", cfg.DC, "exchange_dc", cfg.ExchangeDC, "layers", [2]int{layer.Oldest, layer.Newest})
	return srv.Serve(ctx, mtLn)
}
