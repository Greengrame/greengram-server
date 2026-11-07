package wallet

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/Greengrame/greengram-server/internal/account"
)

// The languages the captured wallet ships a translation chunk for
// (earlyLangpack's list, native names from each chunk's language_dialog).
// zh_HANT is what the real API calls the app's zh-TW.
var waltLanguages = []struct{ Code, Name, Native string }{
	{"en", "English", "English"},
	{"ru", "Russian", "Русский"},
	{"am", "Amharic", "አማርኛ"},
	{"ar", "Arabic", "العربية"},
	{"bn", "Bengali", "বাংলা"},
	{"zh_HANT", "Chinese (Traditional)", "中文（繁體）"},
	{"de", "German", "Deutsch"},
	{"hi", "Hindi", "हिंदी"},
	{"id", "Indonesian", "Indonesia"},
	{"it", "Italian", "Italiano"},
	{"kk", "Kazakh", "Қазақ тілі"},
	{"ko", "Korean", "한국어"},
	{"pt", "Portuguese (Brazil)", "Português (Brasil)"},
	{"es", "Spanish", "Español"},
	{"uk", "Ukrainian", "Українська"},
	{"uz", "Uzbek", "O’zbekcha"},
	{"vi", "Vietnamese", "Tiếng Việt"},
}

func waltLanguage(code string) bool {
	for _, l := range waltLanguages {
		if l.Code == code {
			return true
		}
	}
	return false
}

// waltLanguageFor maps a client's language code onto the wallet's list,
// "" when it has none. Regional forms share the base translation, except
// Chinese: only Traditional exists, and Simplified readers shouldn't get it.
func waltLanguageFor(code string) string {
	code = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(code)), "_", "-")
	switch code {
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return "zh_HANT"
	}
	base, _, _ := strings.Cut(code, "-")
	if base != "zh" && waltLanguage(base) {
		return base
	}
	return ""
}

type launchLanguageKey struct{}

// WithLaunchData remembers the language in a mini app's signed launch data:
// the one the wallet follows until its own setting is used.
func WithLaunchData(ctx context.Context, raw string) context.Context {
	if raw == "" {
		return ctx
	}
	fields, err := url.ParseQuery(raw)
	if err != nil {
		return ctx
	}
	var who struct {
		LanguageCode string `json:"language_code"`
	}
	if json.Unmarshal([]byte(fields.Get("user")), &who) != nil {
		return ctx
	}
	return context.WithValue(ctx, launchLanguageKey{}, who.LanguageCode)
}

// language is what the app runs in: its own setting, else the opening app's.
func (s *Service) language(ctx context.Context, user int64) string {
	var saved string
	err := s.db.QueryRow(ctx, "SELECT language FROM wallet_prefs WHERE user_id=$1", user).Scan(&saved)
	if err == nil && waltLanguage(saved) {
		return saved
	}
	launch, _ := ctx.Value(launchLanguageKey{}).(string)
	if l := waltLanguageFor(launch); l != "" {
		return l
	}
	return "en"
}

func (s *Service) setLanguage(ctx context.Context, u account.User, code string) error {
	if !waltLanguage(code) {
		return fail(400, "This language is unavailable.")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO wallet_prefs(user_id, language) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET language = excluded.language`, u.ID, code)
	return err
}

func availableLanguages() []map[string]string {
	out := make([]map[string]string, 0, len(waltLanguages))
	for _, l := range waltLanguages {
		out = append(out, map[string]string{"code": l.Code, "name": l.Name, "translation": l.Native})
	}
	return out
}
