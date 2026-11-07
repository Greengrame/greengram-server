#!/usr/bin/env python3
"""Builds the directory GREENGRAM_LANGPACK_DIR reads, from gramsrv's data/langpack.

    scripts/langpacks.py --gramsrv <gramsrv>/data/langpack \
        --fallback <web a>/src/assets/localization/fallback.strings --out <data dir>/langpack

weba, per language:
  direct  strings from gramsrv's weba pack whose key and {variables} still fit
          this Web A's fallback.strings
  filled  strings a key is missing, taken from the same language in another of
          Telegram's packs, only where that pack's English is exactly this
          Web A's English. Real translations, reused, nothing generated.
          --no-fill turns it off.
  hand    scripts/langpack-extra/<code>.strings, our own text for a few very
          visible keys and our own keys. Written for this project, not Telegram's.
          Only used where nothing above has the key.
  the rest stays on Web A's built-in English.

English gets no file on purpose: it would overwrite the bundled strings with
an older copy that doesn't have our keys. provenance.json has the counts.
Other packs (--packs android,ios,...) are copied as they are.
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import sys
import time
from collections import Counter

STRING = re.compile(r'"((?:\\.|[^"\\])*)"\s*=\s*"((?:\\.|[^"\\])*)";', re.S)
VARIABLE = re.compile(r'\{(\w+)\}')
FORMS = ('zero', 'one', 'two', 'few', 'many', 'other')
# webk comes first: same kind of app, so it wins a tie
FILL_FROM = ('webk', 'tdesktop', 'android', 'ios', 'macos', 'android_x')


def read(path):
    """key -> raw value, or key -> {form: raw value}, escapes left as they are."""
    with open(path, encoding='utf8') as f:
        text = f.read()
    out = {}
    for key, value in STRING.findall(text):
        base, form = None, None
        if '#' in key:
            base, form = key.split('#', 1)
        elif '_' in key and key.rsplit('_', 1)[1] in FORMS:
            base, form = key.rsplit('_', 1)
        if base:
            entry = out.setdefault(base, {})
            if isinstance(entry, dict):
                entry[form] = value
        else:
            out[key] = value
    return out


def variables(value):
    if isinstance(value, dict):
        return set().union(*(variables(v) for v in value.values()))
    return set(VARIABLE.findall(value))


def fits(english, value):
    return isinstance(english, dict) == isinstance(value, dict) and variables(english) == variables(value)


def sha256(path):
    with open(path, 'rb') as f:
        return hashlib.sha256(f.read()).hexdigest()


def clean_fill(english, value):
    """A borrowed string can't bring markup or format codes its English doesn't have."""
    if value == english or not value.strip():
        return False
    if variables(english) != variables(value):
        return False
    return not any(c in value and c not in english for c in ('%', '<', '&', '$', '**', '\\n'))


def build_memory(src, index, code):
    """English text -> Counter of this language's translations, over the other packs."""
    memory = {}
    for name in FILL_FROM:
        files = index.get(name, {})
        if code not in files or 'en' not in files:
            continue
        english, local = read(os.path.join(src, files['en']['file'])), read(os.path.join(src, files[code]['file']))
        for key, value in local.items():
            en = english.get(key)
            if isinstance(value, str) and isinstance(en, str) and clean_fill(en, value):
                memory.setdefault(en, []).append(value)
    return {en: Counter(values).most_common(1)[0][0] for en, values in memory.items()}


def write(path, strings):
    with open(path, 'w', encoding='utf8') as f:
        for key in sorted(strings):
            value = strings[key]
            if isinstance(value, dict):
                for form in FORMS:
                    if form in value:
                        f.write(f'"{key}#{form}" = "{value[form]}";\n')
            else:
                f.write(f'"{key}" = "{value}";\n')


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--gramsrv', required=True, help="gramsrv's data/langpack")
    ap.add_argument('--fallback', required=True, help="the patched Web A's fallback.strings")
    ap.add_argument('--out', required=True)
    ap.add_argument('--packs', default='', help='other packs to copy, comma separated')
    ap.add_argument('--no-fill', action='store_true')
    ap.add_argument('--version', type=int, default=int(time.strftime('%y%m%d%H')),
                    help='for every generated file; clients only take a higher one')
    args = ap.parse_args()

    with open(os.path.join(args.gramsrv, 'official-language-packs.json'), encoding='utf8') as f:
        doc = json.load(f)
    packs = {p['name']: p for p in doc['packs']}
    index = {p['name']: {l['lang_code']: l for l in p['languages']} for p in doc['packs']}
    fallback = read(args.fallback)
    total = len(fallback)

    shutil.rmtree(os.path.join(args.out, 'weba'), ignore_errors=True)
    os.makedirs(os.path.join(args.out, 'weba'))
    languages, report = [], {}
    for meta in packs['weba']['languages']:
        code = meta['lang_code']
        if code == 'en':
            languages.append({**meta, 'file': '', 'version': 0, 'strings_count': total, 'translated_count': total})
            continue
        source = os.path.join(args.gramsrv, meta['file'])
        pack = read(source)
        strings = {k: v for k, v in pack.items() if k in fallback and fits(fallback[k], v)}
        direct = len(strings)
        filled = 0
        if not args.no_fill:
            memory = build_memory(args.gramsrv, index, code)
            for key, english in fallback.items():
                if key not in strings and isinstance(english, str) and english in memory:
                    strings[key] = memory[english]
                    filled += 1
        hand = 0
        extra = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'langpack-extra', code + '.strings')
        if os.path.exists(extra):
            for key, value in read(extra).items():
                if key not in strings and key in fallback and fits(fallback[key], value):
                    strings[key] = value
                    hand += 1
        name = f'weba_{code}_v{args.version}.strings'
        write(os.path.join(args.out, 'weba', name), strings)
        languages.append({**meta, 'file': f'weba/{name}', 'version': args.version,
                          'strings_count': total, 'translated_count': direct + filled + hand})
        report[code] = {
            'name': meta['name'], 'direct': direct, 'filled': filled, 'hand': hand, 'english': total - direct - filled - hand,
            'percent': round((direct + filled + hand) * 100 / total, 1), 'source_file': meta['file'], 'source_sha256': sha256(source),
        }
        print(f'{code:8} {direct:5} direct {filled:5} filled {hand:3} hand {report[code]["english"]:5} english  {report[code]["percent"]}%')

    out_packs = [{'name': 'weba', 'languages': languages}]
    for name in filter(None, args.packs.split(',')):
        if name == 'weba':
            continue
        shutil.rmtree(os.path.join(args.out, name), ignore_errors=True)
        os.makedirs(os.path.join(args.out, name))
        for meta in packs[name]['languages']:
            shutil.copyfile(os.path.join(args.gramsrv, meta['file']), os.path.join(args.out, meta['file']))
        out_packs.append(packs[name])

    with open(os.path.join(args.out, 'official-language-packs.json'), 'w', encoding='utf8') as f:
        json.dump({'schema': 1, 'packs': out_packs}, f, ensure_ascii=False, indent=1)
    with open(os.path.join(args.out, 'provenance.json'), 'w', encoding='utf8') as f:
        json.dump({'fallback_keys': total, 'fallback_sha256': sha256(args.fallback), 'version': args.version,
                   'filled_from': [] if args.no_fill else FILL_FROM, 'languages': report}, f, ensure_ascii=False, indent=1)
    return 0


if __name__ == '__main__':
    sys.exit(main())
