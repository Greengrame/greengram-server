# greengram-server

Greengram is a self-hosted messenger. This is its server: one Go program that
speaks MTProto, the protocol of Telegram's apps, so Telegram Web A, Android
forks and desktop clients pointed at it work against your own server and
your own database instead of Telegram's.

**Greengram is an independent project. It is not affiliated with, endorsed
by or connected to Telegram.** Accounts, messages and files live only on the
server that runs this; nothing talks to Telegram.

What works: accounts (a phone number, username and password; no SMS is
sent), private chats,
groups, channels, topics, media and files, stickers and custom emoji,
reactions, stories, bots with the Bot API, Stars, gifts and an instance
wallet, the web client (Telegram Web A with a patch) and an admin panel.
`docs/telegram-gaps.md` lists what Web A asks for that the server doesn't do.

## Self-hosting

You need a Linux box with:

- Go 1.27 (`go.mod` names the exact toolchain)
- PostgreSQL 17 or 18
- for the web client and admin panel: [Bun](https://bun.sh) 1.4
- for the web client build: `curl`, `patch`
- a domain and a reverse proxy with TLS in front (Caddy, nginx, Traefik)

### 1. Database

```
sudo -u postgres createuser -P greengram
sudo -u postgres createdb -O greengram greengram
```

The server makes its tables itself on every start (migrations in
`internal/store/migrations`).

### 2. Build

```
git clone git@github.com:Greengrame/greengram-server.git
cd greengram-server
go build -trimpath -o bin/ ./cmd/greengram-server ./cmd/ggminiapps ./cmd/ggstickers ./cmd/ggcollectibles
git clone git@github.com:Greengrame/greengram-webapp.git ../greengram-webapp
scripts/build-web.sh ../greengram-webapp out
scripts/build-admin.sh out
```

`build-web.sh <client checkout> out` builds the Greengram client into
`out/web`. It accepts the `greengram-webapp` repository directly and the
older checkout with `vendor/telegram-web-a`. The one-argument form downloads
pinned upstream Telegram Web A, verifies its sha256, and applies the server's
patch. The build takes a few minutes and about 2 GB of memory.

`build-admin.sh` builds the admin panel from `admin/` into `out/admin`: it
installs the panel's packages with bun at the versions in `admin/bun.lock`
and bundles them. It takes under a minute.

### 3. Configure

Everything is set through the environment. Put it in a file the service
reads, for example `/etc/greengram.env`:

```
GREENGRAM_DATABASE_URL=postgres://greengram:PASSWORD@localhost/greengram
GREENGRAM_DATA_DIR=/var/lib/greengram
GREENGRAM_BASE_URL=https://chat.example.org
GREENGRAM_PUBLIC_HOST=chat.example.org
GREENGRAM_COOKIE_SECURE=1
GREENGRAM_WEB_DIR=/srv/greengram/out/web
GREENGRAM_ADMIN_DIR=/srv/greengram/out/admin
```

| variable | default | what it is |
|---|---|---|
| `GREENGRAM_DATABASE_URL` | (required) | Postgres connection string |
| `GREENGRAM_DATA_DIR` | `data` | keys, uploaded files, bundles; back it up |
| `GREENGRAM_HTTP_ADDR` | `127.0.0.1:8092` | web client, sign-in page, admin panel, Bot API, `/apiws` |
| `GREENGRAM_MTPROTO_ADDR` | `:44380` | TCP for the apps |
| `GREENGRAM_BASE_URL` | `http://127.0.0.1:8091` | the site's address as people open it |
| `GREENGRAM_PUBLIC_HOST` | `127.0.0.1` | the host the apps are told to dial |
| `GREENGRAM_PUBLIC_PORT` | the MTProto port | the port the apps are told to dial |
| `GREENGRAM_COOKIE_SECURE` | off | `1` when the site is served over HTTPS |
| `GREENGRAM_WEB_DIR` | none | the built web client (`out/web`); without it `/` says none is installed |
| `GREENGRAM_ADMIN_DIR` | none | the admin panel build (`out/admin`); without it `/admin` says none is installed |
| `GREENGRAM_SITE_NAME` | `Greengram` | name of the announcements account |
| `GREENGRAM_ADMIN_CONTACT` | none | how to reach the admins, shown on the home page |
| `GREENGRAM_COMMUNITY_URL` | none | an http(s) invite to your community (Discord, a group), linked from the home page |
| `GREENGRAM_SECRET_KEY_FILE` | `<data dir>/instance-secret.key` | encrypts phone numbers; made on first start, never lose it |
| `GREENGRAM_MINIAPPS_DIR` | `<data dir>/miniapps` | the mini apps bundle, see below |
| `GREENGRAM_LANGPACK_DIR` | `<data dir>/langpack` | language packs, see `docs/languages.md` |
| `GREENGRAM_DC`, `GREENGRAM_EXCHANGE_DC` | `2`, `10002` | the DC number clients see |
| `GREENGRAM_DEBUG` | off | debug logging |

The data dir holds the server's RSA key (`server.pem`), the instance secret
and every uploaded file. All of them are made on first start. Losing the
RSA key means every app has to sign in again; losing the secret makes stored
phone numbers unreadable. With `GREENGRAM_BLOBS=s3` uploaded files live in
an S3 bucket (Cloudflare R2 works) instead, with a disk cache for reads; see
`docs/blobs-s3.md`.

### 4. Reverse proxy

Send the site's HTTPS traffic, websockets included, to `GREENGRAM_HTTP_ADDR`.
The apps connect straight to `GREENGRAM_MTPROTO_ADDR` over TCP; open that
port in the firewall (or forward it from the proxy as plain TCP). For Caddy:

```
chat.example.org {
	reverse_proxy 127.0.0.1:8092
}
```

### 5. Start

```
set -a; . /etc/greengram.env; set +a
bin/greengram-server
curl http://127.0.0.1:8092/healthz
```

A systemd unit:

```
[Unit]
Description=Greengram
After=network-online.target postgresql.service

[Service]
EnvironmentFile=/etc/greengram.env
ExecStart=/srv/greengram/bin/greengram-server
User=greengram
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

### 6. First account

Open the site and pick **create account**. The first person to sign up on a
new server becomes its admin. Sign-up stays open for everyone until an admin
closes it, in the admin panel's settings or through the admin API while
signed in as the admin (the `session` cookie from the browser):

```
curl -X PATCH -b 'session=<cookie>' -H 'Content-Type: application/json' \
	-d '{"registrationEnabled": false}' https://chat.example.org/api/admin/settings
```

### 7. Update using SSH

The repositories support GitHub SSH keys; a personal access token is not
required. Commit or stash local edits before pulling. From the server checkout:

```sh
git pull --ff-only
git -C ../greengram-webapp pull --ff-only
scripts/build-web.sh ../greengram-webapp out
go build -trimpath -o bin/ ./cmd/greengram-server
```

Use `../greengram-web` if that is the existing client folder name. Restart the
server after building; it applies database migrations at startup. The updated
client is required for widgets. See [Bot API setup and compatibility](docs/bot-api.md)
for polling examples, the new methods, and the local GRAM wallet adapter.

## Optional parts

**Admin panel.** The panel (members, reports, gifts, packs, settings) is in
`admin/` and opens at `/admin` for admins. Build it with
`scripts/build-admin.sh out` and set `GREENGRAM_ADMIN_DIR` to `out/admin`;
rebuild after updating the repository. Without it the server works, `/admin`
says the panel isn't installed and how to build it, and the admin API under
`/api/admin` is there either way.

**Mini apps.** The built-in bots BotFather, Stickers, Wallet and Fragment
open mini apps captured from Telegram. They are Telegram's, so they are not
in this repository; the server pins every file by sha256 (`internal/miniapps/locks`)
and reads them from the mini apps bundle. @bugsbot's Bugs and Suggestions
page is built from bugs.telegram.org's page, styles and pictures, which are in
the bundle too; its script (`internal/miniapps/static/bugs/platform.js`) and
API are ours. Without the bundle, everything else works and the mini apps say
they aren't installed. With a bundle:

```
bin/ggminiapps install miniapps-bundle.tar.gz -sha256 <its sha256>
bin/ggminiapps status
```

`install` also takes an `https://` URL. No restart needed, except that the
built-in bots get their pictures on the next start.

**Sticker and emoji packs, reactions.** `cmd/ggstickers` imports packs and
Telegram's reaction artwork from exports you make yourself; see the comment at
the top of `cmd/ggstickers/main.go`. Without them the sticker panel is empty.

**Collectible gifts.** `scripts/collectibles.py` fetches the art of
collectible gifts from the public changes.tg catalog into a local bundle, and
`cmd/ggcollectibles` imports it. Nothing it fetches goes in a repository.

**Language packs.** `docs/languages.md`.

**Bots.** Bots are made with @BotFather in any client and use the Bot API at
`https://<site>/bot<token>/<method>`, with `getUpdates` polling (no
webhooks yet). See [Bot API usage and compatibility](docs/bot-api.md) for
working methods, uploads, downloads, and Bot API 10.4 coverage.

## Clients

- **Web:** the build above, served at `/`.
- **Android** and **desktop** apps are in the works and not public yet.

## Development

```
go vet ./...
GREENGRAM_TEST_DATABASE_URL=postgres://greengram:PASSWORD@localhost/greengram_test go test -race ./...
```

Store and end-to-end tests need a real Postgres and skip without one; each
test makes its own schema and drops it. Tests that check the captured mini
apps read an installed bundle from `GREENGRAM_TEST_MINIAPPS_DIR` and skip
without it. See `CONTRIBUTING.md`.

The admin panel's tests:

```
cd admin
bun install --frozen-lockfile
bun test ./tests
```

## Community

Talk to us on [Discord](https://discord.gg/vM6S6qKwYs).

## People

Greengram is made by listixx and opuadm, its co-owner.

## Credits

- [gramsrv](https://github.com/iamxvbaba/gramsrv) (Apache-2.0): we read it for
  the MTProto edge, sessions, updates and file handling and write our own code.
  Files copied from it keep their copyright line plus a "changed by Greengram"
  note; its licence and notice are in `third_party/gramsrv/`.
- [iamxvbaba/td](https://github.com/iamxvbaba/td) (MIT, fork of
  [gotd/td](https://github.com/gotd/td)): TL types and the per-layer codecs for
  layers 225-232, used as a dependency. The server half of the key exchange
  is copied into `internal/edge/dh` with one change (which DC numbers count as
  ours); its licence is in `third_party/td/`.
- [Telegram Web A](https://github.com/Ajaxy/telegram-tt) (GPL-3.0): the web
  client. `web/telegram-web-a.patch` changes it, and a few of its icons are
  used by the server; see `third_party/telegram-tt/`.
- [pgx](https://github.com/jackc/pgx) (MIT): Postgres driver.
- [Kumo](https://github.com/cloudflare/kumo) (MIT), React, Phosphor Icons,
  Apache ECharts, lottie-web and Tailwind CSS: the admin panel's UI, bundled
  into its build; see `third_party/admin-panel/NOTICE`.

## License

GNU Affero General Public License v3.0, see `LICENSE`. If you run a changed
version for other people, the AGPL asks you to offer them its source.
