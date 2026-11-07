package miniapps

import (
	"errors"
	"strings"
	"testing"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
)

// Every page has to render: a template only fails when it runs.
func TestBotfatherPagesRender(t *testing.T) {
	s := &Server{Dir: testBundle(t)}
	b := bots.Bot{Menu: &bots.MenuButton{Text: "Open", URL: "https://example.org"}, MainAppURL: "https://example.org"}
	v := botView{ID: "5", Name: `<b>Bot</b>`, Username: "x_bot", Avatar: botAvatar(5, "Bot"), Host: "example.org/u/", Token: "5:abc", Bot: b, MainMode: bots.ModeCompact,
		Commands:      []bots.Command{{Command: "help", Description: "Help"}},
		GroupRights:   rightRows(bots.GroupRightNames, groupRightLabels, []string{"other"}),
		ChannelRights: rightRows(bots.ChannelRightNames, channelRightLabels, nil),
		Providers:     paymentProviders, Provider: paymentProviders[0]}
	for _, name := range []string{"bot", "profile", "usernames", "commands", "commandcreate", "apps", "menuapp", "mainapp", "settings", "inline", "login", "cloud", "games", "payments", "provider", "stars", "unavailable"} {
		out, err := s.renderBotPage(name, v)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if strings.Contains(out, "<b>Bot</b>") || strings.Contains(out, "ZgotmplZ") {
			t.Errorf("%s: bad value in the page: %s", name, out)
		}
	}
	out, err := s.renderBotPage("list", []botRow{{ID: "5", Name: "Bot", Username: "x_bot", Avatar: botAvatar(5, "Bot")}})
	if err != nil || !strings.Contains(out, "@x_bot") || strings.Contains(out, "ZgotmplZ") {
		t.Errorf("list: %v %s", err, out)
	}
	if out, err := s.renderBotPage("create", map[string]string{"Host": "example.org/u/"}); err != nil || !strings.Contains(out, "example.org/u/") {
		t.Errorf("create: %v %s", err, out)
	}
	if len(bots.GroupRightNames) != 13 || len(bots.ChannelRightNames) != 13 {
		t.Error("the page says 13 rights each")
	}
	for _, n := range bots.GroupRightNames {
		if groupRightLabels[n] == "" {
			t.Errorf("group right %s has no label", n)
		}
	}
	for _, n := range bots.ChannelRightNames {
		if channelRightLabels[n] == "" {
			t.Errorf("channel right %s has no label", n)
		}
	}
}

func TestSetCommand(t *testing.T) {
	list := []bots.Command{{Command: "a", Description: "A"}, {Command: "b", Description: "B"}}
	out, _, err := setCommand(list, "c", "C", "")
	if err != nil || len(out) != 3 || out[2].Command != "c" {
		t.Fatalf("add: %v %v", out, err)
	}
	out, _, err = setCommand(list, "z", "Z", "a")
	if err != nil || out[0].Command != "z" || out[1].Command != "b" || list[0].Command != "a" {
		t.Fatalf("replace: %v %v", out, err)
	}
	if out, _, err = setCommand(list, "a", "new text", "a"); err != nil || out[0].Description != "new text" {
		t.Fatalf("same name: %v %v", out, err)
	}
	for _, c := range [][3]string{{"b", "dup", ""}, {"b", "dup", "a"}, {"Bad", "x", ""}, {"ok", "", ""}, {"ok", "x", "nope"}} {
		if _, _, err := setCommand(list, c[0], c[1], c[2]); err == nil {
			t.Errorf("%v went through", c)
		}
	}
}

func TestOrderCommands(t *testing.T) {
	list := []bots.Command{{Command: "a", Description: "A"}, {Command: "b", Description: "B"}, {Command: "c", Description: "C"}}
	out, err := orderCommands(list, []any{"c", "a"})
	if err != nil || len(out) != 2 || out[0].Description != "C" || out[1].Description != "A" {
		t.Fatalf("%v %v", out, err)
	}
	if _, err := orderCommands(list, []any{"x"}); err == nil {
		t.Fatal("unknown command went through")
	}
	if out, err := orderCommands(list, nil); err != nil || out == nil || len(out) != 0 {
		t.Fatalf("empty: %v %v", out, err)
	}
}

// Without the bundle the pages answer with what to install, not a crash.
func TestBotfatherPagesMissing(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		s := &Server{Dir: dir}
		_, err := s.renderBotPage("list", []botRow{})
		var refusal *account.Error
		if !errors.As(err, &refusal) || refusal.Status != 503 || !strings.Contains(refusal.Message, "ggminiapps install") {
			t.Errorf("%q: %v", dir, err)
		}
	}
}
