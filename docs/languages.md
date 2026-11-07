# Languages

## Web A and other clients: langpack.*

The server serves language packs from `GREENGRAM_LANGPACK_DIR`, by default
`<data dir>/langpack`. It reads the directory at start. Nothing there: English
only, and the apps use the strings they ship with.

Web A asks for pack `weba`. Picking a language in Settings > Language calls
`langpack.getLanguage` + `getLangPack`, caches the strings in the browser and
falls back to its own English `fallback.strings` key by key.

### Building the directory

The packs are not in this repository (see `third_party/gramsrv/NOTICE`). The
source is gramsrv's `data/langpack`, public, no Telegram account involved:

    git clone --depth 1 --filter=blob:none --sparse https://github.com/iamxvbaba/gramsrv
    git -C gramsrv sparse-checkout set data/langpack
    scripts/langpacks.py --gramsrv gramsrv/data/langpack \
        --fallback <patched web a>/src/assets/localization/fallback.strings \
        --out <data dir>/langpack

Restart the server. Use the same Web A tree the build came from, it decides
which keys count. `provenance.json` in the output has the counts per language.

Each language's `weba` file holds:

- **direct**: gramsrv's `weba` strings whose key and `{variables}` still fit
  this Web A.
- **filled**: for a key still missing, the same language's string from another
  Telegram pack (`webk`, `tdesktop`, `android`, `ios`, `macos`, `android_x`), only
  where that pack's English is exactly this Web A's English. Real translations
  reused, nothing generated. `--no-fill` leaves them out.
- **hand**: `scripts/langpack-extra/<code>.strings`, written for this project and
  not Telegram's: the Settings subtitles and our wallet transfer keys, Russian and
  Ukrainian. Only used where nothing above has the key.

Everything else stays English. The language list shows the percentage.

`--packs android,ios,...` copies other clients' packs unchanged (115 MB for all).

### Adding a language by hand

Drop a file in and restart:

    <dir>/weba/weba_<code>_v<version>.strings

    "Key" = "Value";
    "Key#one" = "{count} thing";
    "Key#other" = "{count} things";

Its language shows up in the list even if `official-language-packs.json` doesn't
know it, and a higher version than the index has wins. Keys are Web A's, from its
`fallback.strings`, keep the `{variables}` as they are. Browsers only take a
higher version than the one they have cached. Names and right-to-left come from
a small table in `internal/api/helpdata.go`; an unknown code shows as itself.

## Wallet

The captured wallet carries its own 17 translations (`vendor/walt`,
`*-translation.*.js`). `/profile/languages` lists them, the choice is stored in
`wallet_prefs.language` and comes back as `bot_language`. No choice yet: the
language of the app that opened it (`language_code` in the launch data), else
English. The passcode screens are the wallet's own, in those translations.
Greengram's own wallet screens aren't in those chunks: the DeFi and transfer
confirmation screens are English.
