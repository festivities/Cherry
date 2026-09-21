# Cherry — Current Plan (PLAN.md)

Living task list. Purge completed items; keep only active/next work. Full background and evidence in `.agents\AGENTS.md`.

## Status (2026-09-22)

**M1 ACHIEVED (2026-09-22, runtime-confirmed).** Guest generate → createSession → avatar-creation scene reached; force-close+relaunch skips login (checkSession + AV_AUTH round-trip). Patcher gate, string table (`arts_tx` 00543 record grammar), diaryskin `[empty]`, UIResource seeding (santi backup → device) all cleared along the way. In-memory account state (restart of cherry.exe logs the client out — known, accepted for M1). **Next: M2 transport** (Session C). Artifacts are now OUT of git history (force-pushed, gitignored, LFS abandoned).

## Milestones

- ✅ **M0 — Configuration consumed** (2026-09-21 02:41).
- ✅ **M1 — Guest session accepted and recoverable** (2026-09-22). Guest generate → createSession → avatar creation; relaunch checkSession via persisted AV_AUTH. `aid:"0"` routes fresh guests to avatar creation; `social/terms/sns` `{"result":true}` breaks the login loop.
- **M2 — Resources & transport accepted**: manifest/file acceptance; XTCP init/configure; gateway tunnel; ≥1 application login response accepted.
- **M3 — Initial lobby bootstrap**: avatar+scene render, interaction, stable ≥60s, relaunch works.

## Session C — M2 transport (NEXT)

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
- `/etc/hosts` prior failure had a `shell_data_file` SELinux label — do not reopen; use the emulator `-dns-server` override (known-working).

## Explicitly deferred (do not build)

LINE OAuth, billing, ad SDK stubs, OBS media, FCM, crash collectors, minigames, chat, social/economy, full item catalog + 2014 import, all-messages protobuf recovery, persistent storage, Windows client port, Frida instrumentation (unless a specific client-state question requires it).
