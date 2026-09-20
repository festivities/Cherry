# Cherry — Current Plan (PLAN.md)

Living task list. Purge completed items; keep only active/next work. Full background and evidence in `.agents\AGENTS.md`.

## Status (2026-09-21)

**M0 ACHIEVED** (runtime evidence in cherry-m0c.log: setInitConf 200 consumed → splash + checkresource2.json follow-ons). Go server live: HTTPS :443 (legacy static-RSA TLS profile — client's OpenSSL offers no ECDHE), HTTP :80 (patcher/CDN), observers :10000/:10123. Static recon verified earlier (reports in `cherry\tools\{airarmor,http,lpn}-static\`).

## Milestones

- ✅ **M0 — Configuration consumed** (2026-09-21 02:41).
- **M1 — Guest session accepted and recoverable**: UI guest flow: `guest/generate` (digest validated) → `createSession` (cc=accessToken) → session consumed → authenticated follow-on; relaunch restores via `checkSession`. No hook-forced callbacks. **NEXT**.
- **M2 — Resources & transport accepted**: manifest/file acceptance; XTCP init/configure; gateway tunnel; ≥1 application login response accepted.
- **M3 — Initial lobby bootstrap**: avatar+scene render, interaction, stable ≥60s, relaunch works.

## Session B — complete M1 (NEXT)

1. **Serve the patcher chain** (game currently 404-stalled-ish on it after intro): `/notice/adr/checkresource2.json` (content-version manifest → tells the client whether to download updates; consumer = CDTMainVersionJson/GetContentsVersion — recover minimal schema from http-static/lpn-static reports + device files/version/*.ini format), `/v4/resource/splash/<ts>/0/JP` (splash resource — likely per-category version data; recover consumer). Decide values = "no updates needed".
2. **Login flow handlers** per the verified contracts: `POST /v4/account/guest/generate` (validate X-LINEPLAY-ACNT = md5hex(uniqueCode+"vocky7dkd722i1u8ak84o")), `GET /v4/checkSession` (403/3000 for unknown AV_AUTH; 200+session for valid), `GET /v4/createSession` (session result + quoted 128-char AV_AUTH Set-Cookie, Domain=.play.naver.jp).
3. **sckey.enc**: serve at play-static `/arts_session/sckey.enc` (or the path checkresource2.json directs) — 8812-byte blob per verified format.
4. **Fix the :10000 shadowing** (YunDetectService) so the gateway observer actually sees the game's banner-wait connections; decide whether M1 needs a minimal LPN banner+HO response (synthesizer: M1 boundary = session consumption + correlated transport activity; gateway connects at boot anyway, so correlation must be session-triggered).
5. UI-drive the login/guest path (user taps — intro complete, saved avatar persists in app data); capture uniqueCode/digest, cookie round-trip, XTCP 10123 activity.
6. Relaunch WITHOUT clearing data → checkSession restores session → M1.
7. Item serving (optional polish): map `/img/read/arts_item_<cat>_<id>/...` to the lpbackup item dirs — recover the URL→files mapping from downloader code or by observing post-login requests.

## Session C — M2 transport (not before)

`transport.go`/`transport_test.go`, in order: length-prefixed buffered reads; 15-byte banner `00 00 00 08 00 00 00…` / 11-byte client reply `00 00 00 04 00 00 01 0A 35 ED 6A` (byte order VERIFIED from STRB trace — Agent C's `…6A ED 35 0A` was reversed; still capture the live vector); gateway HO (`BE32(2) "HO"` — plaintext bootstrap, since the key table installs into the session agent type-0 only); session HI (`BE32(14) "HI" BE32(keyIndex) BE64(value)` with index 0/threshold 0) on the key-loaded connection; XTCP `LO` frame = `BE32(len+16) "LO" 0x10 flag BE32(payloadLen) BE64(txnId) payload` carrying JSON (sendInit: sessionId=aid, sessionKey, country, os, lpVer; configure parses pingSeconds/noopSeconds); keepalives ('M' ping sub 81/83, inbound 'M'+85 → disconnect); tunnel open type-1/ack-2; first login namespace (do NOT assume CIRCLE); encrypted mode-3 frames (`BE32(len)` plaintext + AES("LP"||BE64(sid)||BE16(msgid)||proto) from offset 4) only after receive-state semantics verified.

## Required runnable checks (encode as tests)

| Area | Vector |
|---|---|
| Guest MD5 | fixed UUID; exact UTF-8 concat; lowercase hex; negatives (hashing JSON body / newline / wrong secret) |
| HTTP | statuses; error fields top-level vs success under `result`; string ids; 403 branch; resume branch |
| Cookie | exact quoted wire header; 128-char token; parse round-trip |
| Blob | len 8812; threshold 0; 100 entries; lengths 64/16; independent decrypt of entry 0 == chosen ASCII |
| AES | CFB128 KAT; same frame twice → same ciphertext (per-frame reset, legacy) |
| Handshake | 15B banner; corrected 11B reply; 6B HO; 18B HI; fragmented/coalesced TCP reads |
| Frames | BE32 len counts ALL following bytes; BE16 msgid (non-palindromic test value); BE64 sid; truncation/oversize rejection |
| LO codec | distinct from protobuf framing |

## Strategic calls

- **No Frida in M0/M1 sessions.** Cherry's own HTTP logs expose the flow; static consumers answered structure. Instrumentation stays deferred (ptrace-child/Berberis uncertainty buys nothing for acceptance). Watch for AirArmor callback 10 (SUSPICIOUS_FRAMEWORK — abnormal, re-shows a per-frame popup that can obstruct UI): record it, never press its button; callback 12 alone is harmless.
- LPN game host = runtime CreateOption; gateway default `gws.play.naver.jp:10000` (GetGatewayServerPort 0x2a17a20). `tx.lbg.play.naver.jp` is an HTTPS API base, NOT the LPN socket.
- No native version/force-update gate exists (IsVersionUpdate 0x1b08eec is generic); boot unblocks on setInitConf→msg 10035.

## Open questions

- ⛔ AV_AUTH persistence round-trip (curl cookie-list → relaunch request) — **M1 acceptance blocker; runtime-verify**.
- ⛔ Live 11-byte handshake reply + banner — capture before freezing golden vectors (static order verified, W21/W22 context unread).
- XTCP server→client framing/wrapper (processPacket 0x1c3df04) and expected init reply — M2.
- Gateway key-table ownership (does anything install keys into the gateway agent?) — M2; HO avoids it.
- Mode-3 open/ack semantics under AES (tag/type overwritten by "LP") — M2.
- Agreements/profile side effects post-session (ReqTermInfo consumer) — add on demonstrated block only.
- `/etc/hosts` prior failure had a `shell_data_file` SELinux label — do not reopen; use the emulator `-dns-server` override (known-working).

## Explicitly deferred (do not build)

LINE OAuth, billing, ad SDK stubs, OBS media, FCM, crash collectors, minigames, chat, social/economy, full item catalog + 2014 import, all-messages protobuf recovery, persistent storage, Windows client port, Frida instrumentation (unless a specific client-state question requires it).
