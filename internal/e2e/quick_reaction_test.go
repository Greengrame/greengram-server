package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// The quick reaction comes back with the config, also on a later sign-in.
func TestQuickReaction(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	emoji := func(c *tg.Client, ctx context.Context) (string, error) {
		cfg, err := c.HelpGetConfig(ctx)
		if err != nil {
			return "", err
		}
		r, ok := cfg.ReactionsDefault.(*tg.ReactionEmoji)
		if !ok {
			return "", fmt.Errorf("reaction %T", cfg.ReactionsDefault)
		}
		return r.Emoticon, nil
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		if got, err := emoji(c, ctx); err != nil || got != "👍" {
			return fmt.Errorf("default %q, %v", got, err)
		}
		if _, err := c.MessagesSetDefaultReaction(ctx, &tg.ReactionEmoji{Emoticon: "🔥"}); err != nil {
			return err
		}
		return nil
	})
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		if got, err := emoji(c, ctx); err != nil || got != "🔥" {
			return fmt.Errorf("after setting %q, %v", got, err)
		}
		return nil
	})
}
