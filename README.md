<a id="readme-top"></a>

<div align="center">

<img src="https://raw.githubusercontent.com/hareru/public/1a4381431b8d637895fc9120cfeb94f035af3294/cherry.svg" alt="Cherry logo" width="200">

<h1>Cherry</h1>

<p><b>A server emulator for discontinued LINE PLAY (10.1.0), written in Go and built for preservation.</b></p>

[![Discord][discord-shield]][discord-url]
[![Gate][gate-shield]][gate-url]
[![License][license-shield]][license-url]
[![Last commit][commit-shield]][commit-url]
[![Issues][issues-shield]][issues-url]
[![Stars][stars-shield]][stars-url]

<a href="#getting-started"><strong>Get started »</strong></a>
<br />
<a href="https://github.com/festivities/Cherry/issues/new?labels=bug">Report a bug</a>
&middot;
<a href="https://github.com/festivities/Raspberry">Raspberry patcher</a>
&middot;
<a href="https://discord.gg/E9NNwhJmqN">Join the Discord</a>

</div>

> [!NOTE]
> For setup help and support, join our [Discord server](https://discord.gg/E9NNwhJmqN).

## Table of contents

- [About](#about)
- [Features](#features)
- [How it fits together](#how-it-fits-together)
- [Getting started](#getting-started)
- [Configuration](#configuration)
- [Assets](#assets)
- [Connecting a client](#connecting-a-client)
- [Operator data and the Staff Panel](#operator-data-and-the-staff-panel)
- [Operations](#operations)
- [Repository layout](#repository-layout)
- [Known gaps](#known-gaps)
- [Troubleshooting](#troubleshooting)
- [Reporting issues](#reporting-issues)
- [Contributing](#contributing)
- [Documentation](#documentation)
- [License and legal](#license-and-legal)
- [Acknowledgments](#acknowledgments)

## About

LINE PLAY (`jp.naver.lineplay.android`) was LINE's avatar social game. It ran from 2012 until its servers closed in 2024. Cherry rebuilds the server side so the final client (10.1.0.0, Android versionCode 254, and the iOS 10.1.0 build) can be played again: avatars, Gardens, rooms, Squares, minigames, friends and chat.

- **No real money.** Every paid path stays switched off: no paid currency, memberships, VIP, ads or season passes. Every currency is earned by playing. Staff can still hand out rewards.
- **No LINE files.** Cherry ships no LINE binaries, APKs or game assets. Operators supply the client and the asset dataset from their own copies (see [Assets](#assets)).
- **Endpoint-agnostic.** No server address is hard-coded anywhere. Every listener, hostname, port and secret is operator configuration.
- **Two dependencies only.** The Go standard library plus [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), a pure-Go SQLite. Builds use `CGO_ENABLED=0`.

> [!IMPORTANT]
> Cherry is an unofficial fan preservation project. It is not affiliated with, endorsed by, or connected to LINE Corporation (now LY Corporation), NAVER, or any of their affiliates. See [License and legal](#license-and-legal).

### Built with

- [Go](https://go.dev) (standard library) — the server, the Staff Panel and the tools
- [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — storage, without cgo
- Plain HTML and CSS with a small script — the Staff Panel (strict CSP, no frameworks)

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Features

Every feature below is implemented on the server, and nearly all of them have been verified on a real client. The per-feature status, with evidence, is in [.agents/PLAN.md](.agents/PLAN.md). Wire formats, ids and client rules were recovered from the client itself. Where no original data survives (prices, drop tables, rewards, recipes), Cherry fills it in, and those values are marked as Cherry-made (see [Original vs. Cherry-made content](#original-vs-cherry-made-content)).

<details open>
<summary><b>Accounts and avatars</b></summary>

- **Sign-in:** Discord sign-in through the patched client on public servers. Device-bound guest login is for development labs. One login per account: the newest sign-in logs out the older device, which shows the game's own "used on another device" message. Logout and Delete Avatar work too.
- **Avatar creation:** human and animal avatars, the welcome gift and the starter kit.
- **Look:** the Closet (equip, save, and the saved look shown everywhere), Styling slots (saved outfits you can load again, with extra slots to buy) and Avatar Pic photos with the original frames.
- **Face Shop:** 1,173 original human face parts, the animal eyes, noses, mouths, skins, ears and tails, and skin tones.
- **Colorant:** Dye Shop, Dye Inventory, custom `#RRGGBB` dyes, Dye Base Shop and Bleach.
- **Avatar Birthday:** the anniversary flag and its reward.

</details>

<details>
<summary><b>Garden</b></summary>

- **The Garden:** your home map, with 24 Garden maps and landscapes (Interior Shop, Garden Edit) and friends' visits shown live.
- **Gathering:** essence, ore and fishing spots, the Bag, Smiles, and the daily QUEST panel.
- **Atelier:** crafting with data-driven recipes and blueprints.
- **Butlers:** the seven original Garden butlers (Ophelia, Ian, Merry, Kai, Celina, Elena and Michel). Hire them, pick a chief and send them auto-gathering. Voice Book, Skin Shop and Memories are in too.
- **Seasonal Garden event icons.**

</details>

<details>
<summary><b>My Room and Room Party</b></summary>

- **My Room:** furniture, layouts, two floors, nine preset slots, the Remodeling Shop and Model House sets.
- **Room Party:** invite and join, walk, chat, pets in the room and the room heart button.

</details>

<details>
<summary><b>Squares</b></summary>

- **Eleven Squares:** LINE City, LINE Café, Runway Square, Pet Theme Park, LINE Park, Treasure Island, LINE City Shibuya, Disney Square, Disney Disco, Hatsune Miku Square and SANRIO CHARACTERS Square.
- **Square rooms:** every Square is split into numbered rooms. The owner can change the room size (default 20 players). The player list shows everyone's profile pictures.
- **Treasure Island:** fishing with all 81 original species. There is a 9-page Collection Book with page rewards, a Fishing Shop with rods and bait packs, and rare-fish retries. Boss fishing raids can be played solo or with friends (lobbies, invites, shared boss HP). Boss Coins buy items at the exchange office, and a boss book tracks your catches.
- **LINE City:** the Riding Pet race and the Ride Point exchange shop.

</details>

<details>
<summary><b>Pets, shops and prize games</b></summary>

- **Minipet Shop:** 310 pets, covering dogs, cats and ride pets. Ride pets keep their original ride stats. The Pet Book calls and follows.
- **Shops:** fashion and interior shops, daily deals, limited items and sell-back.
- **Gacha Shop.**
- **Lucky Spin and Jackpot Spin:** the server draws every result. Wheels and odds come from operator data.

</details>

<details>
<summary><b>Treasure World and Circle</b></summary>

- **Treasure World:** a dice board game that runs in seasons: tickets, Jewels, exchange squares and Mystery Cards. Quests, missions and secret quests, rankings with point and rank rewards, wearable buff items, a mini-game, traps and broom travel.
- **Circle (player clubs):** create, search, apply, invite, roles and upgrades. The original cat hot-spa room with original logos and backgrounds. Points, seasons, rankings, missions and chat.

</details>

<details>
<summary><b>Social</b></summary>

- **Friends:** search and Avatar Code lookup, invitations, recommendations, hearts and My Pins.
- **Diary:** text, photo and comic posts, comments, likes, Share to Diary and a wish list.
- **Guestbook, Profile Edit, profile pictures and Status Message.**
- **Gifts:** the Gift Shop and item gifts.
- **Chat:** Lounge Chat theme rooms and 1:1 friend chat, with saved history and unread badges.
- **Player reports**, which go to the staff report queue.

</details>

<details>
<summary><b>Economy and events</b></summary>

- **Currencies:** Gems, Cash, Boss Coins, Ride Points, Smiles and Face Shop tickets. Every one is earned in game, and every change is written to a ledger in the same database transaction as the new balance.
- **Daily rewards:** the daily login calendar, plus seasonal calendars, season popups and event quests for New Year, Winter and Halloween.
- **Login popups:** operator banners (for example your community's Discord invite) shown once per game day, with a button that opens a link in the phone's browser or an in-game screen.
- **Mailbox:** item gifts and staff mail.
- **Quests:** daily, hearts and event quests.

</details>

<details>
<summary><b>Staff Panel</b></summary>

- **Sign-in:** a separate HTTPS web UI on its own listener and hostname. Staff sign in with Discord, and only the owner and allowlisted staff get in. Discord MFA is required, and sensitive actions need a sign-in from the last five minutes.
- **Overview:** a live dashboard (players online, request rate, picture storage, database size and the Go runtime) and a global search.
- **Players:** a page per player with balances, inventory, ledger, social data and moderation history. From there staff can grant items, rename, kick, ban or unban, remove posts and pictures, and send mail to one player or to everyone. The owner can set or add balances for every player, move an avatar to another Discord identity and restore deleted avatars.
- **Moderation:** the report queue, the chat log (kept for a set number of days) and a photo grid.
- **Insights:** who is online, the economy, and activity.
- **Content:** an editor for every operator document, with a form view, diff, validation, publish, rollback and version history.
- **System:** the audit log, and the owner's Settings page. Settings change live, with no restart: chat retention, staff members, Square room size, the player cap, the server limits and the upload limits.

</details>

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## How it fits together

```
           patched LINE PLAY client (Android / iOS)
                │ HTTPS     │ HTTP     │ session    │ gateway
                ▼           ▼          ▼            ▼
┌─────────────────────────────────────────────────────────────┐
│ cherry                                                      │
│  web (REST /v4/…, sign-in, discovery, asset routes)         │
│  lpn (session LO frames, gateway LPN frames: Garden,        │
│       Square, Room Party, Circle, Treasure World)           │
│  economy · room · thumbs · auth · admin (Staff Panel)       │
│  store ── cherry.db (SQLite, WAL)                           │
│  assets ── read-through cache ── S3-compatible bucket (R2)  │
└─────────────────────────────────────────────────────────────┘
```

| Default listener | Setting | What it carries |
| --- | --- | --- |
| `:443` TCP | `listen.https` | The game's REST API (`/v4/…`, JSON), Discord sign-in (`/auth/v1/…`), discovery (`/cherry/v1/info`), asset streaming and Cherry's own pages. It keeps the static-RSA cipher suites the client's old OpenSSL needs, alongside modern ones. |
| `:80` TCP | `listen.http` | Plain HTTP, limited to the exact paths the client fetches without TLS (item files, notices, the iOS static base). |
| `:10123` TCP | `listen.session` | The session socket: length-prefixed `LO` frames with JSON control messages (sign-on, keepalive, pushes, Lounge and 1:1 chat). |
| `:10000` TCP | `listen.gateway` | The game gateway: LPN frames carrying a big-endian message id plus protobuf, one namespace per game area. After a key-table handshake (`arts_session/sckey.enc`) the frames are encrypted with AES-128-CFB128. |
| `:53` UDP | `listen.dns` | A DNS sink for development labs with the unmodified client. It is always off on public servers. |
| `127.0.0.1:8099` | `listen.admin` | Loopback-only tools: `/healthz`, `/debug/vars` counters, `/admin/*` lab helpers and the operator document API. |
| off | `listen.staff` | The Staff Panel. |

The client never learns Cherry's address from the server. Replies keep LINE's original host names and ports, and the patched client maps them to the operator's endpoint. Cherry routes requests by path, and SNI only chooses the certificate. The operator's publicly trusted certificate covers `server.hostnames`, and self-signed pairs cover LINE's names (the native client does not verify them).

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Getting started

### Prerequisites

- **Go 1.27 or newer** (the version in `go.mod`). Nothing else is needed to build.
- For `go test -race`: cgo and a C compiler. Everything else builds and tests without one.
- To play: an S3-compatible bucket holding your own asset dataset ([Assets](#assets)) and a client patched by [Raspberry](https://github.com/festivities/Raspberry).

### 1. Build

```sh
git clone https://github.com/festivities/Cherry.git
cd Cherry
go build -o cherry ./cmd/cherry          # cherry.exe on Windows
./cherry -h                              # every setting, its flag, environment name, default and meaning
```

Cross-compiling works without a C toolchain:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o cherry ./cmd/cherry
```

### 2. Run the gate

Every commit passes these checks, and CI (`.github/workflows/gate.yml`) runs them on Linux along with `govulncheck`:

```sh
gofmt -l cmd internal data                                   # must print nothing (LF line endings)
go vet ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go vet ./...
go test -race -count=1 -timeout 550s ./...                   # -race needs cgo and a C compiler
CHERRY_TEST_NO_ARCHIVE=1 go test -count=1 -timeout 550s ./... # the CI view
```

Run `gofmt` on `cmd internal data`, not on `.`, so it never walks a local asset archive. Some tests compare against a maintainer's local device archive. They skip themselves when it is absent, and `CHERRY_TEST_NO_ARCHIVE=1` forces that view on any machine. Tests need no network, no bucket and no client.

### 3. Try a local sandbox (no client, no assets)

This mode is for development and trying the Staff Panel. Everything listens on loopback, the DNS sink is off, and `dev-fake` replaces Discord with a test sign-in. Use empty, throwaway data and cache directories:

```sh
mkdir -p /tmp/cherry-dev/data /tmp/cherry-dev/cache
./cherry \
  -listen-https 127.0.0.1:18443 -listen-http 127.0.0.1:18080 \
  -listen-session 127.0.0.1:20123 -listen-gateway 127.0.0.1:20000 \
  -listen-admin 127.0.0.1:18099 -listen-dns= \
  -data-dir /tmp/cherry-dev/data -cache-dir /tmp/cherry-dev/cache \
  -auth-mode dev-fake \
  -listen-staff 127.0.0.1:18444 -staff-owner dev-owner
```

Then:

```sh
curl -s  http://127.0.0.1:18099/healthz            # ok
curl -sk https://127.0.0.1:18443/cherry/v1/info    # discovery document
```

To sign in to the Staff Panel as the owner, open `https://127.0.0.1:18444/auth/login?as=owner` and accept the self-signed certificate warning.

Without bucket credentials Cherry still starts. It logs one line saying the art checks were skipped, original files answer 404 or a placeholder, and the shop catalog is empty, so purchases are refused. This is expected.

### 4. A development lab with the unmodified client

`auth.mode lab-guest` (the default) serves the stock, unpatched client. Point the device's DNS at Cherry's DNS sink (`listen.dns`). The sink answers LINE's host names with Cherry's address. On the Android emulator, `-dns-server <host IP>` does this. Players use the client's own "Start as Guest", which is bound to the device. This mode needs the standard ports (443, 80, 10000, 10123 and UDP 53), so it usually needs administrator rights or `CAP_NET_BIND_SERVICE`.

> [!WARNING]
> `lab-guest` is for labs only: `server.public=true` refuses `lab-guest` and the DNS sink.

### 5. A public server

A public server runs with `server.public=true` and `auth.mode discord`. Cherry refuses to start until all of these are in place:

- **Certificate:** a publicly trusted **RSA** certificate for your `server.hostnames` (`tls.cert-file`, `tls.key-file`). Get it with any ACME client. Cherry reloads the PEM files when they change.
- **Discord application:** a client id, a file holding the client secret, and the exact callback URL you registered with Discord. That URL is `https://<your host>[:port]/auth/v1/discord/callback`.
- **Session secret:** `auth.session-secret-file`, a file of at least 32 random bytes (for example `head -c 48 /dev/urandom > session.secret && chmod 600 session.secret`). Every session key is derived from it. If it is lost, every player has to sign in with Discord again. No data is lost.
- **Private files:** off Windows, the secret files must not be readable by group or others (mode `0600`).
- **No DNS sink:** it turns off automatically on a public server.

New sign-ups follow `auth.signup`: `open`, `allowlist` (the default in `discord` mode, needs `auth.allowlist-file`) or `closed`. A player who already has an account can always sign in.

<details>
<summary>An example <code>cherry.json</code> (every value is a placeholder)</summary>

```json
{
  "server.public": true,
  "server.name": "My Cherry",
  "server.hostnames": ["play.example.com"],
  "data.dir": "/var/lib/cherry",
  "cache.dir": "/var/cache/cherry",
  "tls.cert-file": "/etc/cherry/tls/fullchain.pem",
  "tls.key-file": "/etc/cherry/tls/privkey.pem",

  "auth.mode": "discord",
  "auth.signup": "allowlist",
  "auth.allowlist-file": "/etc/cherry/signup-allowlist.txt",
  "auth.discord.client-id": "<discord application id>",
  "auth.discord.secret-file": "/etc/cherry/discord-client-secret",
  "auth.discord.redirect": "https://play.example.com/auth/v1/discord/callback",
  "auth.session-secret-file": "/var/lib/cherry/session.secret",

  "listen.staff": ":8443",
  "staff.hostname": "staff.example.com",
  "staff.tls-cert-file": "/etc/cherry/tls/staff-fullchain.pem",
  "staff.tls-key-file": "/etc/cherry/tls/staff-privkey.pem",
  "staff.owner": "<your Discord user id>",
  "staff.discord.redirect": "https://staff.example.com:8443/auth/callback"
}
```

</details>

Run it with `cherry -config /etc/cherry/cherry.json`. Cherry shuts down cleanly on `SIGTERM`, so a service manager can stop it.

<details>
<summary>A minimal systemd unit</summary>

```ini
[Unit]
Description=Cherry (LINE PLAY server emulator)
Wants=network-online.target
After=network-online.target

[Service]
User=cherry
ExecStart=/usr/local/bin/cherry -config /etc/cherry/cherry.json
Restart=on-failure
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
StateDirectory=cherry
CacheDirectory=cherry

[Install]
WantedBy=multi-user.target
```

</details>

Things to know before you go public:

- **Ports:** the patched client can map its ports (443, 80, 10000, 10123) to any server ports, so Cherry does not have to own the standard ones.
- **No reverse proxy.** Rate and connection limits count the connecting address, and Cherry reads no `X-Forwarded-For` header, so behind a proxy every player would share one budget. The session and gateway ports are raw TCP.
- **Staff Panel address:** give it its own hostname, and keep that hostname off shared certificates if you want it hard to find. Its Discord callback must use the port that `listen.staff` serves.
- **Health check:** `/healthz` on the loopback admin listener is the thing to watch.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Configuration

Every setting has a file key, a flag and an environment variable. When a setting is given more than once, the first source in this list wins:

1. command-line flag: `-listen-https :443`
2. environment variable: `CHERRY_LISTEN_HTTPS=:443`
3. JSON file given with `-config` or `CHERRY_CONFIG`: `{"listen.https": ":443"}`
4. built-in default

An empty value still counts as a value: `-listen-dns=` turns the DNS sink off. Unknown keys in the JSON file are errors, so a typo cannot pass silently. Cherry checks the whole combination at startup and refuses to start with a one-line message that names the bad setting. `cherry -h` prints the full table.

**Auth modes** (`auth.mode`):

| Mode | Sign-in | Use |
| --- | --- | --- |
| `lab-guest` (default) | the stock client's device-bound guest login | labs with the unmodified client; refused when `server.public=true` |
| `discord` | Discord OAuth through the patched client ([CONTRACT §3](.agents/CONTRACT.md)) | public servers |
| `dev-fake` | a test provider that signs anyone in as `dev-<name>`; its Staff Panel binds loopback only | development and tests; refused when `server.public=true` |

**Setting groups:**

| Keys | What they control |
| --- | --- |
| `listen.https`, `listen.http`, `listen.session`, `listen.gateway`, `listen.dns`, `listen.admin` | Listener addresses. `listen.admin` must be a loopback address. |
| `data.dir`, `cache.dir` | State and re-downloadable caches. Both default to the user cache directory plus `Cherry` (`%LOCALAPPDATA%\Cherry` on Windows, `~/.cache/Cherry` on Linux). **Set them explicitly on a server.** |
| `server.public`, `server.name`, `server.hostnames` | The public-server switch, the display name in discovery, and the operator host names. |
| `tls.cert-file`, `tls.key-file` | The operator certificate on the game HTTPS port. |
| `auth.*` | Mode, sign-up gate and allowlist, the Discord application, and the session secret. |
| `listen.staff`, `staff.*` | The Staff Panel: hostname, certificate, owner, staff allowlist and Discord callback. |
| `limits.*` | Connections per address and in total, token buckets for API requests, accounts, static files and gateway frames, guest logins, and `limits.max-players` (default 200). In `discord` and `dev-fake` modes the rate limits default on (for example 30 API requests a second per address, burst 400). In `lab-guest` the rate limits default off. |
| `uploads.*` | Picture uploads (diary, room, profile). Settings: on/off, one storage limit per player across all kinds, a server-wide total, a per-upload receive cap (20 MiB) and stored-size targets for JPEG and PNG (150 KiB each). A built-in pure-Go compressor downscales and re-encodes uploads to meet those targets. |
| `retention.*` | How many days an unused login token is kept (`idle-token-days`) and how long the chat log keeps messages (`chat-days`). |

> [!TIP]
> Many of these can also be changed live on the Staff Panel's **Settings** page, which overrides the file value: chat retention, staff members, Square room size, the player cap, the server limits and the upload limits. Those overrides are stored in the database and every change is audited.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Assets

The client needs a few gigabytes of original files: item art, UI, sounds, map packs and the string table. Cherry serves them from an **S3-compatible bucket** through a read-through cache in `cache.dir`. Only Cloudflare R2 has been tested. Cherry never reads a local archive at runtime, and the repository contains no assets.

- **Bucket credentials:** put `cloudflare.json` in `data.dir` with `r2_endpoint`, `r2_bucket`, `r2_access_key_id` and `r2_secret_access_key`. Keep the file private.
- **Streaming:** `config/streaming.json` `{"enabled": true}` in `data.dir` points the client's built-in patcher at the bucket. A fresh install then downloads what it needs (about 250 MB).
- **Bucket layout:** `static/` holds byte-exact copies of original files. `cherry/` holds Cherry-made files and the indexes. The two trees never mix.
- **Building the bucket:** `cmd/cherry-assets` builds and uploads it from your own sources (device backups of the app's `files/` folder, APKs, an iOS app cache). Run `plan` first, then `upload`, then `verify`. It can also `convert` items from the old 2014 iOS item archive, and those converted items are stored as Cherry-made.

**[.agents/DATASET.md](.agents/DATASET.md)** is the step-by-step guide: the bucket layout, choosing and auditing sources, keeping Cherry-made files out of `static/`, and finding gaps from the server log.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Connecting a client

Players need a client patched by **[Raspberry](https://github.com/festivities/Raspberry)**, the companion patcher. You supply your own copy of the final client: the Android APK 10.1.0.0 or a decrypted iOS IPA 10.1.0. Raspberry checks its hash and patches it for your endpoint (host name plus port map). The patched client:

- redirects only LINE PLAY's own host names to your server,
- replaces the LINE login with Discord sign-in,
- points in-app web pages and notices at your server,
- adjusts only what re-signing needs.

Raspberry's README has the build and patch steps. The interface between the two (host set, port map, sign-in endpoints, discovery, TLS) is specified in **[.agents/CONTRACT.md](.agents/CONTRACT.md)**.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Operator data and the Staff Panel

### Operator documents

Everything Cherry had to invent lives in **29 JSON documents** under `data/`, so operators can change it without touching Go code. That covers prices, gacha pools and rates, drop tables, fish tables, recipes, rewards, quests, calendars, Square and Garden lists, butlers, pets, spins, styling slots, login popups, Circle and Treasure World rules, and the menu. The documents are embedded in the binary. Two more files, `streaming.json` and `itemconv.json`, are plain on/off switches.

Each document loads from the first of these that exists:

1. a version published in the database, from the Staff Panel's Documents editor or `scripts/cherry-docs.py` on the loopback admin port,
2. a file with the same name in `<data.dir>/config/`,
3. the embedded default.

Documents decode strictly and are validated at startup, including checks that the items they name have art. A bad config file stops startup. A published version that fails validation falls back to the next source.

> [!NOTE]
> A published document applies at the next restart. The Settings page is the only place where changes take effect live.

### Original vs. Cherry-made content

Cherry keeps what it invents clearly apart from original game data:

- **Data files:** every data file carries `"origin"`, set to `cherry` (wholly Cherry-made) or `mixed` (original ids and art with Cherry-made values), plus a `note` saying which field is which.
- **Ids:** Cherry-made ids (fish, quests, gacha, spins, Circles, Treasure World seasons, chat rooms and others) live in reserved blocks that are checked at startup.
- **Pictures:** Cherry-made pictures (generated thumbnails, fish cards, wheels, menu icons) are served under `/cherry/…` routes and stored only under the bucket's `cherry/` tree. They are never presented as originals.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Operations

- **Database:** all state lives in one SQLite database, `<data.dir>/cherry.db`, with `-wal` and `-shm` files beside it. That covers accounts, the currency ledger, social data, Circles, Treasure World, chat, reports, published documents and the audit log. Login tokens and sign-in codes are stored only as SHA-256 hashes. Player pictures live in `diary-media/`, `room-media/` and `profile-media/`.
- **Backups:** copy the three `cherry.db*` files while Cherry is stopped, or replicate continuously with [Litestream](https://litestream.io/), an external program that Cherry does not depend on. Pictures are not in the database, so back up the media folders separately.

  <details>
  <summary>A minimal Litestream config (check the Litestream docs for your version and storage provider)</summary>

  ```yaml
  dbs:
    - path: /var/lib/cherry/cherry.db
      replicas:
        - type: s3
          bucket: <your-backup-bucket>
          path: cherry
          endpoint: https://<account-id>.r2.cloudflarestorage.com
          region: auto
  # credentials: LITESTREAM_ACCESS_KEY_ID / LITESTREAM_SECRET_ACCESS_KEY in the service environment
  ```

  </details>

- **Upgrades:** schema migrations only go forward, and an older build refuses a newer database. Back up before you deploy a build that changes the schema.
- **Restores:** never restore the whole database from an old backup while players are logged in. Every session check rotates login tokens, so an older database drops the tokens issued since, and those clients log out. Fix individual rows instead.
- **The cache:** `cache.dir` can be deleted at any time. The next start rebuilds the item index from the bucket, which takes about 45 seconds before the listeners open.
- **Housekeeping:** Cherry prunes idle tokens hourly and sweeps the chat log on its retention setting. It also writes token last-use times in batches.
- **Built-in limits:** frame and upload size caps, per-connection frame rates, per-address and per-account request buckets, and a concurrent-player cap. When the server is full, a player starting the app gets the game's own maintenance popup saying the server is full (with the player count), and keeps their login. Online players are never kicked.
- **Watching it:** refusals are counters on `/debug/vars` (`cherry.conn_refused`, `rate_limited`, `frames_dropped`, `frame_floods`), so the log stays small.
- **Security:** every gateway login must present the account's own session key. Handlers take the acting player from the session, never from a request field. The plain HTTP port serves an allowlist only, and uploads are bound to the signed-in account with quotas. The Staff Panel uses CSRF tokens, Origin checks, a strict CSP and an audit row for every change. The loopback admin listener refuses to bind a non-loopback address.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Repository layout

| Path | What it is |
| --- | --- |
| `cmd/cherry` | The server: configuration, database, asset store, listeners, housekeeping, shutdown. |
| `cmd/cherry-assets` | Asset tool: `plan`, `upload`, `verify`, `prune` and `convert` for the bucket. The only code that reads a local archive. |
| `cmd/cherry-import` | One-time importer from the old JSON stores into `cherry.db`. |
| `cmd/cherry-load` | Load generator (`seed`, `run`, `probe`, `wait`). **Run it only against an isolated test instance.** |
| `internal/config` | The settings table, flag/env/file resolution and startup checks. |
| `internal/store` | All shared state and the SQLite database: accounts, ledger, tokens, social, media, settings, migrations. |
| `internal/web` | The HTTP game API and route-level integration tests. |
| `internal/lpn` | Session and gateway protocols: Garden, Squares, Room Party, Circle, Treasure World, chat. |
| `internal/economy` | Shops, gacha, spins, quests, fishing, butlers, dyes, styling slots, calendars, mailbox, Circle and Treasure World rules, operator data. |
| `internal/room` | My Room furniture, presets, pets and the starter room. |
| `internal/auth` | Player sign-in (Discord, `dev-fake`), discovery, sign-up gate. |
| `internal/admin` | The Staff Panel (templates, sessions, pages). Only `cmd/cherry` imports it. |
| `internal/assets` | The S3/R2 client (stdlib SigV4), read-through cache, manifests, item index. |
| `internal/thumbs` | Generated item, pet, landscape thumbnails and dye masks. |
| `internal/itemconv`, `internal/itemcode` | The 2014 iOS item converter; item code ↔ numeric id and species rules. |
| `internal/opdata` | The strict operator-document loader and provenance checks. |
| `internal/httpx`, `internal/netsvc`, `internal/metrics`, `internal/testutil` | HTTP helpers and limits, DNS sink and certificates, counters, test helpers. |
| `data/` | Embedded operator documents, face code lists and menu icons ([Tabler Icons](https://tabler.io/icons), MIT). |
| `scripts/` | Lab helpers: `cherry-docs.py` (operator documents over the loopback API), `summarize-log.py` (method, path and status from a log), emulator launchers. |
| `.agents/` | Detailed engineering notes: protocol reference, feature status, client contract, dataset guide, testing notes. |

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Known gaps

These are known and documented, and each has a reason:

- **Story Gacha** is hidden because no original stories survive in any source found.
- **Fashion Shop SET tab** is empty because no original set contents were found.
- **Riding Pet race** runs on a stand-in map, since no original racing map pack is in any backup.
- **Lounge Chat stickers** draw blank, since the originals were not found.
- **Treasure World** is entered from the MENU. The 10.1 client ignores the link tapped inside a Square.
- **In-game web pages** (odds, help) need the patched client. The stock client's web view rejects self-signed certificates.
- **Asset store:** only Cloudflare R2 is tested. Other S3 providers need the region to be configurable ([DATASET §4](.agents/DATASET.md)).

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Troubleshooting

- **Cherry refuses to start** — read the one-line message: it names the bad setting or file. `cherry -h` lists every key. On a public server, check the certificate, the Discord settings and the session secret file (mode `0600`).
- **"Network Error" right at app start** — the client cannot reach the server: check the endpoint and port map the client was patched with, your firewall, and that the HTTPS certificate covers the host name.
- **Grey item pictures, empty shops, 404s for art** — the bucket credentials (`cloudflare.json`) are missing or wrong, or the dataset lacks those files. The log line at startup says whether the art checks ran.
- **"Maintenance in progress … The server is full"** — the player cap is reached. Raise **Maximum concurrent players** on the Settings page (live) or `limits.max-players`.
- **Players behind one address get "Network Error" or slow loads** — they share one connection and rate budget. Raise `limits.*` on the Settings page (live) and watch `cherry.conn_refused` and `rate_limited` on `/debug/vars`.

For anything else, join [our Discord server](https://discord.gg/E9NNwhJmqN) and ask in the support channel.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Reporting issues

Report bugs on the [GitHub issue tracker](https://github.com/festivities/Cherry/issues). Search existing issues first to avoid duplicates.

Please include what you did, what you expected and what happened, the Cherry commit you built (`git rev-parse --short HEAD`), the client platform, and the relevant log lines with method, path and status only. **Never attach** databases, raw logs (they carry cookies and tokens), credentials, game assets or your server's private details.

Client-side problems (patching, installing, signing in on the device) belong in [Raspberry](https://github.com/festivities/Raspberry/issues).

> [!CAUTION]
> Security issues: please don't post the details in a public issue. Report them privately through [GitHub's private vulnerability reporting](https://github.com/festivities/Cherry/security/advisories/new) (the repository's Security tab → "Report a vulnerability").

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Contributing

Pull requests are welcome. Before opening one:

1. Run [the gate](#2-run-the-gate). CI runs it too.
2. Keep to the dependencies: standard library only, apart from `modernc.org/sqlite`. Discuss any new module first.
3. Verify against the client: check every reply shape against what the client parses. The client crashes on a wrong JSON type and silently drops a protobuf frame with a missing required field. [.agents/REFERENCE.md](.agents/REFERENCE.md) has the per-feature protocol notes, and [.agents/AGENTS.md](.agents/AGENTS.md) has the architecture and rules.
4. Keep code and docs endpoint-agnostic. **Never commit** a real host name, port, address, Discord id, bucket name or secret, and never commit game assets, APKs, client binaries, credentials, databases or logs. The repository is public.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Documentation

- [.agents/AGENTS.md](.agents/AGENTS.md) — rules, architecture and the code map
- [.agents/REFERENCE.md](.agents/REFERENCE.md) — per-feature protocol reference and the client's known limits
- [.agents/PLAN.md](.agents/PLAN.md) — feature status with evidence, and the roadmap
- [.agents/CONTRACT.md](.agents/CONTRACT.md) — the interface with the Raspberry-patched client
- [.agents/DATASET.md](.agents/DATASET.md) — building the asset bucket from your own sources
- [.agents/TESTING.md](.agents/TESTING.md) — device testing notes

## License and legal

Distributed under the **GNU Affero General Public License v3.0**; see [LICENSE](LICENSE). If you run a modified Cherry as a network service, the AGPL requires you to offer its source code to your players.

Cherry is an independent, non-commercial fan project for game preservation and interoperability. It is **not affiliated with, endorsed by, or sponsored by** LINE Corporation (now LY Corporation), NAVER Corporation, or any of their affiliates. LINE, LINE PLAY and related names and logos are trademarks of their owners. Other names that appear in game content (for example Disney, SANRIO and Hatsune Miku) belong to their respective owners.

This repository contains only original server code and Cherry's own data. It does not distribute LINE's client, binaries, APKs or game assets. Operators and players must provide their own copies and are responsible for complying with the laws that apply to them.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

## Acknowledgments

- The players who donated device backups of their game files, which made most of the original content possible.
- [Raspberry](https://github.com/festivities/Raspberry), the companion client patcher.
- [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite), [Litestream](https://litestream.io/) and [Tabler Icons](https://tabler.io/icons).
- [Best-README-Template](https://github.com/othneildrew/Best-README-Template), which this layout follows.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

<!-- MARKDOWN LINKS & IMAGES -->
[discord-shield]: https://img.shields.io/discord/894925535870865498?style=for-the-badge&logo=discord&logoColor=white&label=Discord&color=5865F2
[discord-url]: https://discord.gg/E9NNwhJmqN
[gate-shield]: https://img.shields.io/github/actions/workflow/status/festivities/Cherry/gate.yml?branch=main&style=for-the-badge&logo=github&label=Gate
[gate-url]: https://github.com/festivities/Cherry/actions/workflows/gate.yml
[license-shield]: https://img.shields.io/github/license/festivities/Cherry?style=for-the-badge
[license-url]: https://github.com/festivities/Cherry/blob/main/LICENSE
[commit-shield]: https://img.shields.io/github/last-commit/festivities/Cherry?style=for-the-badge
[commit-url]: https://github.com/festivities/Cherry/commits/main
[issues-shield]: https://img.shields.io/github/issues/festivities/Cherry?style=for-the-badge
[issues-url]: https://github.com/festivities/Cherry/issues
[stars-shield]: https://img.shields.io/github/stars/festivities/Cherry?style=for-the-badge
[stars-url]: https://github.com/festivities/Cherry/stargazers
