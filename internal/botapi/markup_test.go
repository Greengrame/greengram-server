package botapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func parse(t *testing.T, raw string) (tg.ReplyMarkupClass, error) {
	t.Helper()
	var m apiMarkup
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return tgMarkup(m)
}

func TestInlineKeyboard(t *testing.T) {
	m, err := parse(t, `{"inline_keyboard":[[{"text":"A","callback_data":"a"},{"text":"Site","url":"https://example.com"}],[{"text":"App","web_app":{"url":"https://app.example"}}]]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !HasCallback(m, []byte("a")) || HasCallback(m, []byte("b")) {
		t.Fatal("callback lookup")
	}
	back := apiInlineMarkup(m)
	raw, _ := json.Marshal(back)
	want := `{"inline_keyboard":[[{"text":"A","callback_data":"a"},{"text":"Site","url":"https://example.com"}],[{"text":"App","web_app":{"url":"https://app.example"}}]]}`
	if string(raw) != want {
		t.Fatalf("round trip\n got %s\nwant %s", raw, want)
	}
}

func TestKeyboardRefusals(t *testing.T) {
	for _, raw := range []string{
		`{"inline_keyboard":[[{"text":"A"}]]}`,
		`{"inline_keyboard":[[{"text":"A","callback_data":"` + strings.Repeat("x", 65) + `"}]]}`,
		`{"inline_keyboard":[[{"text":"A","url":"javascript:alert(1)"}]]}`,
		`{}`,
	} {
		if _, err := parse(t, raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestReplyKeyboard(t *testing.T) {
	m, err := parse(t, `{"keyboard":[["yes","no"],[{"text":"phone","request_contact":true}]],"resize_keyboard":true,"one_time_keyboard":true}`)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := m.(*tg.ReplyKeyboardMarkup)
	if !ok || !kb.Resize || !kb.SingleUse || len(kb.Rows) != 2 || kb.Rows[0].Buttons[1].Text != "no" {
		t.Fatalf("keyboard %+v", m)
	}
	if _, ok := kb.Rows[1].Buttons[0].Type.(*tg.ButtonTypeRequestPhone); !ok {
		t.Fatalf("contact button %+v", kb.Rows[1].Buttons[0])
	}
	if apiInlineMarkup(m) != nil {
		t.Fatal("reply keyboards aren't shown back to bots")
	}
	if m, _ := parse(t, `{"remove_keyboard":true}`); m == nil {
		t.Fatal("remove_keyboard")
	}
}

func TestCommandEntity(t *testing.T) {
	got := withCommand("/start@echo_bot hi", nil)
	if len(got) != 1 || got[0].GetLength() != len("/start@echo_bot") {
		t.Fatalf("entities %+v", got)
	}
	if got := withCommand("hello", nil); len(got) != 0 {
		t.Fatalf("plain text got %+v", got)
	}
}
