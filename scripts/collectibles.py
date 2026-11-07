#!/usr/bin/env python3
"""Fetch Telegram's collectible gift art into a local bundle for ggcollectibles.

Nothing this writes goes into a repository: the art is Telegram's.

  collectibles.py fetch --cache DIR "Plush Pepe" "Durov's Cap" ...
  collectibles.py fetch --cache DIR --all
  collectibles.py bundle --cache DIR --out BUNDLE "Plush Pepe" ...
  collectibles.py bundle --cache DIR --out BUNDLE --all

The source is the public changes.tg catalog (api.changes.tg, cdn.changes.tg):
every model, symbol (pattern) and backdrop of a collection with its rarity in
permille, the lottie of each model and symbol, a PNG of each symbol (the
pattern is drawn from a raster) and the gift's own pre-upgrade sticker.
Requests are one at a time with a pause between them; a file already in the
cache is never fetched again. --all takes every collection the catalog lists;
one that fails is skipped and the run goes on, and running it again picks up
where it stopped. A collection is complete once DIR/<key>/complete exists,
and bundle --all takes only complete ones. Symbols are one shared set across
collections, so each is fetched once into DIR/_patterns. Craft-only models
(no share in upgrades) aren't fetched.

The bundle has the sticker bundles' shape: active.json names a build,
builds/<sha256 of manifest.json>/manifest.json lists the collections and
builds/<sha>/assets/<sha256>.<ext> are the files.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = 'https://api.changes.tg/'
CDN = 'https://cdn.changes.tg/gifts/'
UA = 'greengram-collectibles/1 (one request at a time)'
PAUSE = 0.3


def get(url, tries=6):
    for i in range(tries):
        try:
            req = urllib.request.Request(url, headers={'User-Agent': UA, 'Accept-Encoding': 'gzip'})
            with urllib.request.urlopen(req, timeout=60) as r:
                data = r.read()
                if r.headers.get('Content-Encoding') == 'gzip':
                    data = gzip.decompress(data)
            time.sleep(PAUSE)
            return data
        except urllib.error.HTTPError as e:
            if e.code == 404:
                raise
            time.sleep(2 + i * 3)
        except Exception:
            time.sleep(2 + i * 3)
    raise Failed(url)


class Failed(Exception):
    pass


def q(s):
    return urllib.parse.quote(s, safe='')


def key(title):
    return re.sub(r'[^a-z0-9]', '', title.lower())


def cached(path, url):
    if os.path.exists(path) and os.path.getsize(path) > 0:
        return open(path, 'rb').read()
    try:
        data = get(url)
    except urllib.error.HTTPError:
        # the CDN files names with a plain apostrophe where the catalog has a curly one
        alt = url.replace(q('\u2019'), q("'"))
        if alt == url:
            raise
        data = get(alt)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    open(path + '.tmp', 'wb').write(data)
    os.replace(path + '.tmp', path)
    return data


def fetch(cache, title):
    d = os.path.join(cache, key(title))
    if os.path.exists(os.path.join(d, 'complete')):
        return 'cached'
    meta = json.loads(cached(os.path.join(d, 'gift.json'), API + 'gift/' + q(title)))['gift']
    models = json.loads(cached(os.path.join(d, 'models.json'), API + 'models/' + q(title)))
    backdrops = json.loads(cached(os.path.join(d, 'backdrops.json'), API + 'backdrops/' + q(title)))
    symbols = json.loads(cached(os.path.join(d, 'symbols.json'), API + 'symbols/' + q(title)))
    if not models or not symbols or not backdrops:
        print(f'{title}: not upgradable in the catalog, skipped', flush=True)
        return 'empty'
    cached(os.path.join(d, 'original.json'), CDN + 'originals/' + meta['id'] + '/Original.json')
    models = drawn(models)
    for m in models:
        cached(os.path.join(d, 'models', m['name'] + '.json'), CDN + 'models/' + q(title) + '/lottie/' + q(m['name']) + '.json')
    for s in drawn(symbols):
        base = pattern(cache, d, s['name'])
        cached(base + '.json', CDN + 'patterns/' + q(title) + '/lottie/' + q(s['name']) + '.json')
        cached(base + '.png', CDN + 'patterns/' + q(title) + '/png/' + q(s['name']) + '.png')
    open(os.path.join(d, 'complete'), 'w').close()
    print(f'{title}: {len(models)} models, {len(symbols)} symbols, {len(backdrops)} backdrops', flush=True)
    return 'fetched'


def pattern(cache, d, name):
    """Where a symbol's files are: the shared set, or a collection's own copy
    from before symbols were shared."""
    own = os.path.join(d, 'patterns', name)
    if os.path.exists(own + '.json') and os.path.exists(own + '.png'):
        return own
    return os.path.join(cache, '_patterns', name)


def catalog(cache):
    return json.loads(cached(os.path.join(cache, 'gifts.json'), API + 'gifts'))


def fetch_all(cache, titles):
    done, failed, empty = [], [], []
    for t in titles:
        try:
            r = fetch(cache, t)
        except (Failed, urllib.error.HTTPError, KeyError, ValueError) as e:
            print(f'{t}: FAILED {e}', flush=True)
            failed.append(t)
            continue
        (empty if r == 'empty' else done).append(t)
    print(f'complete {len(done)}, not upgradable {len(empty)}, failed {len(failed)}', flush=True)
    for t in failed:
        print('  failed:', t, flush=True)


def drawn(items):
    """What an upgrade draws from, with its share: the share Telegram gave it
    for upgrades (originalRarityPermille; crafting moved the current one).
    Craft-only attributes have none and are left out."""
    out = []
    for x in items:
        p = x.get('originalRarityPermille') or x['rarityPermille']
        if p:
            out.append(dict(x, rarityPermille=p))
    return out


def check_rarity(title, kind, items):
    # Telegram's per-attribute permille is rounded, so a list adds up to about 1000
    total = sum(x['rarityPermille'] for x in items)
    if not items or abs(total - 1000) > 50 or any(x['rarityPermille'] < 1 for x in items):
        raise SystemExit(f'{title}: {kind} rarities add up to {total}')


def bundle(cache, out, titles):
    assets = {}

    def asset(data, ext):
        h = hashlib.sha256(data).hexdigest()
        assets[h + '.' + ext] = data
        return {'file': f'assets/{h}.{ext}', 'sha256': h, 'size': len(data)}

    def tgs(path):
        # gzip with no name or time in the header, so a rebuild is the same bytes
        raw = open(path, 'rb').read()
        json.loads(raw)
        return asset(gzip.compress(raw, 9, mtime=0), 'tgs')

    out_sets = []
    for title in titles:
        d = os.path.join(cache, key(title))
        meta = json.load(open(os.path.join(d, 'gift.json')))['gift']
        models = drawn(json.load(open(os.path.join(d, 'models.json'))))
        backdrops = drawn(json.load(open(os.path.join(d, 'backdrops.json'))))
        symbols = drawn(json.load(open(os.path.join(d, 'symbols.json'))))
        for kind, items in (('model', models), ('symbol', symbols), ('backdrop', backdrops)):
            check_rarity(title, kind, items)
        s = {
            'name': key(title), 'title': meta['name'], 'telegramGiftId': meta['id'],
            'original': tgs(os.path.join(d, 'original.json')),
            'models': [], 'patterns': [], 'backdrops': [],
        }
        for m in models:
            s['models'].append({'name': m['name'], 'rarityPermille': m['rarityPermille'],
                                'sticker': tgs(os.path.join(d, 'models', m['name'] + '.json'))})
        for p in symbols:
            base = pattern(cache, d, p['name'])
            s['patterns'].append({'name': p['name'], 'rarityPermille': p['rarityPermille'],
                                  'sticker': tgs(base + '.json'),
                                  'image': asset(open(base + '.png', 'rb').read(), 'png')})
        for b in backdrops:
            s['backdrops'].append({'name': b['name'], 'backdropId': b['backdropId'], 'rarityPermille': b['rarityPermille'],
                                   'centerColor': b['centerColor'], 'edgeColor': b['edgeColor'],
                                   'patternColor': b['patternColor'], 'textColor': b['textColor']})
        out_sets.append(s)
        print(f"{meta['name']}: {len(s['models'])} models, {len(s['patterns'])} patterns, {len(s['backdrops'])} backdrops", flush=True)
    raw = json.dumps({'version': 1, 'source': 'changes.tg', 'collections': out_sets}, ensure_ascii=False, indent=1).encode()
    sha = hashlib.sha256(raw).hexdigest()
    shutil.rmtree(out, ignore_errors=True)
    root = os.path.join(out, 'builds', sha)
    os.makedirs(os.path.join(root, 'assets'))
    open(os.path.join(root, 'manifest.json'), 'wb').write(raw)
    for name, data in assets.items():
        open(os.path.join(root, 'assets', name), 'wb').write(data)
    json.dump({'version': 1, 'build': sha}, open(os.path.join(out, 'active.json'), 'w'))
    print('build', sha[:12], 'assets', len(assets))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('command', choices=['fetch', 'bundle'])
    ap.add_argument('titles', nargs='*')
    ap.add_argument('--cache', required=True)
    ap.add_argument('--out')
    ap.add_argument('--all', action='store_true', help='every collection in the catalog')
    a = ap.parse_args()
    if a.all == bool(a.titles):
        sys.exit('name collections or pass --all')
    if a.command == 'fetch':
        fetch_all(a.cache, catalog(a.cache) if a.all else a.titles)
    else:
        if not a.out:
            sys.exit('bundle needs --out')
        titles = a.titles
        if a.all:
            titles = [t for t in catalog(a.cache) if os.path.exists(os.path.join(a.cache, key(t), 'complete'))]
        bundle(a.cache, a.out, titles)


if __name__ == '__main__':
    main()
