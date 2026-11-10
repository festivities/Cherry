# Cherry — Bringing your own asset dataset (DATASET.md)

For agents helping someone **host their own Cherry**. Cherry ships no game assets. The LINE PLAY client needs a lot of files (item art, UI, sounds, map packs, the string table…), and Cherry serves them from an S3-style bucket. The original lab used two device backups ("santi" and "lpbackup") plus a donor's pet folders ("miaw", 2026-11-07); see AGENTS "Policies" (R2 layout). Another host will have a different collection. This file explains how to turn that collection into the bucket layout Cherry expects. Rules that apply everywhere are in [AGENTS.md](AGENTS.md) "Policies".

**Paths (2026-11-10):** `%LOCALAPPDATA%\Cherry\…` below is today's Windows layout, the only one the code knows (`os.UserCacheDir()`). PLAN Roadmap 8 slice 8.3a makes the data directory a setting and separates state from the re-downloadable caches (`static\`, `dp-cache\`) for Linux hosts; until that lands, a Linux host gets `$XDG_CACHE_HOME/Cherry` or `~/.cache/Cherry`.

## 1. What Cherry needs at runtime (and what it never does)

- At runtime Cherry reads **only the bucket**, through a read-through cache in `%LOCALAPPDATA%\Cherry\static\` (`internal/assets`: `assets.Archive` opened by `LoadArchive`, `Store.Local`/`ReadFile`/`ServeKey`/`Prefetch`, `DeviceFiles`, `ReadItem`/`ItemDir`, the originals item index `Items()`). It never reads a local archive folder; since 2026-11-02 the server has no archive paths at all (only the upload tool and the test helpers in `internal/testutil` have them). Do not add one: if a file is needed, it goes into the bucket.
- **The originals item index** is one listing of `static/item/` saved as `%LOCALAPPDATA%\Cherry\static\.items.json`. Shops, gacha pools, grants and the art check (`thumbs.HasArt`, file existence) come from it. The first start without that file lists the bucket (~45 s on the lab host; the listeners open only afterwards, and an app launched earlier shows "Network Error"); later starts read the saved copy. There is no refresh: **delete `.items.json` after uploading new original items.** At startup Cherry also builds the shop catalog (pet categories from each pet's `behavior.xml`) and reads the string table before it accepts a request (`economy.WarmArchive`, `web.WarmArchive`), because a request that waits on per-item bucket reads times out on the client.
- Credentials: `%LOCALAPPDATA%\Cherry\cloudflare.json` with `r2_endpoint` (https), `r2_bucket`, `r2_access_key_id`, `r2_secret_access_key` (plus `account_id`, `api_token`, `d1_database_id` for the D1 mirror, upload tool only). It is private: never print, log or commit it. Without the file Cherry still starts (the art-dependent startup checks are skipped with one log line), but items, thumbnails and original art answer 404 or a placeholder.
- `config\streaming.json` {"enabled": true} also points the client's patcher at the bucket, so a fresh APK-only install downloads what it needs (~348 MB). Without it the device must already have the files in its own `files/`. Switching it on changes the checkresource2 versions, so devices fetch every list again and download what their saved lists lack.

## 2. Bucket layout (the contract)

Two top-level trees. They never mix (AGENTS "R2/D1 layout"):

| Key | Content | Source on a device / in an APK |
| --- | --- | --- |
| `static/animation/<rel>`, `static/sound/<rel>`, `static/ui/<rel>` | bulk categories (also the patcher lists) | device `files/animation/`, `files/sound/`, `files/UIResource/` |
| `static/subui/MarbleChallengePopup.plist` | the subui category (one plist) | `files/UIResource/` |
| `static/tx/arts_strings.ast` | string table (SQLite), MD5 checked against `web.artsStringsMD5` | `files/stringtable/arts_strings.ast` |
| `static/item/<kind>/<numeric id>/<file>` | items: kinds `custom dress interior tile pet garden` (`assets.ItemKinds`); `1409_iteminfo.artsitem`, part PNGs, optional `dp.png`, `<id>.aniproj` / `<id>_<name>.aniproj` | `files/item/<kind>/<id>/` |
| `static/garden/<stem>.mworldpack`, `static/world/<stem>.mworldpack` | map packs (`assets.PackDirs`, names checked by `assets.ValidPack`) | `files/garden/`, `files/world/` |
| `static/chatting/action_hd/<name>.png` | chat action icons (served without the version prefix) | `files/cache/action/00005_chatting_deco_action_<name>.png` |
| `static/garden01/…`, `static/craft01/…`, `static/photozone/frame/…` | Garden craft art, Avatar Pic frames (`assets.CraftDirs`) | `files/item/craftitem/<dir>/…`, else `files/cache/<dir>/…` |
| `static/DailyBonus/…` | calendar skin plist + PNGs (origin source `DailyBonus`) | `files/DailyBonus/` of a device that never used Cherry (Cherry writes `cherry_day*.png` there) |
| `static/DiarySkin/common/UIImage_hd/avatar_photo_bg.png`, `static/DiarySkin/00001/UIImage_hd/skin_info.json` | the default Diary skin (origin source `DiarySkin`, two single files since 2026-11-08; not a bulk category, or it would become a patcher category) | `files/DiarySkin/` |
| `static/android-10.1.0.0-2024/avatar_data/…` | avatar rigs and body sprites for Cherry's thumbnails (origin source) | APK `assets/avatar_data/` (10.1.0.0) |
| `static/android-4.0.0.0-2016/animation/fish/fish_ani_0001.png` | fish silhouette behind Cherry's fish pictures (origin source) | 4.0.0.0 APK `assets/animation/fish/` (an older APK; any copy of that one file works) |
| `cherry/manifest/<cat>.json`, `cherry/manifest/packs.json`, `cherry/manifest/items.json` | indexes Cherry reads (built by the upload tool) | generated |
| `cherry/item/<kind>/<id>/…`, `cherry/garden/…` | Cherry-made content (converted iOS items, generated packs) | generated, recipe recorded |

`DeviceFiles` maps a device path the same way: `UIResource/<rel>` → `static/ui/<rel>`, every other folder `<folder>/<rel>` → `static/<folder>/<rel>`. A device `files/` tree taken from a working client is close to the `static/` layout already; most of the work is choosing sources and keeping Cherry-made files out. "Origin sources" (`origSources` in `cmd/cherry-assets/origin.go`) are originals no device-backup root provides; each keeps its own folder name as the key prefix and gets D1 `manifest` rows with cat = that name. The lab's bucket: 113,668 objects / 1.09 GB on 2026-11-02, 113,809 after the miaw pet donation (2026-11-07, `verify` green), plus the two `DiarySkin` files (2026-11-08; total not re-counted).

## 3. Procedure for an agent

1. **Inventory, read-only.** Ask the user where their collection is and treat it as **read-only** (copy, never edit in place). Typical inputs: device backups of `/data/data/jp.naver.lineplay.android/files/` (rooted phone or emulator, `adb` + `su`), APK files (unzip `assets/`), older APKs/IPAs, zips from other players. For each source, record its version, date and origin, and hash the important files (`libgame.so`, `arts_strings.ast`). AGENTS "Baseline" says how to compare an APK by hash.
2. **Find what each source holds.** List `files/` top-level folders and count `item/<kind>/<id>/` folders per kind, map packs and bulk files. Several sources overlap: pick a **priority order** (most complete first). The tool takes bulk categories from the first root only, and items, map packs and craft art from later roots only where the earlier ones lack the key (`buildObjects`).
3. **Keep Cherry-made files out of `static/`.** A device that has ever talked to a Cherry server holds Cherry's files at the client's own paths: generated `dp.png` composites in item folders, `files/DailyBonus/cherry_day*.png` and a Cherry plist, `files/animation/-cherry/`, items in the Cherry id block 428499000–428499999 (`assets.IsCherryItem`). Prefer backups taken **before** the device ever used Cherry. Otherwise run an isolation audit like the lab's (AGENTS "Policies", isolation audit 2026-10-26): compare dp.png files against Cherry's renders and drop the matches. The tool refuses the known patterns (`checkOrigins`, `checkOrig`, `cherryMadeName`). That is a safety net, not proof.
4. **Point the tool at the sources.** The roots are **constants in `cmd/cherry-assets` on purpose** (no flag, so an unaudited backup cannot slip in): `defaultDataset` / `extraDataset` / `miawDataset` → `archiveRoots` (device `files/` trees, priority order), `origSources` + `originalAssetsRoot` in `origin.go` (APK folders and single files that no backup holds, plus single backup files outside the bulk categories, such as the two default `DiarySkin` files from santi), `itemZipPath` (2014 iOS `item.zip`, only for `convert`). Edit them for the user's paths in a commit of their fork, and explain in the commit message what each source is.
5. **Plan, then upload.** `go run ./cmd/cherry-assets plan` (no network: counts and bytes per group) → review with the user (size against the provider's free tier; Cloudflare R2 free tier: 10 GB-month storage, 1 M Class A and 10 M Class B operations a month, no egress fees) → `upload` (HEAD then PUT of missing keys, then the indexes in `cherry/manifest/` and the D1 mirror) → `verify` (lists the bucket and compares it with the plan). `upload -only <origin source>` (one `origSources` entry and its D1 rows) and `upload -packs-only` (map packs + `cherry/manifest/packs.json`) upload a part. A one-source run rewrites only that source's D1 rows. Uploads are outward-facing: get the user's go-ahead for each one.
6. **Check gaps at runtime.** Delete `%LOCALAPPDATA%\Cherry\static\.items.json` if items changed, start Cherry with the credentials (wait for `https listening`), then read its log. A file the client asks for and the bucket lacks shows as a 404 for that path: `scripts/summarize-log.py <log>` lists method, path and status. Match each 404 to §2 and find it in another source, or record it as missing. The archive missing a file does not prove the original game lacked it (AGENTS "Policies"). Also search image files by sight, not only by name (the lab missed the butler cards this way once).
7. **Optional: converted items.** `cherry-assets convert` turns 2014 iOS `item.zip` items that no backup has into the 10.1 format (Cherry-made, recipe `itemconv-v1`, `cherry/item/`, switch `data/itemconv.json`). Skip it when the user has no such zip.

## 4. Other S3-compatible stores

`internal/assets/r2.go` is a small stdlib SigV4 client (HEAD, GET, PUT, DELETE, ListObjectsV2) with the region fixed to `"auto"` (R2). Only R2 has been tested. For MinIO, Backblaze B2, Wasabi, AWS S3 or similar:
- Make the region configurable (an optional `r2_region` in the credentials file, default `"auto"`). Most providers reject a signature whose region scope is wrong.
- Check `objectPath` against the provider's addressing style (path-style `https://<endpoint>/<bucket>/<key>`, the form R2 accepts; MinIO accepts it too; AWS prefers virtual-hosted style).
- The D1 mirror (`cmd/cherry-assets/d1.go`) is Cloudflare-only and is not read at runtime. Make its sync skip cleanly when no D1 fields are set.
- Keep the dependency rule (AGENTS "Policies", Dependencies: stdlib only, except `modernc.org/sqlite` for the database; any other module needs the user's yes). The S3 client stays stdlib.

## 5. Don'ts

- Never commit game assets, credentials, account stores or logs (AGENTS "Policies"). The repository is public.
- Never put a Cherry-made file under `static/`, or an original under `cherry/`.
- Never let Cherry read the user's archive folder at runtime. Everything goes through the bucket.
- Never restore a live `accounts.json` wholesale from an older backup while devices are logged in (it drops their rotated login tokens; AGENTS "Policies"). This file is about assets, but bucket work often comes with restarts and store backups.
- Dataset work never modifies the client or an APK. Client patching is a separate job with its own rules (AGENTS "Policies", Client modifications: the Raspberry repo, since 2026-11-09), and no patched or original binary ever goes into the bucket or into Git.
