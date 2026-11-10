package builtin

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/bots"
)

// dialog is where a BotFather conversation stands.
type dialog struct {
	cmd  string // what we're doing: newbot, token, setname...
	step string // what the next message is: name, username, bot, value, confirm
	name string
	bot  bots.Bot
}

const help = `I can help you create and manage Telegram bots. If you're new to the Bot API, please see the manual.

You can control me by sending these commands:

/newbot - create a new bot
/mybots - edit your bots

Edit Bots
/settopics - enable or disable private bot topics
/setsecondaryusername - set a Premium secondary username
/setname - change a bot's name
/setdescription - change bot description
/setabouttext - change bot about info
/setcommands - change the list of commands
/deletebot - delete a bot

Bot Settings
/token - generate authorization token
/revoke - revoke bot access token

/cancel - cancel the current operation`

const deleteConfirm = "Yes, I am totally sure."

func (r *runner) dialog(user int64) *dialog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chats[user]
}

func (r *runner) setDialog(user int64, d *dialog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d == nil {
		delete(r.chats, user)
		return
	}
	r.chats[user] = d
}

func (r *runner) text(ctx context.Context, user int64, text string) error {
	return r.say(ctx, user, botapi.Out{Text: text})
}

// code marks the first sub in text as code, for tokens.
func code(text, sub string) []tg.MessageEntityClass {
	i := strings.Index(text, sub)
	if i < 0 {
		return nil
	}
	off := len(utf16.Encode([]rune(text[:i])))
	return []tg.MessageEntityClass{&tg.MessageEntityCode{Offset: off, Length: len(utf16.Encode([]rune(sub)))}}
}

func hide() tg.ReplyMarkupClass { return &tg.ReplyKeyboardHide{} }

// command splits "/token@BotFather arg" into "token".
func command(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	c, _, _ := strings.Cut(text[1:], " ")
	c, _, _ = strings.Cut(c, "@")
	return strings.ToLower(c), true
}

func (r *runner) botfather(ctx context.Context, user int64, text string) error {
	if c, ok := command(text); ok {
		return r.start(ctx, user, c)
	}
	d := r.dialog(user)
	if d == nil {
		return r.say(ctx, user, botapi.Out{Text: "Unrecognized command. Say what?"})
	}
	switch d.step {
	case "bot":
		b, ok, err := r.Bots.OwnedByUsername(ctx, user, text)
		if err != nil {
			return err
		}
		if !ok {
			return r.text(ctx, user, "Invalid bot selected.")
		}
		d.bot = b
		return r.chosen(ctx, user, d)
	case "name":
		if strings.TrimSpace(text) == "" || len([]rune(text)) > 64 {
			return r.text(ctx, user, "Sorry, this name can't be used. Use 1-64 characters.")
		}
		d.name, d.step = strings.TrimSpace(text), "username"
		return r.text(ctx, user, "Good. Now let's choose a username for your bot. It must end in `bot`. Like this, for example: TetrisBot or tetris_bot.")
	case "username":
		b, token, err := r.Bots.Create(ctx, user, d.name, text)
		if refused(err) {
			return r.text(ctx, user, err.Error())
		}
		if err != nil {
			return err
		}
		r.setDialog(user, nil)
		msg := "Done! Congratulations on your new bot. You will find it at @" + b.Username +
			". You can now add a description, about section and profile picture for your bot, see /help for a list of commands.\n\n" +
			"Use this token to access the HTTP API:\n" + token + "\nKeep your token secure and store it safely, it can be used by anyone to control your bot.\n\n" +
			"For a description of the Bot API, see this page: https://core.telegram.org/bots/api"
		return r.say(ctx, user, botapi.Out{Text: msg, Entities: code(msg, token)})
	case "value":
		return r.value(ctx, user, d, text)
	case "confirm":
		if text != deleteConfirm {
			return r.say(ctx, user, botapi.Out{Text: "Please enter the confirmation text exactly like this:\n" + deleteConfirm + "\n\nType /cancel to cancel the operation."})
		}
		if err := r.Bots.Delete(ctx, d.bot.UserID); err != nil {
			return err
		}
		r.setDialog(user, nil)
		return r.say(ctx, user, botapi.Out{Text: "Done! The bot is gone. /help", Markup: hide()})
	}
	return nil
}

func refused(err error) bool {
	var e *bots.Error
	return errors.As(err, &e)
}

// start begins a command; the ones about one bot ask which first.
func (r *runner) start(ctx context.Context, user int64, c string) error {
	switch c {
	case "start", "help":
		r.setDialog(user, nil)
		return r.say(ctx, user, botapi.Out{Text: help, Markup: r.openButton("Open BotFather")})
	case "cancel":
		if r.dialog(user) == nil {
			return r.say(ctx, user, botapi.Out{Text: "No active command to cancel. I wasn't doing anything anyway. Zzzzz...", Markup: hide()})
		}
		r.setDialog(user, nil)
		return r.say(ctx, user, botapi.Out{Text: "The command has been cancelled. Anything else I can do for you?\n\nSend /help for a list of commands.", Markup: hide()})
	case "newbot":
		r.setDialog(user, &dialog{cmd: c, step: "name"})
		return r.text(ctx, user, "Alright, a new bot. How are we going to call it? Please choose a name for your bot.")
	case "mybots":
		return r.myBots(ctx, user)
	case "token", "revoke", "setsecondaryusername", "settopics", "setname", "setdescription", "setabouttext", "setcommands", "deletebot":
		return r.choose(ctx, user, &dialog{cmd: c, step: "bot"})
	}
	return r.text(ctx, user, "Unrecognized command. Say what?")
}

// choose asks for one of the person's bots with a keyboard of them.
func (r *runner) choose(ctx context.Context, user int64, d *dialog) error {
	list, err := r.Bots.Owned(ctx, user)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		r.setDialog(user, nil)
		return r.text(ctx, user, "You have currently no bots. Use the /newbot command to create a new bot first.")
	}
	kb := &tg.ReplyKeyboardMarkup{Resize: true, SingleUse: true}
	for i := 0; i < len(list); i += 2 {
		row := tg.KeyboardButtonRow{}
		for _, b := range list[i:min(i+2, len(list))] {
			row.Buttons = append(row.Buttons, tg.KeyboardButton{Text: "@" + b.Username, Type: &tg.ButtonTypeDefault{}})
		}
		kb.Rows = append(kb.Rows, row)
	}
	r.setDialog(user, d)
	return r.say(ctx, user, botapi.Out{Text: "Choose a bot.", Markup: kb})
}

// chosen goes on once the bot is known.
func (r *runner) chosen(ctx context.Context, user int64, d *dialog) error {
	at := "@" + d.bot.Username
	switch d.cmd {
	case "settopics":
		d.step = "value"
		return r.say(ctx, user, botapi.Out{Text: "Send ON to enable private bot topics or OFF to disable them.", Markup: hide()})
	case "setsecondaryusername":
		d.step = "value"
		return r.say(ctx, user, botapi.Out{Text: "Send a secondary username ending with agent, ai, app, game, or gay. Premium owners can assign names to up to two bots. Send /empty to remove the name.", Markup: hide()})
	case "token", "revoke":
		return r.revoke(ctx, user, d.bot)
	case "deletebot":
		d.step = "confirm"
		return r.say(ctx, user, botapi.Out{Text: "OK, you selected " + at + ". Are you sure?\n\nSend '" + deleteConfirm + "' to confirm you really want to delete this bot.", Markup: hide()})
	case "setname":
		d.step = "value"
		return r.say(ctx, user, botapi.Out{Text: "OK. Send me the new name for your bot.", Markup: hide()})
	case "setdescription":
		d.step = "value"
		return r.say(ctx, user, botapi.Out{Text: "OK. Send me the new description for the bot. People will see this description when they open a chat with your bot, in a block titled 'What can this bot do?'.", Markup: hide()})
	case "setabouttext":
		d.step = "value"
		return r.say(ctx, user, botapi.Out{Text: "OK. Send me the new 'About' text. People will see this text on the bot's profile page. 120 characters at most.", Markup: hide()})
	case "setcommands":
		d.step = "value"
		return r.say(ctx, user, botapi.Out{Text: "OK. Send me a list of commands for your bot. Please use this format:\n\ncommand1 - Description\ncommand2 - Another description", Markup: hide()})
	}
	return nil
}

func (r *runner) revoke(ctx context.Context, user int64, b bots.Bot) error {
	token, err := r.Bots.Revoke(ctx, b.UserID)
	if err != nil {
		return err
	}
	r.setDialog(user, nil)
	msg := "Your token was replaced with a new one. You can use this token to access HTTP API:\n" + token + "\n\nFor a description of the Bot API, see this page: https://core.telegram.org/bots/api"
	return r.say(ctx, user, botapi.Out{Text: msg, Entities: code(msg, token), Markup: hide()})
}

func (r *runner) value(ctx context.Context, user int64, d *dialog, text string) error {
	var err error
	what := ""
	switch d.cmd {
	case "settopics":
		text = strings.ToLower(strings.TrimSpace(text))
		if text != "on" && text != "off" {
			return r.text(ctx, user, "Send ON or OFF.")
		}
		what = "Topic mode"
		err = r.Bots.SetSetting(ctx, d.bot.UserID, "topics", text == "on")
	case "setsecondaryusername":
		if strings.TrimSpace(text) == "/empty" {
			text = ""
		}
		what = "Secondary username"
		err = r.Bots.SetSecondaryUsername(ctx, user, d.bot.UserID, text)
	case "setname":
		if strings.TrimSpace(text) == "" || len([]rune(text)) > 64 {
			return r.text(ctx, user, "Sorry, this name can't be used. Use 1-64 characters.")
		}
		what = "Name"
		err = r.Bots.SetName(ctx, d.bot.UserID, strings.TrimSpace(text))
	case "setdescription":
		if len([]rune(text)) > 512 {
			return r.text(ctx, user, "Sorry, the description is too long. 512 characters at most.")
		}
		what = "Description"
		err = r.Bots.SetDescription(ctx, d.bot.UserID, text)
	case "setabouttext":
		if len([]rune(text)) > 120 {
			return r.text(ctx, user, "Sorry, the about info is too long. 120 characters at most.")
		}
		what = "About section"
		err = r.Bots.SetAbout(ctx, d.bot.UserID, text)
	case "setcommands":
		list, ok := parseCommands(text)
		if !ok {
			return r.text(ctx, user, "Sorry, the list of commands is invalid. Please use this format:\n\ncommand1 - Description\ncommand2 - Another description\n\nSend /empty to keep the list empty.")
		}
		what = "Command list"
		err = r.Bots.SetCommands(ctx, d.bot.UserID, list)
	}
	if err != nil {
		return err
	}
	r.setDialog(user, nil)
	return r.text(ctx, user, "Success! "+what+" updated. /help")
}

// parseCommands reads "cmd - description" lines, as BotFather does.
func parseCommands(text string) ([]bots.Command, bool) {
	var out []bots.Command
	if strings.TrimSpace(text) == "/empty" {
		return out, true
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		c, desc, ok := strings.Cut(line, "-")
		if !ok {
			return nil, false
		}
		out = append(out, bots.Command{Command: strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "/")), Description: strings.TrimSpace(desc)})
	}
	return out, len(out) > 0 && botapi.ParseCommands(out) == nil
}

// myBots lists the person's bots as buttons; a press opens one.
func (r *runner) myBots(ctx context.Context, user int64) error {
	list, err := r.Bots.Owned(ctx, user)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return r.text(ctx, user, "You have currently no bots. Use the /newbot command to create a new bot first.")
	}
	kb := &tg.ReplyInlineMarkup{}
	for i := 0; i < len(list); i += 2 {
		row := tg.KeyboardInlineButtonRow{}
		for _, b := range list[i:min(i+2, len(list))] {
			row.Buttons = append(row.Buttons, tg.KeyboardInlineButton{Text: "@" + b.Username,
				Type: &tg.InlineButtonTypeCallback{Data: []byte("bot:" + strconv.FormatInt(b.UserID, 10))}})
		}
		kb.Rows = append(kb.Rows, row)
	}
	return r.say(ctx, user, botapi.Out{Text: "Choose a bot from the list below:", Markup: kb})
}

func button(text, data string) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{Text: text, Type: &tg.InlineButtonTypeCallback{Data: []byte(data)}}
}

func (r *runner) botfatherPress(ctx context.Context, cb bots.Callback) error {
	if err := r.Bots.Answer(ctx, r.bot.UserID, cb.ID, "", false, "", 0); err != nil && !errors.Is(err, bots.ErrQueryInvalid) {
		return err
	}
	act, rest, _ := strings.Cut(string(cb.Data), ":")
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return nil
	}
	b, ok, err := r.Bots.Get(ctx, id)
	if err != nil || !ok || b.OwnerID != cb.User || b.Disabled {
		return err
	}
	switch act {
	case "bot":
		kb := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{
			{Buttons: []tg.KeyboardInlineButton{button("Revoke token", "rev:"+rest), button("Delete Bot", "del:"+rest)}},
		}}
		return r.say(ctx, cb.User, botapi.Out{Text: "Here it is: " + b.Name + " @" + b.Username + ".\nWhat do you want to do with the bot?", Markup: kb})
	case "rev":
		return r.revoke(ctx, cb.User, b)
	case "del":
		d := &dialog{cmd: "deletebot", bot: b}
		r.setDialog(cb.User, d)
		return r.chosen(ctx, cb.User, d)
	}
	return nil
}
