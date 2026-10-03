# LINE PLAY reconstructed API summary

## Scope and evidence

This standalone, language-neutral reference summarizes request paths and data protocols observed or reconstructed for the LINE PLAY Android client, version 10.1.0.0 (version code 254). It covers startup, guest onboarding, character creation, Garden entry and movement, and Closet outfit changes. Some details come from static client analysis. The Garden movement send was exercised through a modified-client replay, not captured from an unmodified client.

This is not a complete specification of LINE PLAY's production services. Synthetic replies accepted by the client show compatible response shapes for the tested flows; they do not establish that every field, value, or empty response matches the production backend.

Evidence labels used below:

- **Observed**: the request or exchange appeared in a controlled client run. This label does not imply that every run used an unmodified client.
- **Parser-derived**: client code inspection establishes fields or types consumed by the client, but the complete request/reply may not have been captured.
- **Accepted synthetic reply**: a test response advanced the client in a visible run. Treat it as a compatible example, not a production capture.
- **Modified-client replay**: the protocol was exercised with a modified client; this demonstrates a compatible exchange but is not evidence that an unmodified client emitted that request.
- **Unresolved**: the path was seen, but its schema or significance is not known.

The service origins seen in the client include fapi.play.naver.jp for JSON APIs, play-static.line-scdn.net for static resources, and notice API hosts lan3rd.line.me / notice2.line.me. These are observed origins; their availability and routing may differ in another environment.

Response envelopes are endpoint-specific. Many APIs place data under result, while buddy and common-data responses use a root has_npc_result field. Do not assume a single wrapper for every endpoint.

## HTTP and HTTPS JSON APIs

The JSON API uses versioned paths, mostly under /v4. Observed JSON requests and responses use UTF-8. Exact authentication headers, request signing, and general error-envelope rules have not been recovered; their absence should not be assumed for untested endpoints.

### Bootstrap and notices

| Method and path | Request facts | Response data and status |
|---|---|---|
| GET /v4/setting/goodbye/cherry | Observed before initialization. No request body is documented. | An accepted synthetic reply had result.billingEnd (boolean), result.forceRefundPopup (boolean), and result.refundUrl (string). Exact parser-required fields are not fully established. |
| GET /v4/setInitConf?nationCode=us&appVer=10.1.0.0 | Observed during startup. Query carries nation and app version. | **Parser-derived:** the client reads result.sessionServerInfo, staticDomain, nationCode, isGdprNation, snsLoginUIList, and snsSignUpUIList. sessionServerInfo selects/configures the native session transport. The exact production route descriptor was not recovered. staticDomain is the base for static-resource reads. |
| GET /notice/adr/checkresource2.json | Observed after setInitConf. | **Parser-derived:** root adr maps the client bundle version (observed 10.1.0.0) to a resource-set name; root resource maps that set to resource-type version strings. One accepted manifest used animation 531, sound 530, tx 540, subui 430, and ui 540. Other resource families in that test response were 0; this does not establish their general production semantics. |
| GET /v1/lineplay/android | Notice request observed with query values including notificationLocalRv, noticeNewTerm; another captured form included lang, country, and noticeTimestamp. Values vary. A sample user agent was LAN/1.3.5 (lineplay; 10.1.0; android; 14; sdk_gphone64_x86_64; googleplay; en; US; unknown). | The notice parser consumes nested app information and notification state. A revision-zero example contained result.app.result with version and market links, result.noticeNewCount.result.newCount, and result.notifications.result with lastRv, count, notifications, and timestamp. The list parser's required fields are lastRv, count, notifications, and timestamp. A revision/delta reply is not proof of the initial full-notice contract or cached-notice behavior. |
| GET /v4/maintenance/whitelist/lineplay | Observed during guest startup. | An accepted shape is result: [{gameId, type, data}], with gameId equal to lineplay, type equal to udid, and data containing the client installation UUID. The UUID is client-specific. |
| GET /img/read/arts_session/sckey.enc | Static-resource form of the keyring request; /arts_session/sckey.enc was also observed in an earlier flow. | Binary keyring data; format is described under “Native session and GWS protocol.” |

### Guest session and account bootstrap

| Method and path | Request facts | Response data and status |
|---|---|---|
| POST /v4/account/guest/generate | Observed. Request body schema is not documented. | The client reads result.provider and result.accessToken. An accepted synthetic example used provider LD and a synthetic token; LD was recognized as the guest provider in that flow. |
| GET /v4/createSession | Observed after guest generation and again after character creation. | **Parser-derived:** result contains sessionKey, mid, avatarUserId, aid, lineId, lineName, and termAge. In the tested flow aid was 0 before avatar creation and 1 afterward. Session and identity values are per-client. |
| GET /v4/profile/0 and GET /v4/profile/1 | Observed before and after avatar creation. | The profile parser consumes a JSON result object. A tested profile includes string identifiers and display fields such as avatarId, name, and country, numeric counts/statuses, boolean flags, and avatarSnsInfoList. hasGarden is present as a boolean, but whether it gates owner-Garden entry has not been established. Do not infer a complete required profile schema from this partial shape. |
| GET /v4/setting/term/all | Observed as a terms-state read. | Accepted shape: result object keyed by term identifier, for example "1": false. |
| GET /v4/social/terms/sns | Observed check operation. | Accepted shape: {"result": true}. |
| PUT /v4/social/terms/sns | Observed agreement submission; this is a PUT, distinct from the GET check. | Accepted shape: {"result": true}. Request-body fields were not recovered. |
| GET /v4/account/direct/status | Observed after terms. | Accepted shape: result.status numeric and result.emailAddress string; one synthetic example used 0 and an empty string. |
| GET /v4/buddy/list/type/0 and GET /v4/line/buddy/v4/list | Both observed during guest-to-Garden flow. | Parser-backed empty buddy response uses root has_npc_result, an object with buddyList, newbieRecommendList, nearbyRecommendList, and bookmarks arrays plus cursor/count/profile fields. The tested empty reply showed no friend-information dialog. |
| POST /v4/common/data/list | Observed during a guest-to-Garden client run. Request body schema is not documented. | Parser-backed empty response is root has_npc_result: []. |

### Character creation

| Method and path | Request facts | Response data and status |
|---|---|---|
| GET /v4/avatar/1151262555912366100, /v4/avatar/1151262602112441105, /v4/avatar/1151262571612391402 | Three built-in intro-avatar reads were observed. | Avatar data is returned under result; the avatar parser reads full gender names, skin as a string integer, and items[].cd item codes. |
| GET /v4/banned/keyword | Observed in the creation flow. | Parser expects result to be an array of strings. |
| GET /v4/create/all/items | Observed. | **Parser-derived:** root result contains gender groups M, F, and A. Category records generally contain an items array; DRESS is a direct array. Item identifiers are symbolic strings in cd, not numeric archive IDs. Known categories include face shape HE, eyebrows EB, eyes EY, nose NO, mouth MO, hair HA, top TO, one-piece ON, pants PA, shoes SH, and accessory categories. |
| GET /v4/create/face/setitems/roll | Observed. | result is an array of presets with avatarType (MALE, FEMALE, or ANIMAL) and setItem[]; preset item fields consumed include cd, name, imgPath, and elementCodes[]. |
| POST /v4/create/avatar | Observed request is UTF-8 JSON with fields avatarType, itemCodes, skinColor, and name. Tested Female request used short avatarType "F", symbolic strings in itemCodes, and string skinColor (example "000"). The serialized body ended with newline and NUL bytes in the captured client request. | Accepted synthetic reply returns an avatar under result; the tested avatar used string avatarId "1", gender "FEMALE", a name, skin string, and items[] rows with cd. The server-side data needed by later reads is also represented by /v4/avatar/1. |
| POST /v4/create/complete | Observed after avatar creation. Full request-body schema is not documented. | **Parser-derived:** result.status is boolean and result.rewardCoin is integer. Accepted values true and 0 advanced to the 0-Gems START screen. |
| GET /v4/avatar/1 | Observed after avatar creation and in Closet flows. | Returns an avatar object under result; relevant appearance data is items[] with cd and, where applicable, invenSeq. |
| GET /v4/setting/all | Seen in the startup exchange. | Accepted synthetic result contains notiFlag, changeCountry, countryName, soundConfig, notiConfig, privacyConfig, and roomSize with max/cur values. Parser requirements are not fully documented. |
| GET /v4/quest/status | Seen in the startup exchange. | Accepted synthetic result contains heart, heartBase, heartRewardCoin, questProgress, and toExpire. Parser requirements are not fully documented. |

#### Item identifiers

Native item lookup expects symbolic codes. The known format is nine characters: an attribute character (R, C, P, or G), a type character (M, F, U, or A), a two-letter category, and a five-character base-36 suffix. Examples include CUHA00004, CUHE0000L, CUTO0011X, and CUPA000LO. Numeric IDs are not interchangeable with the symbolic codes in the creation catalog. Decompiled conversion logic maps numeric IDs using attribute × 100,000,000 + type × 10,000,000 + category × 100,000 + the base-36 suffix value; the per-letter numeric enum values and complete category taxonomy are not established here.

### Garden-adjacent HTTP requests

| Method and path | Response data and status |
|---|---|
| GET /v4/pet/inven/represent?avatarId=1 | Parser-backed response is {"result":[]}; the pet representation parser reads result as an array. |
| GET /v4/popup/isExists | Parser-compatible accepted reply is {"result": false}. Its relationship to the intermittent in-Garden Register Guest popup is unresolved. |
| GET /v4/avatar/profile/popup/except | Parser-backed response is {"result":[]}; entries are strings. |
| GET /v4/avatar/additionalInfo | Parser-derived result object includes string vipGrade, numeric balance/count fields (pCash, fCash, pGold, fGold, fPnt, friendCount, num2), optional circleId, and pet with count and list. Empty values were accepted in the tested flow. |

### Closet and outfit persistence

| Method and path | Request and response contract |
|---|---|
| GET /v4/home/list/ext/10.1.0.0/Android | **Parser-derived:** result.homeIconList and result.eventIconList are arrays. Home icon rows include id, name, image, flag, link, nMarkTimestamp, nMark, delimiter, linkType, and showMeOnly. A Closet entry used linkType "goSomewhere(closet)". |
| POST /v4/inven/closet/items/all | **Parser-derived:** response contains result.basicFaceList and result.inventoryList. Basic-face rows use itemCode. Inventory rows use itemCode, invenSeq, count, price, newArrival, specialEffects, grade, and dyeType. A nonempty inventory sequence marks an item as owned/selectable in the native Closet. |
| GET /v4/storage/display | Accepted response: {"result": true}. |
| GET /v4/style/slot/list | Accepted response: {"result":[]}. |
| POST /v4/r/badge/reset/{badge} | A dynamic uppercase badge name was observed. Success was accepted with an empty result object. |
| PUT /v4/avatar/save/v2 | Observed body is a JSON array of rows containing itemCode and invenSeq. The client may omit unchanged slots, so the request can represent partial outfit changes. The response parser consumes result as an avatar object with items: [{cd, invenSeq}]. |

Closet selection, save, return-to-Garden rendering, and reopening with the saved outfit were visibly exercised. GET /v4/inven/recycle/cfg was observed returning 404 and did not block the tested Closet flow.

### Observed feature flow

This is a useful sequence of observed calls, not a requirement that every client version or account makes every request:

1. **Startup:** request `/v4/setting/goodbye/cherry`, `/v4/setInitConf`, the resource manifest, notice data, and maintenance whitelist data. Static resources include the session keyring when the native session channel is used.
2. **Guest and consent:** POST `/v4/account/guest/generate`, then GET `/v4/createSession`; read profile and terms state, submit SNS terms with PUT `/v4/social/terms/sns`, and query account/buddy status as requested by the client.
3. **Character creation:** fetch intro avatars, banned words, creation catalog, and preset rolls; POST `/v4/create/avatar`, then POST `/v4/create/complete`; refresh `/v4/createSession`, profile, and avatar data.
4. **Garden:** use the native GWS channel for the Garden agent: login index 0, room-enter index 1, relay-start index 3, then quest-list request index 15. The movement request and server push are described in the native protocol section. XTCP session init/config is a separate exchange.
5. **Closet:** fetch home-menu data and closet inventory; select outfit items and PUT `/v4/avatar/save/v2`; the client can then reload avatar data and render the saved appearance on return to Garden.

The complete request bodies for several steps are unknown; see the endpoint notes and known-gaps section rather than treating this sequence as a complete production trace.

### Additional observed startup paths with unresolved schemas

The following paths received empty-list or empty-object replies during a startup replay, but their native response parsers were not traced. The reply shapes below are compatibility observations only:

- Empty-list examples: GET /v4/chat/room/bg, /v4/brand/list, /v4/eventFlag/flagList, /v4/playhome/games/lp_rmchat, /v4/avatar/gmAvatarList, /v4/preliminary/winners, /v4/movie/list/en, /v4/contest/fashionista/hint/flag/list, /v4/item/composite/multi/list, /v4/home/integrated/info, and /v4/badge/infos/; POST /v4/shop/status, /v4/items/room/some, and /v4/items/dress/some. Path families also seen include /v4/chat/roomlist/sync/{n}/{n}, /v4/items/modified/list/{n}, and /v4/pet/room/arrange/list/{n}/LEVEL_{n}. The accepted placeholder was result: [].
- Empty-object examples: GET /v4/setting/Android/0, /v4/inven/counts, /v4/coin/balance, /v4/vip/balance, /v4/vip/lounge/host, /v4/dailybonus/today, /v4/admob/0, /v4/quest/labor/status, /v4/seasonpass/info/icon, /v4/circle/join/info, and /v4/postbox/exist. A notification red-dot path family under /v4/noti/event/reddot/count/ also received this placeholder. The accepted placeholder was result: {}.

These empty placeholders are not parser-backed API contracts. The same caution applies to any startup endpoint not described above.

The method and behavior for /v4/items/dress/some are unresolved: one observation records a 404, while another lists POST with an empty-list reply. Do not treat the empty-list response as an established contract.

## Native session and GWS protocol

The configured XTCP session channel and the GWS game channel are distinct transports. Garden login, room-enter, relay, and movement exchanges used GWS on TCP port 10000. XTCP is advertised separately through sessionServerInfo; a separate XTCP init/config exchange was also observed. Garden protobuf messages use the GWS service payload described below, not the XTCP session packet body.

### Keyring resource

The binary resource requested as arts_session/sckey.enc is parsed by the client as follows:

1. The first four bytes are skipped; a required magic value was not established.
2. The next eight bytes are a big-endian timestamp floor.
3. Exactly 100 indexed key records follow, with indices 0 through 99.
4. Each record contains a big-endian 32-bit ciphertext length, AES-128-CFB ciphertext for the key string, then a second big-endian 32-bit ciphertext length and ciphertext for the IV string.
5. Decryption uses key/IV material embedded in the client. A key ID outside the populated table cannot be used.

The native server-first handshake payload is ASCII HI, a big-endian 32-bit key ID, then a big-endian 64-bit timestamp. The timestamp must be at least the keyring's floor. The handshake is length-prefixed and precedes encrypted application frames.

### Shared frame and cipher facts

- TCP is a byte stream; application frames carry a four-byte big-endian ciphertext length prefix. The prefix is outside the ciphertext and its length excludes the prefix itself. Do not infer TCP packet boundaries from a protobuf message-index prefix.
- After handshake, the frame body is encrypted with AES-128-CFB using the selected keyring entry.
- The session-channel decrypted header begins with ASCII LP, then one-byte packet kind, one-byte command, four-byte big-endian body length, and eight-byte big-endian sequence. Packet kind observed/required by the parser is 0x10.
- Short two-byte control frames also occur and do not have the full LP header. Their complete semantics are not established.
- The separate Garden/GWS client stream uses LP followed by an eight-byte big-endian server/agent sequence and its service payload. Server-to-client GWS frames use ASCII G, a byte containing (agent slot << 3) | subtype, an eight-byte big-endian sequence, then the service payload.
- Native agent type maps to GWS slot type - 1. Garden is agent type 10 with sequence 0x0001000100710000; Circle is a different service, type 8 with sequence 0x00010001006f0000. Room Party is another distinct flow, type 4 with sequence 0x0001000100650000.
- The client emits a two-byte MS GWS maintenance frame; a matching MS reply is used to keep that connection alive.

#### Wire layout reference

The following byte layouts summarize the observed framing. Integer widths and byte order are explicit; `||` means concatenation. Ciphertext lengths exclude the four-byte length prefix.

```text
Server-first key handshake (plaintext):
  uint32_be(14) || "HI" || uint32_be(key_id) || uint64_be(timestamp)

Application frame after handshake:
  uint32_be(ciphertext_length) || encrypted_body
  encrypted_body uses AES-128-CFB with the selected keyring key and IV

Decrypted XTCP session packet:
  "LP" || uint8(packet_kind=0x10) || uint8(command) ||
  uint32_be(body_length) || uint64_be(sequence) || body

Decrypted GWS client packet:
  "LP" || uint64_be(server_or_agent_sequence) || service_payload

Decrypted GWS server packet:
  "G" || uint8((agent_slot << 3) | subtype) ||
  uint64_be(sequence) || service_payload

Garden service payload:
  two_byte_message_index || protobuf_body
```

Garden's two-byte message-index byte order is unresolved in the available evidence. Do not substitute the session packet's big-endian fields as proof of the Garden index order.

### XTCP session init/config

The session-channel exchange is separate from the Garden service messages:

| Direction / message | Header values | Body |
|---|---|---|
| Client session init | packet kind 0x10, command 0, sequence observed as 3 | UTF-8 JSON object with exactly the keys country, lpVer, os, sessionId, and sessionKey; all values are strings. |
| Server configure reply | packet kind 0x10, command 0x10, same sequence | UTF-8 JSON with pingSeconds and noopSeconds. One accepted test example used 10 and 30. |

Command 0 is session initialization and command 0x10 is the configure reply. Two-byte follow-up control frames were observed, but their full semantics are not established. This init/config path can be live while Garden traffic uses GWS; it is not the Garden login protobuf channel.

### Garden service messages

The two-byte Garden message index is followed by a protobuf body. A modified-client Garden movement replay is reported as request index 7 and actor push 12. Static client analysis indicates low-byte-first serialization at the Garden send boundary, while the replay reports index 7 without retaining the raw bytes. This summary contains no raw movement frame, so the exact on-wire index byte order remains unresolved. The service-specific sequence reported by the traces is:

| Direction / message | Index | Relevant protobuf fields / accepted data |
|---|---:|---|
| Client login request ch_login_req | 0 | **Parser-derived request fields:** field 1 session_key string; field 2 aid; field 3 client_version; field 4 device_type; field 5 language_code; field 6 os_type. The decompilation shows these values are checked/populated before send, but the complete serialized login request was not retained as a wire capture. |
| Server login result hc_login_res | 0 | Accepted reply has field 1 result code 0x30201. |
| Client room-enter request | 1 | The exchange identifies field 1 as owner aid and field 2 as client map revision. A complete request capture is not retained. |
| Server room-enter result | 1 | Accepted success code is 0x30301. The compatible response names a map resource in field 4 and owner aid in field 5; field 2 is an empty NPC enum list and field 3 carries the client map revision. The client then resolves the named map from assets available on the device. |
| Client room relay-start request | 3 | Sent after the map/room-enter stage. The complete request fields were not established here. |
| Server room relay-start result | 3 | Accepted reply has field 1 result code 0. |
| Client quest-list request | 15 | Observed after Garden entry. |
| Server quest-list result | 21 | Accepted reply has field 1 error code 0; the synthetic guest has no quest entries. |
| Client Garden movement request ch_player_move_req | 7 | **Modified-client replay:** repeated GAMEBASE::MoveInfo messages in field 1. Each MoveInfo has field 1 current position and field 2 target position; each nested point has fields 1 (x) and 2 (y), encoded as protobuf varints. This request was not captured from an unmodified client. |
| Server actor movement push hc_actor_move_push | 12 | An accepted push identifies the actor by an ObjectKey in field 1, carries MoveInfo in field 2, and has field 5 visible set true. A modified-client test observed aid 1 moving from tile [30,39] to [31,33]; the avatar and camera moved. |

The tested room-enter response named map 1908261114. The client resolves that name against map assets available on the device; the Garden map bytes are not carried in the GWS room-enter reply.

Circle and Garden are separate agents and their indices are not interchangeable. Garden is type 10 with sequence 0x0001000100710000; Circle is type 8 with sequence 0x00010001006f0000. The modified-client Garden replay reports request 7 and actor push 12 on the Garden agent.

## Static item art requests

Closet and creation request item display images from paths of the form /arts_item_custom_{numeric-id}/dp.png and /arts_item_dress_{numeric-id}/dp.png. These return image bytes, not JSON. The IDs in these asset paths are numeric archive identifiers; catalog and avatar JSON use symbolic item codes. Do not substitute one identifier form for the other without the native mapping.

## Known gaps and caution

- /img/read/notice/notice.json2 was observed, but its response schema remains unknown. A synthetic result: [] reply coincided with a Garden replay, but it is not parser-validated and should not be treated as an API contract.
- /arts_animation_ini_00531/update_en.ini was observed as a missing static resource; its 404 did not prevent a guest-to-Garden movement replay.
- Optional startup APIs have received empty-list or empty-object replies in test runs. Unless listed above as parser-derived or visibly exercised with a defined shape, those replies are compatibility observations, not known schemas.
- The exact bodies for guest generation, terms PUT, common-data-list, create-complete request, Garden room-enter request beyond the extracted fields, and room relay-start request have not all been recovered.
- Client-side Garden service analysis indicates a two-byte message-index prefix before the protobuf body. This is distinct from the outer TCP length framing; do not treat the index prefix as a TCP frame.
