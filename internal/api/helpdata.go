package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
)

func init() {
	for _, m := range []tlprofile.SemanticID{
		tlprofile.SemanticMethodHelpGetTimezonesList,
		tlprofile.SemanticMethodHelpAcceptTermsOfService,
		tlprofile.SemanticMethodHelpDismissSuggestion,
		tlprofile.SemanticMethodCommunitiesGetJoinedCommunities,
		tlprofile.SemanticMethodAicomposeGetTones,
		tlprofile.SemanticMethodPaymentsGetStarGiftActiveAuctions,
		tlprofile.SemanticMethodLangpackGetLanguages,
		tlprofile.SemanticMethodLangpackGetLanguage,
		tlprofile.SemanticMethodLangpackGetLangPack,
		tlprofile.SemanticMethodLangpackGetDifference,
		tlprofile.SemanticMethodLangpackGetStrings,
	} {
		frozenOK[m] = true
	}
}

// Country is one entry of the phone picker.
type Country struct {
	ISO2, Name, Code string
	Patterns         []string
}

// SetCountries hands help.getCountriesList the instance's list (the admin
// panel edits it; /login shows the same one).
func (a *API) SetCountries(fn func(context.Context) ([]Country, error)) { a.countries.Store(&fn) }

// countryList is SetCountries' list, none without it.
func (a *API) countryList(ctx context.Context) ([]Country, error) {
	if f := a.countries.Load(); f != nil {
		return (*f)(ctx)
	}
	return nil, nil
}

// timezones.json is gramsrv's internal/seed/catalog/timezones.json:
// Telegram's help.getTimezonesList answer.
//
//go:embed timezones.json
var timezonesJSON []byte

var timezones = sync.OnceValue(func() *tg.HelpTimezonesList {
	var doc struct {
		Timezones []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Offset int    `json:"utc_offset"`
		} `json:"timezones"`
	}
	if err := json.Unmarshal(timezonesJSON, &doc); err != nil {
		panic(err)
	}
	out := &tg.HelpTimezonesList{Timezones: make([]tg.Timezone, 0, len(doc.Timezones))}
	for _, t := range doc.Timezones {
		out.Timezones = append(out.Timezones, tg.Timezone{ID: t.ID, Name: t.Name, UtcOffset: t.Offset})
	}
	out.Hash = int(hash32(timezonesJSON))
	return out
})

func hash32(b []byte) int32 {
	h := fnv.New32a()
	h.Write(b)
	return int32(h.Sum32() >> 1)
}

func (a *API) registerHelpData() {
	handle(a, tlprofile.SemanticMethodHelpGetTimezonesList, func(_ context.Context, r *tg.HelpGetTimezonesListRequest) (any, error) {
		t := timezones()
		if r.Hash == t.Hash {
			return &tg.HelpTimezonesListNotModified{}, nil
		}
		// a copy: td sets flags while encoding and replies run in parallel
		cp := *t
		return &cp, nil
	})
	handle(a, tlprofile.SemanticMethodHelpGetCountriesList, func(ctx context.Context, r *tg.HelpGetCountriesListRequest) (any, error) {
		out := &tg.HelpCountriesList{Countries: []tg.HelpCountry{}, Hash: 1}
		if a.countries.Load() == nil {
			return out, nil
		}
		list, err := a.countryList(ctx)
		if err != nil {
			return nil, err
		}
		h := fnv.New32a()
		for _, c := range list {
			h.Write([]byte(c.ISO2 + c.Code + c.Name + "\x00"))
			out.Countries = append(out.Countries, tg.HelpCountry{ISO2: c.ISO2, DefaultName: c.Name,
				CountryCodes: []tg.HelpCountryCode{{CountryCode: c.Code, Patterns: c.Patterns}}})
		}
		out.Hash = int(h.Sum32() >> 1)
		if r.Hash == out.Hash {
			return &tg.HelpCountriesListNotModified{}, nil
		}
		return out, nil
	})
	handle(a, tlprofile.SemanticMethodHelpAcceptTermsOfService, func(context.Context, *tg.HelpAcceptTermsOfServiceRequest) (any, error) {
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodHelpDismissSuggestion, func(context.Context, *tg.HelpDismissSuggestionRequest) (any, error) {
		return true, nil
	})

	// asked at every start; nothing behind them here (no communities, AI
	// compose tones or gift auctions), so the empty answers are the true ones
	handle(a, tlprofile.SemanticMethodCommunitiesGetJoinedCommunities, func(context.Context, *tg.CommunitiesGetJoinedCommunitiesRequest) (any, error) {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAicomposeGetTones, func(context.Context, *tg.AicomposeGetTonesRequest) (any, error) {
		return &tg.AicomposeTones{Tones: []tg.AiComposeToneClass{}, Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodPaymentsGetStarGiftActiveAuctions, func(context.Context, *tg.PaymentsGetStarGiftActiveAuctionsRequest) (any, error) {
		return &tg.PaymentsStarGiftActiveAuctions{Auctions: []tg.StarGiftActiveAuctionState{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}, nil
	})

	handle(a, tlprofile.SemanticMethodLangpackGetLanguages, func(_ context.Context, r *tg.LangpackGetLanguagesRequest) (any, error) {
		return a.langs.pack(r.LangPack).languages(), nil
	})
	handle(a, tlprofile.SemanticMethodLangpackGetLanguage, func(_ context.Context, r *tg.LangpackGetLanguageRequest) (any, error) {
		for _, l := range a.langs.pack(r.LangPack).languages() {
			if l.LangCode == normLang(r.LangCode) {
				return &l, nil
			}
		}
		return nil, tgerr.New(400, "LANG_CODE_NOT_SUPPORTED")
	})
	handle(a, tlprofile.SemanticMethodLangpackGetLangPack, func(_ context.Context, r *tg.LangpackGetLangPackRequest) (any, error) {
		return a.langs.pack(r.LangPack).difference(r.LangCode, 0)
	})
	handle(a, tlprofile.SemanticMethodLangpackGetDifference, func(_ context.Context, r *tg.LangpackGetDifferenceRequest) (any, error) {
		return a.langs.pack(r.LangPack).difference(r.LangCode, r.FromVersion)
	})
	handle(a, tlprofile.SemanticMethodLangpackGetStrings, func(_ context.Context, r *tg.LangpackGetStringsRequest) (any, error) {
		l, err := a.langs.pack(r.LangPack).load(r.LangCode)
		if err != nil {
			return nil, err
		}
		want := map[string]bool{}
		for _, k := range r.Keys {
			want[k] = true
		}
		out := []tg.LangPackStringClass{}
		for _, s := range l.strings {
			if want[langKey(s)] {
				out = append(out, cloneStrings([]tg.LangPackStringClass{s})...)
			}
		}
		return out, nil
	})
}

// Language packs come from GREENGRAM_LANGPACK_DIR: <pack>/<pack>_<code>_v<version>.strings,
// optionally described by official-language-packs.json (gramsrv's layout, see
// docs/languages.md). A file the index doesn't know is picked up as it is.
// Without any, every pack is English only and empty, and the apps use the
// strings they ship with.
type langpacks struct {
	dir string

	mu    sync.Mutex
	packs map[string]*langpack
}

type langpack struct {
	name  string
	dir   string
	langs []langMeta

	mu     sync.Mutex
	loaded map[string]*langStrings
}

type langMeta struct {
	Code       string `json:"lang_code"`
	Name       string `json:"name"`
	NativeName string `json:"native_name"`
	Plural     string `json:"plural_code"`
	Official   bool   `json:"official"`
	RTL        bool   `json:"rtl"`
	Count      int    `json:"strings_count"`
	Translated int    `json:"translated_count"`
	URL        string `json:"translations_url"`
	Version    int    `json:"version"`
	File       string `json:"file"`
}

type langStrings struct {
	version int
	strings []tg.LangPackStringClass
}

var english = langMeta{Code: "en", Name: "English", NativeName: "English", Plural: "en", Official: true}

func (p *langpacks) pack(name string) *langpack {
	name = strings.ToLower(strings.TrimSpace(name))
	p.mu.Lock()
	defer p.mu.Unlock()
	if lp, ok := p.packs[name]; ok {
		return lp
	}
	if p.packs == nil {
		p.packs = map[string]*langpack{}
	}
	lp := &langpack{name: name, dir: p.dir, loaded: map[string]*langStrings{}}
	if p.dir != "" && name != "" {
		lp.langs = addLangFiles(p.dir, name, readLangIndex(p.dir, name))
	}
	if len(lp.langs) == 0 {
		lp.langs = []langMeta{english}
	}
	// English first, then by name: the order the clients show them in
	sort.SliceStable(lp.langs, func(i, j int) bool {
		if a, b := lp.langs[i].Code == "en", lp.langs[j].Code == "en"; a != b {
			return a
		}
		return lp.langs[i].Name < lp.langs[j].Name
	})
	p.packs[name] = lp
	return lp
}

func readLangIndex(dir, name string) []langMeta {
	raw, err := os.ReadFile(filepath.Join(dir, "official-language-packs.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		slog.Warn("api: langpack index", "err", err)
		return nil
	}
	var doc struct {
		Packs []struct {
			Name      string     `json:"name"`
			Languages []langMeta `json:"languages"`
		} `json:"packs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		slog.Warn("api: langpack index", "err", err)
		return nil
	}
	for _, p := range doc.Packs {
		if p.Name == name {
			return p.Languages
		}
	}
	return nil
}

// addLangFiles lays the pack's files over the index: a language it doesn't
// list, or a higher version than it knows, wins. That's how dropping a
// file in adds or updates a language; the server reads them at start.
func addLangFiles(dir, name string, langs []langMeta) []langMeta {
	files, err := os.ReadDir(filepath.Join(dir, name))
	if err != nil {
		return langs
	}
	known := map[string]int{}
	total := 0
	for i, m := range langs {
		known[m.Code] = i
		total = max(total, m.Count)
	}
	for _, f := range files {
		rest, ok := strings.CutPrefix(f.Name(), name+"_")
		m := langFileRe.FindStringSubmatch(rest)
		if !ok || m == nil {
			continue
		}
		code := normLang(m[1])
		version, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		i, ok := known[code]
		if ok && (langs[i].Version > version || (langs[i].Version == version && langs[i].File != "")) {
			continue
		}
		if !ok {
			i = len(langs)
			known[code] = i
			langs = append(langs, describeLang(code))
		}
		raw, err := os.ReadFile(filepath.Join(dir, name, f.Name()))
		if err != nil {
			slog.Warn("api: langpack", "pack", name, "file", f.Name(), "err", err)
			continue
		}
		langs[i].Version, langs[i].File = version, name+"/"+f.Name()
		langs[i].Translated = len(parseStrings(raw))
		total = max(total, langs[i].Translated)
	}
	for i := range langs {
		if langs[i].Count == 0 || langs[i].Count < langs[i].Translated {
			langs[i].Count = total
		}
	}
	return langs
}

var langFileRe = regexp.MustCompile(`^([A-Za-z0-9_-]+)_v(\d+)\.strings$`)

// describeLang fills in what a bare file can't say.
func describeLang(code string) langMeta {
	base, _, _ := strings.Cut(code, "-")
	m := langMeta{Code: code, Name: code, NativeName: code, Plural: base, Official: true}
	if n, ok := langNames[code]; ok {
		m.Name, m.NativeName = n[0], n[1]
	}
	switch base {
	case "ar", "fa", "he", "ur", "ps", "ku", "sd", "yi":
		m.RTL = true
	}
	return m
}

var langNames = map[string][2]string{
	"am": {"Amharic", "አማርኛ"}, "ar": {"Arabic", "العربية"}, "az": {"Azerbaijani", "Azərbaycan"},
	"be": {"Belarusian", "Беларуская"}, "bg": {"Bulgarian", "Български"}, "bn": {"Bengali", "বাংলা"},
	"ca": {"Catalan", "Català"}, "cs": {"Czech", "Čeština"}, "da": {"Danish", "Dansk"},
	"de": {"German", "Deutsch"}, "el": {"Greek", "Ελληνικά"}, "en": {"English", "English"},
	"es": {"Spanish", "Español"}, "et": {"Estonian", "Eesti"}, "fa": {"Persian", "فارسی"},
	"fi": {"Finnish", "Suomi"}, "fr": {"French", "Français"}, "he": {"Hebrew", "עברית"},
	"hi": {"Hindi", "हिन्दी"}, "hr": {"Croatian", "Hrvatski"}, "hu": {"Hungarian", "Magyar"},
	"hy": {"Armenian", "Հայերեն"}, "id": {"Indonesian", "Bahasa Indonesia"}, "it": {"Italian", "Italiano"},
	"ja": {"Japanese", "日本語"}, "ka": {"Georgian", "ქართული"}, "kk": {"Kazakh", "Қазақша"},
	"ko": {"Korean", "한국어"}, "lt": {"Lithuanian", "Lietuvių"}, "lv": {"Latvian", "Latviešu"},
	"ms": {"Malay", "Bahasa Melayu"}, "nb": {"Norwegian (Bokmål)", "Norsk (Bokmål)"}, "nl": {"Dutch", "Nederlands"},
	"pl": {"Polish", "Polski"}, "pt": {"Portuguese", "Português"}, "pt-br": {"Portuguese (Brazil)", "Português (Brasil)"},
	"ro": {"Romanian", "Română"}, "ru": {"Russian", "Русский"}, "sk": {"Slovak", "Slovenčina"},
	"sl": {"Slovenian", "Slovenščina"}, "sr": {"Serbian", "Српски"}, "sv": {"Swedish", "Svenska"},
	"th": {"Thai", "ไทย"}, "tr": {"Turkish", "Türkçe"}, "uk": {"Ukrainian", "Українська"},
	"uz": {"Uzbek", "O‘zbek"}, "vi": {"Vietnamese", "Tiếng Việt"},
	"zh-hans": {"Chinese (Simplified)", "简体中文"}, "zh-hant": {"Chinese (Traditional)", "繁體中文"},
}

// clientLanguage is the language the calling client said it runs in.
func clientLanguage(ctx context.Context) string {
	if c := edge.CallFrom(ctx); c != nil {
		return normLang(c.Client.LangCode)
	}
	return "en"
}

func normLang(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "_", "-")
	if code == "" {
		return "en"
	}
	return strings.TrimSuffix(code, "-raw")
}

func (lp *langpack) languages() []tg.LangPackLanguage {
	out := make([]tg.LangPackLanguage, 0, len(lp.langs))
	for _, m := range lp.langs {
		out = append(out, tg.LangPackLanguage{Official: m.Official, Rtl: m.RTL, Name: m.Name, NativeName: m.NativeName,
			LangCode: m.Code, PluralCode: m.Plural, StringsCount: m.Count, TranslatedCount: m.Translated, TranslationsURL: m.URL})
	}
	return out
}

func (lp *langpack) meta(code string) (langMeta, bool) {
	for _, m := range lp.langs {
		if m.Code == code {
			return m, true
		}
	}
	return langMeta{}, false
}

var errLangCode = tgerr.New(400, "LANG_CODE_NOT_SUPPORTED")

// load reads and caches one language's strings.
func (lp *langpack) load(code string) (*langStrings, error) {
	code = normLang(code)
	m, ok := lp.meta(code)
	if !ok {
		return nil, errLangCode
	}
	lp.mu.Lock()
	defer lp.mu.Unlock()
	if l, ok := lp.loaded[code]; ok {
		return l, nil
	}
	l := &langStrings{version: m.Version, strings: []tg.LangPackStringClass{}}
	if m.File != "" {
		raw, err := os.ReadFile(filepath.Join(lp.dir, filepath.FromSlash(m.File)))
		if err != nil {
			slog.Warn("api: langpack", "pack", lp.name, "lang", code, "err", err)
		} else {
			l.strings = parseStrings(raw)
		}
	}
	lp.loaded[code] = l
	return l, nil
}

// difference answers getLangPack (from 0) and getDifference. There is one
// version per language here, so it's everything or nothing. from_version has to
// be the asked one even so: Web A refetches forever when it doesn't match.
func (lp *langpack) difference(code string, from int) (*tg.LangPackDifference, error) {
	l, err := lp.load(code)
	if err != nil {
		return nil, err
	}
	out := &tg.LangPackDifference{LangCode: normLang(code), FromVersion: from, Version: l.version, Strings: []tg.LangPackStringClass{}}
	if from < l.version || l.version == 0 {
		out.Strings = cloneStrings(l.strings)
	}
	return out, nil
}

var stringRe = regexp.MustCompile(`(?s)"((?:\\.|[^"\\])*)"\s*=\s*"((?:\\.|[^"\\])*)";`)

// parseStrings reads the .strings format: "Key" = "Value"; with plural
// forms as "Key#one", "Key#other" and so on.
func parseStrings(raw []byte) []tg.LangPackStringClass {
	var out []tg.LangPackStringClass
	plural := map[string]*tg.LangPackStringPluralized{}
	for _, m := range stringRe.FindAllSubmatch(raw, -1) {
		key, val := unescape(string(m[1])), unescape(string(m[2]))
		base, form, ok := strings.Cut(key, "#")
		if !ok {
			out = append(out, &tg.LangPackString{Key: key, Value: val})
			continue
		}
		p := plural[base]
		if p == nil {
			p = &tg.LangPackStringPluralized{Key: base}
			plural[base] = p
			out = append(out, p)
		}
		switch form {
		case "zero":
			p.SetZeroValue(val)
		case "one":
			p.SetOneValue(val)
		case "two":
			p.SetTwoValue(val)
		case "few":
			p.SetFewValue(val)
		case "many":
			p.SetManyValue(val)
		default:
			p.OtherValue = val
		}
	}
	return out
}

func unescape(s string) string {
	if v, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return v
	}
	return s
}

// cloneStrings copies the cached strings for one reply: td writes the
// flags of pluralized ones while encoding, and replies encode in parallel.
func cloneStrings(list []tg.LangPackStringClass) []tg.LangPackStringClass {
	out := make([]tg.LangPackStringClass, len(list))
	for i, s := range list {
		switch v := s.(type) {
		case *tg.LangPackStringPluralized:
			cp := *v
			out[i] = &cp
		case *tg.LangPackString:
			cp := *v
			out[i] = &cp
		default:
			out[i] = s
		}
	}
	return out
}

func langKey(s tg.LangPackStringClass) string {
	switch v := s.(type) {
	case *tg.LangPackString:
		return v.Key
	case *tg.LangPackStringPluralized:
		return v.Key
	case *tg.LangPackStringDeleted:
		return v.Key
	}
	return ""
}

// SetLangpackDir points the langpack.* methods at a directory of packs.
func (a *API) SetLangpackDir(dir string) {
	a.langs.dir = dir
}
