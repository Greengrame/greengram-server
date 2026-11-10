package admin

import (
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/bots"
)

func (a *API) editBotVerifier(q *req) (any, error) {
	var id, icon int64
	var err error
	var settings tg.BotVerifierSettings
	var enabled, modify, valid bool
	var company, description string
	id, err = q.pathID("id")
	if err != nil {
		return nil, err
	}
	_, err = a.loadBot(q.ctx, a.DB, id, "")
	if err != nil {
		return nil, err
	}
	enabled, valid = q.boolean("enabled")
	if !valid {
		return nil, fail(400, "Choose whether this bot may verify peers.")
	}
	if enabled {
		icon, valid = q.int("iconCustomEmojiId")
		if !valid || icon <= 0 || a.Media == nil {
			return nil, fail(400, "Choose a verification icon.")
		}
		valid, err = a.Media.IsStatusEmoji(q.ctx, icon)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, fail(400, "Verification icon must be a custom emoji.")
		}
		company, _ = q.str("company")
		description, _ = q.str("description")
		modify, _ = q.boolean("canModifyDescription")
		if company == "" || utf8.RuneCountInString(company) > 128 || utf8.RuneCountInString(description) > 70 {
			return nil, fail(400, "Choose a company name and a description up to 70 characters.")
		}
		settings = tg.BotVerifierSettings{Icon: icon, Company: company, CanModifyCustomDescription: modify}
		settings.SetCustomDescription(tg.TextWithEntities{Text: description})
	}
	err = pgx.BeginFunc(q.ctx, a.DB, func(tx pgx.Tx) error {
		var err error
		err = bots.SetVerifierTx(q.ctx, tx, id, settings, enabled)
		if err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "admin_bot_verifier_updated", id, map[string]any{"enabled": enabled, "company": company, "icon": icon, "canModifyDescription": modify})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": enabled}, nil
}
