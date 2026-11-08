# Cherry ⇄ Raspberry contract — server half (CONTRACT.md)

The interface between a Cherry server and a client patched by **Raspberry** (`D:\Dev\projects\Raspberry`, GitHub `festivities/Raspberry`, public; its own Claude Code session since 2026-11-09). **This file is authoritative for the server half and for the shared definitions** (host set, endpoint, auth endpoints, discovery, modes). The client half is `D:\Dev\projects\Raspberry\.agents\CONTRACT.md` (read-only for the Cherry session). Status: **contract 0 — draft proposed 2026-11-09 by the handoff author**; Raspberry's S1 spike and the first auth slice turn it into **contract 1**. Every rule is **proposed** until both files say **agreed**, and **implemented** only after a device run.

## How the two sessions use this

- Never edit the other repo's files. Ask for changes in "Requests to Raspberry" below (append-only, dated, short); the Raspberry session reads this file at the start of each session. Replies and requests from it are in its file's "Requests to Cherry": read it at the start of each session and before every auth slice. The user relays anything urgent.
- When you change a shared rule, bump `contract`, add a changelog line and say so in "Requests to Raspberry".
- Production values (the user's domain and ports) are private (`%LOCALAPPDATA%\Cherry\deploy\oci.md`, memory `oci-deployment`) and never appear in this public repo. Everything here is operator configuration, never hard-coded.

## 1 Terms

- **Endpoint:** the Cherry server a patched client talks to: a host (DNS name or IPv4 literal) plus a **port map**: the client's own destination port → the server's port, for 80, 443, 10000 and 10123 (identity by default). The operator passes it to the patcher; the client never learns it from the server.
- **Host set** (what the hook redirects; mirrors the lab-proven DNS sink, `internal/netsvc/dns.go` `dnsSinkSuffixes`/`dnsSinkExact`): names equal to or ending with `.play.naver.jp`, `.play.naver.net`, `.line-scdn.net`, `.line-apps.com`, `.line.naver.jp`, plus the exact names `ads.play.naver.jp` and `lan3rd.line.me`. The certificate list `internal/netsvc/cert.go` `certDNSNames` is the set of names the lab has seen. Observed in lab logs (360 files, 2026-11-09 sweep): `fapi.play.naver.jp`, `fapi-diary.play.naver.jp`, `play-static.line-scdn.net`, `obs.line-apps.com`, `obs.line-scdn.net`, and `notice2.line.me` (156 plain-HTTP requests, probably from the physical-phone era; it is not in the sink set [?]). Raspberry's S1 finalizes the list.
- **Patched client:** a client patched by Raspberry. **Stock client:** the unmodified one (the emulator lab).
- **Operator hostname:** the endpoint host, served with a publicly trusted certificate. Cherry knows its own hostnames from configuration (`server.hostnames`).

## 2 Redirect: what the server must do

1. Listen on the four client-facing ports of the port map (HTTPS, HTTP, session, gateway) and, separately, the admin port; all configurable (flags/environment/config file), none hard-coded. (Today they are constants: slice 8.3a.)
2. **Keep every reply unchanged:** names and ports in `setInitConf` and every other reply stay the originals (`session.play.naver.jp:10123`, `gws.play.naver.jp` and so on). The patched client maps them; the server never rewrites addresses for it. The DNS sink and `sinkIPFor`/`lanIP` logic stay lab-only and are never enabled on a public host.
3. **Keep routing on Host/SNI as today,** with per-SNI self-signed certificates for the host set (the native client does not verify). The SNI filter must also admit the operator hostname(s).
4. Plain HTTP on the mapped port 80 must keep serving `GET /img/read/notice/*`, `/img/read/arts_item_*` and `notice2.line.me /v1/lineplay/android` (the client uses them; notices answer 404 today).

## 3 Authentication (the public deployment is Discord-only)

**Modes** (`auth.mode`): `discord` (default for any public server; refuses to start without Discord client id, secret and redirect URLs), `lab-guest` (today's device-bound guests; the stock-client lab and isolated Raspberry runs), `dev-fake` (Discord replaced by a fake provider, tests only). `lab-guest` and `dev-fake` refuse to start when `server.public=true`.

**Server rules in `discord` mode:** `POST /v4/account/guest/generate` accepts only a `uniqueCode` that `/auth/v1/exchange` minted (plus the existing `X-LINEPLAY-ACNT` header check); any other code is refused with an HTTP 4xx whose body shape is fixed after S1 shows what the stock client does with an error. One Discord identity maps to one account; a minted code is stored only as a SHA-256 hash; a new code is minted per exchange and the identity may hold several (cap 10, oldest dropped); an admin can revoke all codes of an identity. Accounts are still created by the stock `create/avatar` flow (aid `"0"` until then). Banned identities are refused at `callback` and `exchange`.

**Endpoints** (on the game HTTPS port, operator hostname, publicly trusted certificate; every response `Cache-Control: no-store`, `Referrer-Policy: no-referrer`; query strings of `/auth/` redacted in logs):

```
GET  /auth/v1/discord/start?challenge=<c>&return=<r>&state=<s>
GET  /auth/v1/discord/callback?code=<d>&state=<server state>       (Discord → server; the client never calls it)
POST /auth/v1/exchange        {"ticket":"…","verifier":"…"}         → {"uniqueCode":"<uuid>","aid":"<id or 0>","new":<bool>}
```

- `start`: `challenge` = base64url (no padding) of SHA-256(verifier), exactly 43 chars; `state` = base64url, 16–64 chars; `return` must be exactly `raspberry://auth` or `http://127.0.0.1:<port 1024–65535>/raspberry/cb`; anything else is a 400 page and never a redirect. The server stores the attempt (challenge, return, client state, its own random `server_state`, IP, created; TTL 10 min; cap open attempts per IP) and replies 302 to `https://discord.com/oauth2/authorize?response_type=code&client_id=<id>&scope=identify&redirect_uri=<registered callback URL>&state=<server_state>` (the `prompt` parameter per Discord's docs; default for players: leave it out; admin logins use `prompt=consent`; check both behaviors on a device).
- `callback`: look up `server_state` (unused, unexpired); exchange the code at `https://discord.com/api/oauth2/token` (`application/x-www-form-urlencoded`: client_id, client_secret, grant_type, code, redirect_uri); read `GET https://discord.com/api/users/@me` (`identify` returns `id`, `username`, `global_name`, `mfa_enabled`); revoke the Discord token (`/oauth2/token/revoke`, best effort) and never store it; link or look up the account by Discord `id`; mint a **ticket** (256-bit random, single use, TTL 120 s, bound to the attempt) and redirect to `<return>?ticket=<t>&state=<client state>`, or `<return>?error=<denied|expired|banned|server>&state=<client state>` for a valid return, else a small HTML error page (CSP, no script).
- `exchange`: look up the ticket (exists, unused, unexpired), compare `SHA-256(verifier)` with the attempt's `challenge` in constant time, mark the ticket used atomically, mint a UUID-shaped CSPRNG `uniqueCode` (stored hashed), reply 200. Errors are JSON `{"error":"invalid_request|invalid_ticket|rate_limited|banned"}` with 400/401/403/429.
- Starting limits (tunable): `start` 10/min and 60/h per IP, `exchange` 20/min per IP, bounded open attempts and tickets, expired rows purged on a timer.
- The Discord application's redirect URIs are exact strings registered by the operator (Discord does not document wildcard matching: do not rely on any).
- **Admin sign-in is separate** (own listener, own cookie, own redirect URI, allowlist + `mfa_enabled`, PLAN Roadmap 8 P4) and is not part of this contract.

## 4 Discovery (optional for clients, required for servers)

`GET /cherry/v1/info` on the game HTTPS port: public, no cookies, rate-limited, tiny JSON, no secrets and no internal addresses: `{"contract":1,"name":"<operator display name>","server":"cherry <version>","auth":["discord"],"ports":{"80":<p>,"443":<p>,"10000":<p>,"10123":<p>}}`. `auth` lists the enabled modes (never `lab-guest`/`dev-fake` on a public server). A client may use it to validate an endpoint; it must not follow its hosts (only ports, and only as a hint).

## 5 Web pages

The patched client swaps only the authority of LINE web URLs (`event.play.naver.jp`, `help2.line.me`; S1 completes the list) for `<operator hostname>[:<mapped 443>]` and keeps path, query and fragment. The server serves those paths with Cherry-made, operator-editable pages (initially the Lucky Spin, Jackpot and gacha odds pages `/web/probability/…` and the HELP pages `/lineplay/<device>/categoryId/…`). The pages must work without third-party scripts.

## 6 TLS and certificates

- The operator hostname uses an **RSA** certificate obtained by an external ACME client (DNS-01) and supplied as PEM files; Cherry reloads them without restart. No ACME client inside Cherry.
- Native-client listeners keep the static-RSA cipher suites (explicit `CipherSuites`, the client's old OpenSSL needs them); browsers and WebView also get modern suites. The admin listener offers modern suites only.
- Per-SNI self-signed certificates for the host set stay as today; SNI `lan3rd.line.me` stays blocked.

## 7 Security requirements on the server side of this interface

Treat every client as hostile: a modified client exists whatever Raspberry ships. Authorization on every route (the actor comes from the session, never from a request field); size, count and rate limits on all input, including LPN frames (caps, read/idle deadlines, per-IP connection limits); constant-time secret comparisons; no open redirects; `ticket`, `verifier`, `uniqueCode`, OAuth `code` and Discord tokens never in logs, errors or the DB in clear (hashes only); Discord client secret in a 0600 secret file or environment variable, never in Git; the DNS sink is never reachable from the internet.

## 8 Lab compatibility

The stock-client emulator lab keeps working through the refactor in `lab-guest` mode on the Windows dev instance. The JSON importer is a lab convenience only; production accounts start fresh (user, 2026-11-09).

## 9 Test aids

- **`dev-fake` provider:** `GET /auth/v1/discord/start` skips Discord and redirects straight to `/auth/v1/discord/callback?code=dev:<name>&state=<server_state>` with `<name>` from `?as=<name>` (default `dev`); the callback treats `dev:<name>` as the identity `dev-<name>`; everything after is identical. Raspberry's stub implements the same behavior.
- **`CHERRY_FAKE_NOW`** moves the game clock on a data copy (AGENTS "Workflow", "Season and date tests").

## 10 Requests to Raspberry (append-only; newest last)

- **2026-11-09 (seed from the handoff author).** From S1 please deliver, in your CONTRACT "Findings": the final host list (which suffixes the client really uses, any host built at run time), the exact location of the `uuid` pref, what the stock client does after a refused `guest/generate` (so the server can answer cleanly in `discord` mode), the return channel you chose (loopback or `raspberry://auth`), whether ports 10000/10123 can be remapped, and which in-app web URLs exist (host, path pattern, who builds them).

## 11 Changelog

- 2026-11-09 — contract 0 drafted (host set, port map, auth endpoints, modes, discovery, web pages).
