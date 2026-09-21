# Cherry — Project Context (AGENTS.md)

Keep this file up to date: update as the LAST step of every meaningful change. It is the single source of truth for agents working in this repo.

## Mission

Reconstruct a Go server emulator ("Cherry") for LINE PLAY (jp.naver.lineplay.android), LINE Corporation's avatar social game (2012–2024). Initial milestone: the real Android client boots into a usable lobby. The eventual server goal is full game functionality; a successful lobby bootstrap is not completion of that goal. Windows client port comes later.

## Baseline (decided, do not re-litigate)

- APK: `D:\Dev\projects\Cherry\.opencode\line-play-artifacts\LINE+PLAY+-+Our+Avatar+World_10.1.0.0_APKPure.apk`
- v10.1.0.0, versionCode 254, minSdk 23, targetSdk 33. Final release.
- Complete single-file APK: 5179 entries, both arm64-v8a + armeabi-v7a libs.
- `libgame.so` (62,736,024 bytes) has the same uncompressed size as the archived device split. The original size comparison alone did not prove byte identity; compare hashes before relying on that stronger claim.
- Recon found no OBB/Play Asset Delivery requirement; the observed resource model is APK assets plus downloads into app `files/`. No OBB reconstruction is currently justified.
- Archive is READ-ONLY. Working copies go in `C:\Users\fes\AppData\Local\Temp\opencode\cherry\` (libs, dex, manifest, resources.arsc already extracted there; agent research artifacts in `...\cherry\tools\`).

## Evidence ledger confidence levels

`reported` → `static-confirmed` (symbol/address verified) → `runtime-confirmed` (captured from client). Unverified findings from the initial recon are `reported` until confirmed.

The 2026-09-20 audit sections below are historical snapshots, not current state. **M0 was achieved 2026-09-21.** At Session-B handoff Cherry and the emulator are stopped; restart lab DNS before the next controlled run.

**Audit correction (2026-09-20, superseded in part):** AirArmor bypass was never achieved. External capture is not an anti-tamper bypass.

## Architecture (from static recon; verify before coding)

### Client stack
- cocos2d-x 2.2.5, custom NHN layers (`arts`/`ArtsPf`), game ns `Na*`, net layer `XSystem`/`XInNetwork`. No Lua/JS/WebSocket.
- `libgame.so`: 119,419 functions, symbols NOT stripped, not obfuscated.
- JNI bridge: `com.nhnarts.message.PfQueueEvent` (~90 native methods, list in temp `tools\native_methods.txt`).
- Obfuscated DEX class `android.line.s.Service` loads libm0vie/libchromview via runtime-decrypted names (this is why glass DEX parsing fails; strings are plaintext, parser is strict).

### Protocol stacks (static-confirmed 2026-09-21; full reports in temp `cherry\tools\{airarmor,http,lpn}-static\REPORT.md`, main-agent spot-verified)
1. **Native HTTP/JSON** (libcurl static, jsoncpp): base `https://<host>/v4/`. fapi.play.naver.jp (game API), fapi-diary, event, face, gws, terms.line.me, notice2.line.me; CDNs play-static.line-scdn.net / play-static.line-apps.com, obs.line-{apps,scdn}.com. `tx.lbg.play.naver.jp` is an HTTPS API base (billing), NOT an LPN socket. 326 URL builders on `NaDataInvokerProtocol` (list: temp `cherry\invoker_short.txt` if present; addresses in http-static\ADDRESSES.txt).
2. **Session XTCP/JSON** — `session.play.naver.jp:10123` (XPN string, verified at 0x2f818b8). Frame `LO`: `BE32(len+16) "LO" 0x10 flag BE32(payloadLen) BE64(txnId) payload` (payload = JSON control: sendInit sessionId/sessionKey/country/os/lpVer; configure → pingSeconds/noopSeconds). Built by `LPNConnector::Send(raw)` 0x2a186a4 (callers: NaSessionManager::sendInit 0x1b30964, OnPushReqest).
3. **Game gateway LPN** — gws.play.naver.jp, **default port 10000** (GetGatewayServerPort 0x2a17a20 = `return 10000`; connected at boot by NaSingletonManager::Initialize 0x192087c; port runtime-overridable via CreateOption). Frame (both dirs): `BE32(len = ALL bytes after the length field) | tag | type | BE64(sessionId) | payload`; tag 'G'=game data, 'D','F','L','M','H' variants; type=(agenttype-1)<<3|subtype. Payload = **BE16(msgid)** || protobuf (msgid BIG-endian — corrects early recon; ids namespace-scoped; tables in lpn-static\dispatch_tables.tsv: CIRCLE 55, MONOPOLY 38, LPNP 36, SQUARE 133, GARDEN 82, TLUP 6 entries; outbound GetIndex == inbound dispatch index).
- **Mode 3 (AES-128-CFB128)**: BE32(len) stays PLAINTEXT; bytes from offset 4 encrypted: AES("LP" || BE64(sessionId) || BE16(msgid) || protobuf); EVP re-init per frame (CFB reset), same key+IV both directions; mode 4 = plaintext. Handshake: server 15-byte banner (`00 00 00 08 00 00 …`) → client 11-byte reply `00 00 00 04 00 00 01 0A 35 ED 6A` (order verified from STRB trace 0x2a0c8f0–0x2a0c918 — capture live vector before freezing) → mode 4 → server `HI` frame `BE32(14) "HI" BE32(keyIndex) BE64(threshold)` → client picks from its 100-entry key table → mode 3; `HO` = stay mode 4. Ping `BE32(2)|'M'|sub` (81='Q', 83='S'; inbound 'M'+85='U' → disconnect); keepalive timeout default 5000ms.
- **Key material**: hardcoded wrapper key ASCII "F609C5FCEFE3F62C6084…DE69C5" (128 chars @0x3008708, first 64 copied; AES-128 key = first 16 ASCII chars) + IV ASCII "B0F9926983812A17DF50A81500E67F34" (@0x3008790) decrypt the server-supplied `arts_session/sckey.enc` blob (404-tolerated) = `[u32][BE64 threshold][100×{BE32 len1,enc1,BE32 len2,enc2}]` → (key64,iv16) pairs; Cherry can ship its own blob.
- **Boot protobuf subset (proto2 optional, has-bits)**: `cs_login_req{1:sessionKey str,2:aid int64,3:version uint32,4:deviceType str,5:languageCode str,6:osType str}` (identical in CIRCLE/MONOPOLY/LPNP cr_login_req); `sc_login_res{1:result,2:enterType}`; `cs_room_enter_req{1:clientMapRevision i64,2:enterType}`; `sc_room_enter_res{1:result,2:npcList,3:clientMapRevision,4:mapFileName,5:playerType,6:circleMemberList,7:skinInfo}`; LPNP `rc_login_res{1:result,2:myroomExist bool,3:myroomTimeout i32}`. NO embedded descriptors (lite runtime); 3877 field statics in lpn-static\field_numbers.tsv.
- **AirArmor enforcement (verified)**: `onAirCallback`@0x2a2b234 (int only) → `SetAirArmorState`@0x200cf9c (write-once latch at +0x4C, only while normal). `IsAirArmorStateNormal`@0x200cf64: abnormal = {1 FAILED_CALLING, 4 ROOTING, 5 SUSPICIOUS_TOOL, 6 REPACKAGED, 9 INSTALLED_TOOL, 10 SUSPICIOUS_FRAMEWORK}; **FRIDA=12, XPOSED=13, DEBUGGER=8 are NORMAL**. Abnormal → ShowAirArmorDetectPopup@0x200cdf0 only (per-frame re-show; button → scene teardown). NO process kill. Callback 12 alone does not latch abnormal; **callback 10 co-fired with Frida and caused [AA-010]**. Keep frida-server stopped in Session B. A clean run without Frida is not a bypass. Detection implementation is inside packed libm0vie.so (entropy 7.8+, invalid JNI_OnLoad bytes; loaded t+0.177s) — not statically extractable; the ptrace self-tracing child (maps only libm0vie) remains unattributed (breakpad is the only proven ptrace user in libgame).

### Key symbols (arm64, from recon; verify image base)
- `LPNAgent::MakeStream` 0x2a11ca0, `LPNAgent::Update` 0x2a10b78
- `NaDispatcherCommon::ResCreateSessionWithToken` 0x1c3c944 (session JSON contract)
- `LP_sc_login_res` CIRCLE 0x2514930 / MONOPOLY 0x16aaf8c
- `LPN::SessionKey` ctor 0x2a19020, Init 0x2a18ff8
- `LPN::AESCrypto::Encrypt/Decrypt` 0x2a191cc / 0x2a19100
- `NaDataInvokerProtocol::baseHostName` 0x1ac4bb8 (host table BSS 0x3BE4590)
- `LPN::LPNConnector::Send` 0x2a18590 / 0x2a186a4
- `ReqCreateSessionWithToken` 0x1c05074, `ReqAccessTokenForGuest` 0x1bbf454
- `LPNAgent::SendPing` 0x2a139e4
- IDA session id (recon, may be stale): `2f8772ca`

### Auth (static-confirmed 2026-09-21)
- LINE SDK v3 embedded (channel-apis.line.naver.jp); channel ID 1350636423. NOT emulating LINE OAuth — local guest route: UI selector **`LD_GUEST`** (string @0x2f8b2e0; NaLoginLayer::MakeLoginButton 0x1cfe130, SNS list-driven layouts SetSnsLoginUITagList 0x1cfe4d0). Guest uniqueCode = device UUID via LoginAdapter.InfoDeviceUUID (saved pref `uuid`, else name-based UUID from ANDROID_ID).
- Guest: POST `https://fapi.play.naver.jp/v4/account/guest/generate` body `{"uniqueCode":"…"}` header `X-LINEPLAY-ACNT = lowercase_md5hex(uniqueCode + "vocky7dkd722i1u8ak84o")` (secret verified @0x2f7d910) → `result.provider` (string, we use "GUEST"), `result.accessToken`.
- Session: GET `/v4/createSession?nationCode=CC` with cookies cc(=accessToken),expireAt,refreshToken,provider,udid,os,device (+cs unless TW) → `result.{sessionKey,mid,avatarUserId,aid,lineId,lineName,termAge}` (all strings; aid nonzero decimal — "0" suppresses NaSessionManager::Create) + **response-header Set-Cookie `AV_AUTH="<quoted value>"`** — client extraction uses `AV_AUTH\t"` … `"` delimiter (string @0x2f84140) and requires value >100 bytes; persisted as the long-term credential; on relaunch cc carries the saved token.
- Boot: `SplashScene::setupPatcher` → GET `/v4/setInitConf` → msg 10035 (M0). Runtime then GETs `/v4/resource/splash/<ts>/0/JP` and static-CDN `/notice/adr/checkresource2.json`. **checkresource2 404 is fatal** (network-error dialog). `CDTMainVersionJson::Request` 0x1b27be8 = `<staticDomain>notice/adr/checkresource2.json`. Parse 0x1becee0: `root["adr"]["10.1.0.0"]` non-empty string, then `root["resource"][<that string>]` with keys animation/he/ch/sound/subui/tx/squareui/ui (atoi, missing→0). No `result` wrapper. Failure → ProcessError → msg 10686. Splash skip fixture: `resourceID: -1` (0 is a different branch). Fresh install cannot skip animation/sound/tx/ui **list** downloads (`RequestVersionIni` 0x1cf9da0). Pack key `"1"` is synthetic — not item-format 1409. checkSession unchanged.
- **TLS (verified — pinning does NOT exist)**: native curl verification fully disabled (`CurlWrapper::InitParam` 0x2a027fc setopt(64,0)+setopt(81,0); repeated in HttpSender 0x2ed3018; no CAINFO/CAPATH) → **any self-signed cert works for all native game hosts**. Java: jadx-verified `LinePlay.setSSLSocketFactory` installs `TLSSocketFactory` = TLSv1.1/1.2 protocol enabler with default platform trust (`SSLContext.init(null,null,null)`); `res/raw/https_keystore` is referenced ONLY by R classes = **dead resource, never used by code**. Earlier "TLS pinning blocks Cherry" claim is dead. (targetSdk 33/no NSC → user CA store untrusted by Java stack — only matters for relayed hosts, which use live infra anyway.)
- Root check: LoginAdapter.checkRootingDevice (non-fatal in practice — game runs rooted).
- `libline-sdk-encryption.so` `Lspg.gk()` mixes APK signing cert + ANDROID_ID with 2 hardcoded secrets — LINE-SDK-login-specific; guest path insulated from it.

### Assets
- APK `assets/` = 2873 files (12.3MB), plain PNGs, no PVR/CCZ. Organized dump `lineplay-original-assets/android-10.1.0.0-2024/` == APK assets extracted.
- Streamed set (device backup `lpbackup/.../files/`, 14,005 files / 359MB): UIResource 7557/7557, animation 4616/4616, sound 483/483 — all MD5-verified complete vs manifests in `files/version/*.ini` (format: newline `MD5_HEX_UPPER:/relative/path`; `<category>lastversion.ini` = u32 LE version). Path mapping: `/X.plist`→UIResource/, `/X.png`→animation/, `/BGM_x.ogg`→sound/.
- `.ast` = SQLite (`localString(stridx INTEGER UNIQUE, local TEXT)`). `.artsitem` = semicolon CSV, variable fields. `.avb/.avc` custom skeleton/frame binaries (reader in libgame.so). `.artsg*` = 2014 fixed-width catalogs.
- Item textures: recon counted only 191 item dirs in the archive's device backup; catalogs are server-served. The 2014 iOS dump (`ios-mini-4.3-2014/item.zip`, ~4400 items) may supply compatible assets. Missing cosmetics cannot be assumed harmless to a required avatar/scene until tested.
- Client resource flow: checkresource2 JSON (no result wrapper) → category `arts_<cat>_ini_<ver>/update_<lang>.ini` (probe `[]`, never 0-byte) → files. Backup lastversion u32 LE: animation 531, sound 530, subui 430, tx 540, ui 540 describe lpbackup, not this device (no `files/version/`). Lang suffix `jp` vs `ja` is a first-run probe.
- Splash skip (static-confirmed): `ResCheckSplashDownload` 0x1b7d39c — serve `{"result":{"update":false,"resourceID":-1,"resourceImageCount":0,"resourceImages":[]}}`.

## Tooling rules

- Prefer `idalib` MCP tools for native analysis; `glass` only where idalib is incompatible. Glass APK parsing failed while lifting classes2.dex (`Invalid string in class name`); that failure alone does not prove deliberate obfuscation is the cause. Python/androguard were used for DEX recon; artifacts are in temp `tools\`.
- android-mcp tools for device interaction; glass frida-* tools for instrumentation.
- Temp scratch dir: `C:\Users\fes\AppData\Local\Temp\opencode\` (cherry\ = extracted APK parts; cherry-asset\ = asset dissection).
- For DEX/Java work prefer `jadx-mcp`; Androguard is the fallback if `jadx-mcp` is unavailable.
- Use short, bounded commands (normally 10 seconds for status probes). No unbounded boot loops, repeated multi-minute tshark retries, or background commands that retain the shell's output handles. Use `workdir` rather than cmd `cd`; use dedicated read/edit/search tools.
- The main model can inspect images returned by tools. The prior claim that it cannot see screenshots is obsolete. If a tool fails, record the tool error rather than infer the UI from logs alone.

## Verified AirArmor evidence — bypass still unresolved

Artifact root below: `C:\Users\fes\AppData\Local\Temp\opencode\cherry\`.

- `aa-capture\logcat-game.txt:32`: `AirArmor: Phase = RELEASE` in the captured game process (PID 20374).
- `aa-capture\logcat-game.txt:270`: epoch `1789854802.613`, `Air Callback:AIR_CALLBACK_DETECTED_FRIDA,12,Frida`.
- `aa-capture\logcat-game.txt:272`: epoch `1789854804.612`, `Air Callback:AIR_CALLBACK_SUSPICIOUS_FRAMEWORK,10,Frida`.
- `aa-capture\logcat-game.txt:274`: `AIR_CALLBACK_DONE` follows those detections. DONE is a completion callback, not proof of a clean result or bypass. Detection appeared roughly 56 seconds after process creation; an immediate smoke test is insufficient.
- `tools\airarmor-evidence.txt:330–374` records a historical self-tracing pair: parent PID 10431 had `TracerPid:10472`; child 10472 had `PPid:10431`, `TracerPid:0`, and a wait4 syscall. This is evidence of a ptrace relationship. Its exact role in failed Frida attaches is not yet isolated.
- The child mapped `libm0vie.so` but not `libgame.so`/`libchromview_android.so`. This makes the early libm0vie loading stage a candidate watchdog origin, not a proven detection function. Do not kill the child or patch a guessed address on that inference alone.
- Recon identified result-handling symbols: `PfQueueEvent_onAirCallback` at `0x2a2b234`, `NaExceptionManager::SetAirArmorState` at `0x200cf9c`, `IsAirArmorStateNormal` at `0x200cf64`, `ShowAirArmorDetectPopup` at `0x200cdf0`. Silencing result UI would not necessarily remove ptrace ownership or native inspection.
- `tools\airarmor-diagnose.js` exists, explicitly says diagnostic-only and not exercised in LINE PLAY, and does not change return values. No saved successful game-hook/bypass run was found. Earlier SystemUI attach/script success is only a Frida infrastructure smoke test.
- Berberis is a separate instrumentation issue: host x86_64 code and translated ARM64 guest code are different hook targets. Direct ARM64 `libgame` Interceptor support has not been demonstrated. Do not attribute every failed script/session to AirArmor.

## Task 3 evidence — partial startup capture

- Independently re-read `aa-capture\boot.pcap`: **168,152 bytes, 435 records**, Linux SLL2; SHA-256 `b48ce1f0f925baab0fb0fef4fc7c9977298092b64b07dce0ad2389a96e1b5fed`.
- Saved supporting files: `aa-capture\sockets.txt`, `logcat-game.txt`, `dns-server.log`. The capture is device-wide; do not attribute every packet to LINE PLAY. Socket sampling started after launch and does not attribute every short-lived startup flow.
- First observed DNS queries include Facebook/Adjust SDKs, then `fapi.play.naver.jp` at epoch `1789854749.555`, `lan3rd.line.me` at `1789854749.871`, `play-static.line-scdn.net` at `1789854750.784`, and `media-pu.line-scdn.net` at `1789854761.337`. Responses for fapi/play-static/media-pu were NXDOMAIN in this capture.
- Complete captured ClientHellos contain SNI for `graph.facebook.com`, `app.adjust.com`, and `lan3rd.line.me`, all on port 443. The earlier `pcap2.py` SNI parser incorrectly read the hostname length without skipping `name_type`; its blank SNI output is invalid. It also skipped IPv6. Audit script `C:\Users\fes\AppData\Local\Temp\opencode\cherry-status-audit.py` independently parses DNS/IP/TLS with dpkt and a checked server-name decoder. It does not perform TCP reassembly; one captured IP record is too short to decode.
- `logcat-game.txt:234–247` records GET `https://lan3rd.line.me/v1/lineplay/android?notificationLocalRv=0&noticeNewTerm=3`, status 200. This is an original LINE notice service response, **not a Cherry response** and not M0.
- The same run logs language changes (`en`, then `ja`) and `NoticeNotificationActivity` at lines 260–264, followed by AirArmor detections. `setAppLanguageType` does not prove it stalled at a language-selection popup. A separate earlier screenshot did show language selection; do not conflate separate runs.
- Neither `session.play.naver.jp` nor `tx.lbg.play.naver.jp` appears as a DNS query in this capture. This is a scoped absence, not proof they are never used or that the exact blocking gate is known. Loopback ports in the capture are not established LPN ports.
- **Missing:** actual game API request paths/methods/headers/bodies, evidence linking URL builders to the observed requests, startup response contracts, complete gate order, and the LPN endpoint/port. The original Task 3 acceptance criteria are not met.

## Task 5 evidence — partial DNS work, no M0

- Historical `aa-capture\dns-server.log` shows A/AAAA sinkhole handling during a run launched with `-dns-server 192.168.1.7`. `gws.play.naver.jp` was queried at 13:11:13. A DNS query alone does not demonstrate further UI/protocol progress.
- Host experiment: `C:\Users\fes\AppData\Local\Temp\opencode\cherry-dns.py`, UDP 53, game-domain suffixes -> `10.0.2.2`, other queries relayed to `1.1.1.1`. At audit, PID 3020 still owns UDP 53 and a bounded host-local DNS query returns `10.0.2.2` for fapi. This temporary script is not production-ready or durable configuration.
- **Device routing is currently not working:** `ping -c 1 -W 1 fapi.play.naver.jp` returns `unknown host`. The current emulator command line has no `-dns-server` override. No emulator restart/routing change was performed during this audit.
- The prior conclusion that Android 17/Bionic ignores `/etc/hosts` universally is unsupported. The failed experimental hosts replacement had SELinux label `shell_data_file`; labeling, mount namespaces, or resolver behavior could explain failure. Current `/etc/hosts` is stock, labeled `system_file`, containing only localhost entries.
- No host TCP listener on 80 or 443 was found during audit. Repository contains no Go implementation. No successful local TLS handshake, validated trust modification, game request received by Cherry, or client-accepted Cherry response is documented. **Task 5 remains partial; M0 is not achieved.**

## Environment and historical baseline validation

- Host: Windows; Java/Python/Node available. Glass executable `C:\Apps\Glass\glass-v0.5.0-windows-amd64.exe` uses Frida 17.9.5. Recon found SDK `build-tools\36.0.0\apksigner.bat` and `zipalign.exe`; do not assume signing tools are absent.
- **Live audit state (2026-09-20):** `emulator-5554` is online, Android 17/API 37, ABI list `x86_64,arm64-v8a`, native bridge property `libndk_translation.so`. Historical game logs identify Berberis 16.0.0. ADB currently runs as shell (uid 2000); neither LINE PLAY nor frida-server has a PID. Glass reports `attached:false`.
- Current emulator command: `emulator.exe -netdelay none -netspeed full -avd Cherry` (QEMU equivalent also checked). No explicit GPU or DNS override; effective GPU configuration was not revalidated in this audit. Do not describe an old launch command as current state.
- **Historical graphics workaround:** boot failed with `IllegalArgumentException: No config chosen` on API 30 and 37; `-gpu swiftshader_indirect -no-snapshot-load` allowed native rendering and a language-selection UI. The exact rejected EGL attributes were not captured; the earlier RGB565 diagnosis was speculative. Keep the known-good launch flags for future controlled tests.
- **Historical baseline:** `Cocos2dxRenderer SurfaceCreated nativeInit:true` and an earlier screenshot establish rendering, not login/lobby or an anti-tamper bypass. Old Android-11 app-data backup: `C:\Users\fes\AppData\Local\Temp\opencode\lineplay-data-backup.tar`.
- Previously computed SHA-256: APK `aa6495a0430beef0c9a719a1f09fe836ff1c82a0f4f96235d47fffda070b45d1`; arm64 libgame `d824c3ef72ac1948f3c5a31619879384f4659c9306e6f5a1b04793334e795b04`. Other native hashes are in `tools\airarmor-evidence.txt`. Signing-certificate fingerprint was not completed in the visible Task 1 work.
- **Frida:** `/data/local/tmp/frida-server` exists (110,825,032 bytes, executable) but is not running at audit. No re-push is currently required. Host copy: `C:\Users\fes\AppData\Local\Temp\opencode\frida-server`. Raw Glass scripts previously reported `Java is not defined`; bridge loading must be handled explicitly. The compiled bridge file is not evidence of successful loading into LINE PLAY. Prior failed scripts also used obsolete Frida API forms; do not treat those failures as protection evidence.
- **android-mcp:** device enumeration works; current Snapshot fails with UIAutomator `ApplicationSharedMemory not initialized`. This is a tool/runtime error, not evidence about LINE PLAY's UI or model vision. The foreground activity from ADB is the launcher. No game launch, Frida attach, reboot, root toggle, TLS change, or anti-tamper modification was performed during the audit.
- glass MCP has no frida-server install verb (that's GUI-only, `crates\glass-frida\src\server.rs`); server lifecycle is manual as above. Glass source available at `D:\Dev\projects\Glass` for reference.

## Status

- **M0 ACHIEVED (2026-09-21 02:41, runtime-confirmed)**: client `GET /v4/setInitConf?nationCode=us&appVer=10.1.0.0` → 200 consumed (follow-ons: `/v4/resource/splash/<ts>/0/JP` — nationCode JP from OUR fixture — and play-static `/notice/adr/checkresource2.json`). Logs: `C:\Users\fes\AppData\Local\Temp\opencode\cherry-m0c.log`.
- Go server: repo root, stdlib only (`main.go`, `http.go`, `cert.go`, `http_test.go`). Listens HTTPS :443 (dual RSA-2048 + ECDSA-256 certs, **explicit CipherSuites incl. static-RSA AES-GCM/CBC** — the client's ancient OpenSSL offers ONLY static-RSA suites; Go 1.22+ default suites fail it; the `tlsrsakex` GODEBUG is REMOVED in Go 1.27 but explicit CipherSuites still works), HTTP :80 and HTTPS :443 share the mux (intro items were HTTP; captured checkresource2 was HTTPS). TCP observers :10000/:10123. Patcher fixtures in `http.go` (unit-tested; not device-proven). Run via PowerShell Start-Process w/ output redirect.
- **Verified boot flow (runtime)**: goodbye 404 tolerated → lan3rd notice dialog (dismiss "閉じる" at ~760,1461) → intro (Cherry mascot; dress-up fetches `/img/read/arts_item_<cat>_<id>/1409_iteminfo.artsitem` via :80, 404-tolerated) → 「LINEプレイの世界へ！」 → **setInitConf 200** → splash resource + `/notice/adr/checkresource2.json`.
- **AirArmor [AA-010] is a UI blocker, not a process kill.** Callback 12 (DETECTED_FRIDA) is "normal"; callback 10 (SUSPICIOUS_FRAMEWORK, data string `"Frida"`) is abnormal and shows `お使いのデバイスでは安定的なサービスの提供が困難なためアプリをご利用いただけません。 [AA-010]` / 確認 → scene teardown. Cause: **frida-server running on the device**. Mitigation: stop it during client runs; binary renamed to `/data/local/tmp/.fs_archive` (host copy still at `C:\Users\fes\AppData\Local\Temp\opencode\frida-server`). After kill, a relaunch (pid 7426) had **no** Frida/framework callbacks. Callback 11 SUSPICIOUS_NETWORK is also "normal" (does not latch).
- **YunDetectService.exe is Baidu (Pan Baidu downloader), unrelated to LINE PLAY. Keep it running.** It binds `127.0.0.1:10000`. QEMU maps guest `10.0.2.2:10000` (game LPN gateway) onto that loopback port, so Cherry's wildcard `:10000` observer never sees the game. Do **not** kill/disable YunDetect. Defer a non-destructive fix until transport work (e.g. serve the gateway on another port if the client can be told via session/config, or a temporary stop only if the user agrees for a transport session). HTTP :80/:443 are unaffected.
- **Post-M0 stall:** checkresource2 404 → network-error dialog. Fixtures are now in code; next is a device probe (PLAN Session B step 3). Keep YunDetect running. Keep Frida stopped.
- IDA verification session `b61900c0`. Reports: temp `cherry\tools\patcher-static\`. Prefer jadx-mcp over Androguard when JADX is up.
