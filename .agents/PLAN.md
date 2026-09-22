# Cherry — Current Plan (PLAN.md)

Milestone-boundary handoff; no next-milestone work has begun. Architecture, exact response bodies, and history: `.agents\AGENTS.md`.

## Status (2026-09-22)

- **M0 COMPLETE — runtime-confirmed 2026-09-21:** client accepted Cherry's boot configuration.
- **M1 COMPLETE — runtime-confirmed 2026-09-22:** guest login reaches avatar creation; checkSession + persisted AV_AUTH works on relaunch.
- **M2 ACHIEVED / COMPLETE — runtime-confirmed 2026-09-22:** avatar creation + completion/gift + transition through login preload/session initialization into the Garden loading phase. **Not a usable lobby; the project's initial lobby goal remains unmet.**

## M2 acceptance and handoff evidence

- User completed avatar setup; console recorded `POST /v4/create/avatar` 200 and `POST /v4/create/complete` 200. Spinner cleared, 300-Gem gift appeared, and user pressed START. Setting and friend preload jobs advanced. **User-reported visual evidence:** a sprite image reading **“Garden”** appeared immediately before the crash; this is not an HTTP string.
- At host `19:42:25.262533`, console captured `SESSION lo ... op=0x00 txn=1 payload={"country":"","lpVer":"adr 10.1.0.0","os":"adr 17","sessionId":"1","sessionKey":"..."}` and `SESSION reply ... op=0x10 txn=0 bytes=56` (server configure response). This proves preload msg 12210 / `CSceneManager+0x171` gate cleared and `sendInit` ran; the former gated-status claim is superseded.
- `TestPreloadStubs` covers the implemented preload fixtures, including the corrected brand `result` array regression. Go tests passed and executable was rebuilt before replay; not rerun during this documentation handoff.
- First fixed-brand replay: setting/all 200 → brand/list 200 → buddy/list/type/0?page=0&size=500 200 → broad bootstrap HTTP burst → sendInit + configure reply → line/buddy/v4/list?page= 200 → crash. UIA console: `C:\Users\fes\AppData\Local\Temp\opencode\uia-windows.txt`, lines 773–820 (775 create/complete; 790–817 preload/sendInit; 820 disconnect); relaunches continue after 822.
- Android fatal logcat: device `11:42:25.074`, `11:43:22.931`, `11:43:59.051`: GLThread SIGSEGV / SEGV_MAPERR, fault addr `0x0`. Device and host log timezones differ. `crash_dump64` failed; no tombstone/backtrace PC was captured. **Cause unknown.** This is distinct from the fixed wrong-brand-shape crashes at device `11:25:38.891`, `11:25:53.075`, `11:27:04.516`.

## M3: make Garden/lobby bootstrap survive and reach a usable scene — NOT STARTED

Candidate boundary only: the first post-preload burst included 404s for chat room bg/sync, noti reddot, shop/status, push/regist, items/modified, playhome lp_rmchat, preliminary/winners, inven/counts, coin GEM/CASH, vip balance/lounge, avatar popup except, pet arrange, movie list, fashionista hint, composite list, banned keyword, and `/v4/setting/Android/0`. `/v4/avatar/gmAvatarList` and `/v4/avatar/additionalInfo` hit the broad avatar handler and returned 200, potentially with semantically wrong avatar-info schema. **No request/consumer is selected as the culprit.**

The next session must first correlate the repeatable GLThread crash with one response/consumer and preserve the first-failure request ordering. Avoid bulk-stubbing all 404s or speculative endpoint implementation. This handoff records evidence, not an implementation plan; no crash inspection or M3 implementation in this update.

## Guardrails and explicitly deferred work

- **User controls all restarts/replays; no scripted taps or automatic restarts.** Restarting Cherry wipes in-memory accounts. Future code changes require format, tests, and executable rebuild; passing tests alone is not deployment. This update is documentation only: no Go edits, IDA, crash investigation, process restarts, or commit.
- **Full-client asset policy:** users download the fullest client; the santi backup push is the lab instance. Do not mimic on-demand streaming or build an artsitem/CDN file server unless a later demonstrated blocker requires a file absent from the backup. Archive stays read-only. **Assume the archive is missing assets.** There is no way to verify completeness — players never obtained every gacha item, furniture, or other catalog asset. A file's absence from the santi backup or the APK is not proof it never existed. Do not start building a CDN for those gaps unless a later demonstrated block requires a specific missing file.
- **Keep Frida stopped.** Instrumentation deferred unless a specific client-state question requires it. Record AirArmor callback 10 if seen; do not press its popup button. Callback 12 alone is harmless.
- **Keep YunDetectService.exe running; defer the :10000 conflict.** Do not kill/disable it or change routing for this task. Do not reopen the `/etc/hosts` experiment; emulator DNS override is the known-working route.
- Deferred: LINE OAuth, billing, ad SDK stubs, OBS media, FCM, crash collectors, minigames, chat, social/economy, full item catalog + 2014 import, all-messages protobuf recovery, persistent storage, Windows client port, and Frida instrumentation.
