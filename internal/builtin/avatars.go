package builtin

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// The payment accounts' pictures, rasterized from Web A's PremiumStar.svg and
// GoldStar.svg (GPL-3.0, see third_party/telegram-tt). The built-in bots'
// pictures are Telegram's and come from the mini apps bundle instead.
//
//go:embed avatars/*.jpg
var avatars embed.FS

// SeedSystemAvatars gives the accounts that take payments their pictures, so
// the purchase dialog doesn't fall back to a letter. The old server drew these
// from Web A's PremiumStar.svg and GoldStar.svg; these are those, rasterized.
// Same rule as SeedAvatars: a picture set or hidden on purpose stays.
func SeedSystemAvatars(ctx context.Context, s *stars.Service, m *media.Service) error {
	var failed []error
	for _, sys := range []stars.System{stars.PremiumAccount, stars.StarsAccount} {
		id, err := s.SystemUser(ctx, sys)
		if err == nil {
			err = setAvatar(ctx, avatarFiles, m, id, sys.Kind+".jpg")
		}
		failed = append(failed, err)
	}
	return errors.Join(failed...)
}

// SeedAvatars gives each built-in bot its picture from dir/<kind>.jpg (the
// mini apps bundle's greengram/avatars). It runs every start because the old
// server's import carried no profile photos, and it only touches a bot that
// never had one: a picture set on purpose stays, and so does one hidden on
// purpose. A bot whose picture isn't there keeps its letter.
func SeedAvatars(ctx context.Context, dir string, b *bots.Service, m *media.Service) error {
	if dir == "" {
		return nil
	}
	var failed []error
	for _, kind := range []string{bots.BotFather, bots.Stickers, bots.Wallet, bots.Fragment, bots.BugsBot} {
		failed = append(failed, seedAvatar(ctx, os.DirFS(dir), b, m, kind))
	}
	return errors.Join(failed...)
}

func seedAvatar(ctx context.Context, dir fs.FS, b *bots.Service, m *media.Service, kind string) error {
	if _, err := fs.Stat(dir, kind+".jpg"); err != nil {
		return nil
	}
	bot, ok, err := b.ByKind(ctx, kind)
	if err != nil || !ok {
		return err
	}
	return setAvatar(ctx, dir, m, bot.UserID, kind+".jpg")
}

func setAvatar(ctx context.Context, dir fs.FS, m *media.Service, user int64, file string) error {
	had, err := m.HadUserPhoto(ctx, user)
	if err != nil || had {
		return err
	}
	raw, err := fs.ReadFile(dir, file)
	if err != nil {
		return err
	}
	p, err := m.NewPhoto(ctx, user, raw)
	if err != nil {
		return err
	}
	return m.SetUserPhoto(ctx, user, p.ID)
}

// AvatarsDir is where SeedAvatars looks in a mini apps bundle.
func AvatarsDir(miniappsDir string) string {
	if miniappsDir == "" {
		return ""
	}
	return filepath.Join(miniappsDir, "greengram", "avatars")
}

var avatarFiles, _ = fs.Sub(avatars, "avatars")
