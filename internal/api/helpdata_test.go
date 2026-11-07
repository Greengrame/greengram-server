package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func TestLangpackDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "weba"), 0o755)
	os.WriteFile(filepath.Join(dir, "official-language-packs.json"), []byte(`{"packs":[{"name":"weba","languages":[
		{"lang_code":"uk","name":"Ukrainian","native_name":"Українська","plural_code":"uk","official":true,"version":7,"file":"weba/weba_uk_v7.strings"}]}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "weba", "weba_uk_v7.strings"), []byte(`"Hello" = "Привіт";
"Apples#one" = "{count} яблуко";
"Apples#few" = "{count} яблука";
"Apples#other" = "{count} яблук";
"Quote" = "say \"hi\"\n";
`), 0o644)

	p := &langpacks{dir: dir}
	langs := p.pack("weba").languages()
	if len(langs) != 1 || langs[0].LangCode != "uk" || langs[0].NativeName != "Українська" {
		t.Fatalf("languages %+v", langs)
	}
	d, err := p.pack("weba").difference("uk", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != 7 || len(d.Strings) != 3 {
		t.Fatalf("pack %+v", d)
	}
	if s := d.Strings[0].(*tg.LangPackString); s.Value != "Привіт" {
		t.Fatalf("plain string %+v", s)
	}
	pl := d.Strings[1].(*tg.LangPackStringPluralized)
	if few, _ := pl.GetFewValue(); pl.Key != "Apples" || few != "{count} яблука" || pl.OtherValue != "{count} яблук" {
		t.Fatalf("plural %+v", pl)
	}
	if s := d.Strings[2].(*tg.LangPackString); s.Value != "say \"hi\"\n" {
		t.Fatalf("escapes %q", s.Value)
	}
	// up to date: nothing to send
	if d, _ := p.pack("weba").difference("uk", 7); len(d.Strings) != 0 || d.Version != 7 {
		t.Fatalf("difference from current %+v", d)
	}
	if _, err := p.pack("weba").load("de"); err == nil {
		t.Fatal("a language that isn't there loaded")
	}
	// no directory: English, empty, so the apps keep their own strings
	none := &langpacks{}
	if l := none.pack("android").languages(); len(l) != 1 || l[0].LangCode != "en" {
		t.Fatalf("fallback languages %+v", l)
	}
}

func TestLangpackDifferenceKeepsFromVersion(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "weba"), 0o755)
	os.WriteFile(filepath.Join(dir, "weba", "weba_uk_v9.strings"), []byte(`"Hello" = "Привіт";`), 0o644)
	p := &langpacks{dir: dir}
	d, err := p.pack("weba").difference("uk", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.FromVersion != 7 || d.Version != 9 || len(d.Strings) != 1 {
		t.Fatalf("difference %+v", d)
	}
}

// A file dropped in with no index entry is a language, the highest version wins.
func TestLangpackDropIn(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "weba"), 0o755)
	os.WriteFile(filepath.Join(dir, "weba", "weba_uk_v1.strings"), []byte(`"Hello" = "old";`), 0o644)
	os.WriteFile(filepath.Join(dir, "weba", "weba_uk_v2.strings"), []byte(`"Hello" = "Привіт";"Bye" = "Бувай";`), 0o644)
	os.WriteFile(filepath.Join(dir, "weba", "weba_pt_BR_v3.strings"), []byte(`"Hello" = "Olá";`), 0o644)
	os.WriteFile(filepath.Join(dir, "weba", "weba_ar_v1.strings"), []byte(`"Hello" = "مرحبا";`), 0o644)
	os.WriteFile(filepath.Join(dir, "weba", "notes.txt"), []byte(`skip`), 0o644)
	p := &langpacks{dir: dir}
	got := map[string]tg.LangPackLanguage{}
	for _, l := range p.pack("weba").languages() {
		got[l.LangCode] = l
	}
	if len(got) != 3 {
		t.Fatalf("languages %+v", got)
	}
	if uk := got["uk"]; uk.NativeName != "Українська" || uk.TranslatedCount != 2 || uk.StringsCount != 2 {
		t.Fatalf("uk %+v", uk)
	}
	if !got["ar"].Rtl || got["pt-br"].PluralCode != "pt" {
		t.Fatalf("ar %+v pt %+v", got["ar"], got["pt-br"])
	}
	d, err := p.pack("weba").difference("uk", 0)
	if err != nil || d.Version != 2 || len(d.Strings) != 2 {
		t.Fatalf("pack %+v %v", d, err)
	}
}
