package economy

import (
	"bytes"
	"encoding/json"
	"image/png"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// Phase E fixtures. Codes follow itemCodeFromID: dress/225514607 = CUAE00B9R (accessory, animated:
// 250 Gems, grade R); interior/121400639 = RUWA000HR (wall, 100); pet/326400016 = PUPE0000G (1,200).
const (
	phaseEClothing = "CUAE00B9R"
	phaseEWall     = "RUWA000HR"
	phaseEPet      = "PUPE0000G"
)

// The archive holds CUAE00B9R, CUON0062I (Halloween days 3 and 7), RUCH004RC (Winter day 7) and the
// wall/pet above; CUAE00737 (Winter day 3) is left out on purpose to test the missing-item fallback.
func phaseEFixture(t *testing.T) {
	t.Helper()
	shopFixture(t, map[string][]string{
		"dress/225514607":    {"dp.png", "x.aniproj"},
		"dress/225107866":    {"dp.png", "x.aniproj"},
		"interior/120406168": {"dp.png", "x.aniproj"},
		"interior/121400639": {"dp.png"},
		"pet/326400016":      {"skeleton.xml", "dp.png"},
	})
}

// phaseEAccount installs a non-lab account (ownership is exactly its inventory) with a ledger dir.
func phaseEAccount(t *testing.T, gems int64, inventory []string) (*store.Account, string) {
	t.Helper()
	acc := &store.Account{AccessToken: "ptok", SessionKey: "sk", Aid: "9901", Name: "Mail", Gender: "FEMALE",
		Gems: gems, ItemCodes: inventory, InventoryCodes: inventory,
		Rooms:     map[string]store.RoomLayout{room.DefaultLevel: {Placed: []store.RoomPlaced{}}},
		RoomItems: []store.RoomItem{{Seq: store.FirstRoomSeq, Cd: "RUTA0004S"}}, NextRoomSeq: store.FirstRoomSeq + 1}
	testutil.InstallAccounts(t, map[string]*store.Account{"ptok": acc})
	testutil.SetLab(t, "9901", false)
	return acc, gachaLedgerDir(t)
}

// phaseEClock pins NowFunc to t (JST wall time) for the test.
func phaseEClock(t *testing.T, when time.Time) {
	t.Helper()
	old := NowFunc
	NowFunc = func() time.Time { return when }
	t.Cleanup(func() { NowFunc = old })
}

func phaseECall(t *testing.T, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerMailRoutes(mux)
	registerLoginBonusRoutes(mux)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func phaseEJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return out
}

func phaseEAddItemMail(t *testing.T, acc *store.Account, code string) int64 {
	t.Helper()
	if !addMailLocked(acc, store.MailRow{Type: store.MailTypeItem, Item: code, Message: "gift", Sender: MailSender()}, ActiveRewards().Mail.DefaultDays, NowFunc()) {
		t.Fatal("mailbox full")
	}
	return acc.NextMailSeq
}

// TestMailItemRowShape pins the postbox item row: every key the client parses and its JSON type.
func TestMailItemRowShape(t *testing.T) {
	phaseEFixture(t)
	at := time.Date(2026, time.October, 25, 6, 30, 0, 0, jst)
	phaseEClock(t, at)
	acc, _ := phaseEAccount(t, 0, nil)
	seq := phaseEAddItemMail(t, acc, phaseEClothing)

	rec := phaseECall(t, http.MethodGet, "/v4/postbox/list", "", "ptok")
	if rec.Code != http.StatusOK {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	env := phaseEJSON(t, rec)
	res, ok := env["result"].(map[string]any)
	if !ok || res["nextCursor"] != "0" || res["totalCount"] != "1" {
		t.Fatalf("list wrapper %v", env)
	}
	items, ok := res["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items must be an array of one row: %v", res["items"])
	}
	row := items[0].(map[string]any)
	want := map[string]string{"postSeq": strconv.FormatInt(seq, 10), "postType": "item", "productData": phaseEClothing,
		"productDataDetail": "", "productImageUrl": "", "title": ItemName(phaseEClothing), "content": "gift",
		"itemType": "DRESS", "itemGradeType": "R", "itemSpecialEffects": "", "flagType": "", "sent": "", "expireDate": ""}
	for k, v := range want {
		s, ok := row[k].(string)
		if !ok {
			t.Errorf("%s must be a string, got %T", k, row[k])
			continue
		}
		if v != "" && s != v {
			t.Errorf("%s = %q, want %q", k, s, v)
		}
	}
	for _, k := range []string{"expireDate", "sent"} {
		if s, _ := row[k].(string); s == "" {
			t.Errorf("%s must be epoch ms text, got empty", k)
		}
	}
	for k, want := range map[string]any{"fromBlocked": false, "canReply": false} {
		if row[k] != want {
			t.Errorf("%s = %v (%T), want %v bool", k, row[k], row[k], want)
		}
	}
	for _, k := range []string{"dyeType", "recycleReward"} {
		if _, ok := row[k].(float64); !ok {
			t.Errorf("%s must be a JSON number, got %T", k, row[k])
		}
	}
	sender, ok := row["senderAvatar"].(map[string]any)
	if !ok {
		t.Fatalf("senderAvatar must be an object, got %T", row["senderAvatar"])
	}
	if sender["avatarId"] != "0" || sender["name"] != MailSender() {
		t.Errorf("senderAvatar = %v", sender)
	}
	if _, ok := sender["items"].([]any); !ok {
		t.Errorf("senderAvatar.items must be an array")
	}

	// With a request host the picture is the item's dp.png route (the host is sunk to Cherry).
	req := httptest.NewRequest(http.MethodGet, "/v4/postbox/list", nil)
	req.Host = "fapi.play.naver.jp"
	req.Header.Set("Cookie", `AV_AUTH="ptok"`)
	rec = httptest.NewRecorder()
	mux := http.NewServeMux()
	registerMailRoutes(mux)
	mux.ServeHTTP(rec, req)
	img := phaseEJSON(t, rec)["result"].(map[string]any)["items"].([]any)[0].(map[string]any)["productImageUrl"]
	if img != "https://fapi.play.naver.jp/arts_item_dress_225514607/dp.png" {
		t.Errorf("productImageUrl = %v", img)
	}
	if got := itemImageURL(phaseEClothing, ""); got != "" {
		t.Errorf("no host must give no picture, got %q", got)
	}
}

// TestMailItemGrantKinds: unowned clothing, furniture and a pet are granted to their shop lists, the
// row leaves the mailbox in the same save, and a second receive is refused (62107).
func TestMailItemGrantKinds(t *testing.T) {
	phaseEFixture(t)
	phaseEClock(t, time.Date(2026, time.October, 25, 6, 30, 0, 0, jst))
	acc, dir := phaseEAccount(t, 0, nil)
	seqs := []int64{
		phaseEAddItemMail(t, acc, phaseEClothing),
		phaseEAddItemMail(t, acc, phaseEWall),
		phaseEAddItemMail(t, acc, phaseEPet),
	}
	for _, seq := range seqs {
		rec := phaseECall(t, http.MethodPost, "/v4/postbox/receive/"+strconv.FormatInt(seq, 10), "", "ptok")
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"result":true}` {
			t.Fatalf("receive %d: %d %s", seq, rec.Code, rec.Body.String())
		}
	}
	if !containsStr(store.AccountInventoryCodes(acc), phaseEClothing) {
		t.Errorf("clothing not in closet: %v", store.AccountInventoryCodes(acc))
	}
	if !hasRoomCode(acc, phaseEWall) {
		t.Errorf("furniture not in room items: %v", acc.RoomItems)
	}
	if len(acc.Pets) != 1 || acc.Pets[0].Cd != phaseEPet {
		t.Errorf("pet not granted: %+v", acc.Pets)
	}
	if len(acc.Mail) != 0 {
		t.Errorf("rows left in mailbox: %+v", acc.Mail)
	}
	if lines := gachaLedger(t, dir); len(lines) != 0 {
		t.Errorf("grants moved currency: %+v", lines)
	}
	rec := phaseECall(t, http.MethodPost, "/v4/postbox/receive/"+strconv.FormatInt(seqs[0], 10), "", "ptok")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "62107") {
		t.Errorf("second receive = %d %s, want 400 62107", rec.Code, rec.Body.String())
	}
	if n := len(store.AccountInventoryCodes(acc)); n != 1 {
		t.Errorf("repeat receive changed the closet: %v", store.AccountInventoryCodes(acc))
	}
}

// TestMailItemOwnedPaysSellBack: an owned item pays its table price in Gems, with a ledger line,
// and the closet gets no duplicate.
func TestMailItemOwnedPaysSellBack(t *testing.T) {
	phaseEFixture(t)
	phaseEClock(t, time.Date(2026, time.October, 25, 6, 30, 0, 0, jst))
	acc, dir := phaseEAccount(t, 10, []string{phaseEClothing})
	seq := phaseEAddItemMail(t, acc, phaseEClothing)
	rec := phaseECall(t, http.MethodPost, "/v4/postbox/receive/"+strconv.FormatInt(seq, 10), "", "ptok")
	if rec.Code != http.StatusOK {
		t.Fatalf("receive %d %s", rec.Code, rec.Body.String())
	}
	price := sellBackGems(phaseEClothing)
	if price != 250 || acc.Gems != 10+price {
		t.Fatalf("gems = %d, want %d (sell-back %d)", acc.Gems, 10+price, price)
	}
	if n := len(store.AccountInventoryCodes(acc)); n != 1 {
		t.Errorf("closet duplicated: %v", store.AccountInventoryCodes(acc))
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 1 || lines[0].Delta != price || !strings.Contains(lines[0].Reason, "sell-back "+phaseEClothing) {
		t.Errorf("ledger = %+v", lines)
	}
}

// TestMailItemReceiveRollsBack: a failed save leaves the row, the balance and the closet as they were.
func TestMailItemReceiveRollsBack(t *testing.T) {
	phaseEFixture(t)
	phaseEClock(t, time.Date(2026, time.October, 25, 6, 30, 0, 0, jst))
	acc, _ := phaseEAccount(t, 5, nil)
	seq := phaseEAddItemMail(t, acc, phaseEClothing)
	store.AccountsMu.Lock()
	blocker := filepath.Join(t.TempDir(), "blocker") // a file where the store directory should be: MkdirAll fails
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountStorePath = filepath.Join(blocker, "accounts.json")
	store.AccountsMu.Unlock()
	rec := phaseECall(t, http.MethodPost, "/v4/postbox/receive/"+strconv.FormatInt(seq, 10), "", "ptok")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("receive = %d %s, want 500", rec.Code, rec.Body.String())
	}
	if len(acc.Mail) != 1 || acc.Gems != 5 || len(store.AccountInventoryCodes(acc)) != 0 {
		t.Errorf("state not restored: mail=%d gems=%d closet=%v", len(acc.Mail), acc.Gems, store.AccountInventoryCodes(acc))
	}
}

// TestMailMissingItemKeepsRow: a row whose item the archive lacks is refused at receive time, logged,
// and kept in the mailbox with the balance and closet untouched.
func TestMailMissingItemKeepsRow(t *testing.T) {
	phaseEFixture(t)
	phaseEClock(t, time.Date(2026, time.October, 25, 6, 30, 0, 0, jst))
	acc, _ := phaseEAccount(t, 5, nil)
	seq := phaseEAddItemMail(t, acc, "CUAE00737") // a row that predates the archive change
	logs := captureLog(t)
	rec := phaseECall(t, http.MethodPost, "/v4/postbox/receive/"+strconv.FormatInt(seq, 10), "", "ptok")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "62110") {
		t.Fatalf("receive = %d %s, want 400 62110", rec.Code, rec.Body.String())
	}
	if len(acc.Mail) != 1 || acc.Gems != 5 || len(store.AccountInventoryCodes(acc)) != 0 {
		t.Errorf("row or balance changed: mail=%d gems=%d closet=%v", len(acc.Mail), acc.Gems, store.AccountInventoryCodes(acc))
	}
	if !strings.Contains(logs.String(), "cherry: mail item missing code=CUAE00737") {
		t.Errorf("no missing-item log line: %q", logs.String())
	}
}

// TestMailAdminItem: the staff tool mails a sellable item code and refuses unknown codes.
func TestMailAdminItem(t *testing.T) {
	phaseEFixture(t)
	phaseEClock(t, time.Date(2026, time.October, 25, 6, 30, 0, 0, jst))
	acc, _ := phaseEAccount(t, 0, nil)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/mail", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		handleAdminMail(rec, req)
		return rec
	}
	if rec := post(`{"aid":"9901","type":"item","item":"CUXX00000","message":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown code = %d", rec.Code)
	}
	if rec := post(`{"aid":"9901","type":"item","item":"CUAE00737","message":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("sellable code missing from the archive = %d, want 400", rec.Code) // refused at send time
	}
	if rec := post(`{"aid":"9901","type":"item","item":"` + phaseEWall + `","message":"Welcome back"}`); rec.Code != http.StatusOK {
		t.Fatalf("item mail = %d %s", rec.Code, rec.Body.String())
	}
	if len(acc.Mail) != 1 || acc.Mail[0].Type != store.MailTypeItem || acc.Mail[0].Item != phaseEWall || acc.Mail[0].Amount != 0 {
		t.Fatalf("mail row = %+v", acc.Mail)
	}
	if rec := post(`{"aid":"9901","type":"gems","amount":0}`); rec.Code != http.StatusBadRequest {
		t.Errorf("zero gems = %d, want 400", rec.Code)
	}
}

// TestSeasonByGameDay: seasons follow the 06:00 JST game day, and New Year wins the Jan 1-5 overlap.
func TestSeasonByGameDay(t *testing.T) {
	cases := []struct {
		when time.Time
		want string
	}{
		{time.Date(2026, time.October, 24, 23, 59, 0, 0, jst), ""},
		{time.Date(2026, time.October, 25, 5, 59, 0, 0, jst), ""},
		{time.Date(2026, time.October, 25, 6, 0, 0, 0, jst), "halloween"},
		{time.Date(2026, time.November, 7, 23, 0, 0, 0, jst), "halloween"},
		{time.Date(2026, time.November, 8, 5, 59, 0, 0, jst), "halloween"},
		{time.Date(2026, time.November, 8, 6, 0, 0, 0, jst), ""},
		{time.Date(2026, time.December, 14, 12, 0, 0, 0, jst), ""},
		{time.Date(2026, time.December, 15, 6, 0, 0, 0, jst), "winter"},
		{time.Date(2026, time.December, 31, 23, 0, 0, 0, jst), "winter"},
		{time.Date(2027, time.January, 1, 6, 0, 0, 0, jst), "newyear"},
		{time.Date(2027, time.January, 1, 5, 59, 0, 0, jst), "winter"},
		{time.Date(2027, time.January, 5, 12, 0, 0, 0, jst), "newyear"},
		{time.Date(2027, time.January, 14, 12, 0, 0, 0, jst), "newyear"},
		{time.Date(2027, time.January, 15, 6, 0, 0, 0, jst), ""},
	}
	for _, c := range cases {
		if got := scheduleAt(c.when).Name; got != c.want {
			t.Errorf("%s: calendar %q, want %q", c.when.Format(time.RFC3339), got, c.want)
		}
	}
	if s, e := seasonNamed("winter"); !e {
		t.Fatal("winter missing")
	} else if a, b := s.window(time.Date(2026, time.December, 20, 12, 0, 0, 0, jst)); a != "20261215000000" || b != "20270105235959" {
		t.Errorf("winter window 2026-12-20 = %s..%s", a, b)
	}
	if a, b := scheduleAt(time.Date(2027, time.January, 3, 12, 0, 0, 0, jst)).window(time.Date(2027, time.January, 3, 12, 0, 0, 0, jst)); a != "20270101000000" || b != "20270114235959" {
		t.Errorf("newyear window = %s..%s", a, b)
	}
}

// TestSeasonScheduleIDsDistinct: every calendar's skin version differs from the others.
func TestSeasonScheduleIDsDistinct(t *testing.T) {
	seen := map[int]string{normalSchedule().ID: "normal"}
	for _, s := range seasons() {
		id := s.withID().ID
		if id < 100 || id > 999 {
			t.Errorf("%s id %d out of range", s.Name, id)
		}
		if other, dup := seen[id]; dup {
			t.Errorf("%s shares scheduleId %d with %s", s.Name, id, other)
		}
		seen[id] = s.Name
		if got := scheduleByID(id).Name; got != s.Name {
			t.Errorf("scheduleByID(%d) = %q, want %q", id, got, s.Name)
		}
	}
	if got := scheduleByID(1).Name; got != "" {
		t.Errorf("unknown id must fall back to the normal calendar, got %q", got)
	}
}

// TestSeasonCalendarAttend walks the Halloween calendar: info rows, the item day (its Gems plus the
// item), the owned row turning into Gems, a repeat attend crediting nothing, and the season change
// restarting the count.
func TestSeasonCalendarAttend(t *testing.T) {
	phaseEFixture(t)
	acc, dir := phaseEAccount(t, 0, nil)
	day := func(d, h int) time.Time { return time.Date(2026, time.October, d, h, 30, 0, 0, jst) }

	phaseEClock(t, day(25, 6))
	rec := phaseECall(t, http.MethodGet, "/v4/dailybonus/info", "", "ptok")
	res := phaseEJSON(t, rec)["result"].(map[string]any)
	halloween := scheduleAt(day(25, 6)).withID()
	if res["title"] != "Halloween Login" || res["scheduleId"] != float64(halloween.ID) || res["startYmdt"] != "20261025000000" || res["endYmdt"] != "20261107235959" {
		t.Fatalf("info header = %v", res)
	}
	bonus := res["bonus"].([]any)
	if len(bonus) != 7 {
		t.Fatalf("bonus rows = %d", len(bonus))
	}
	item := bonus[2].(map[string]any)
	if item["bonusType"] != "ITEM" || item["itemCd"] != phaseEClothing || item["count"] != float64(3) || item["gem"] != float64(150) {
		t.Errorf("item day row = %v", item)
	}
	if gem := bonus[0].(map[string]any); gem["bonusType"] != "GEM" || gem["gem"] != float64(90) || gem["itemCd"] != "" {
		t.Errorf("gem day row = %v", gem)
	}

	attend := func(when time.Time) map[string]any {
		phaseEClock(t, when)
		r := phaseECall(t, http.MethodGet, "/v4/dailybonus/attend", "", "ptok")
		if r.Code != http.StatusOK {
			t.Fatalf("attend %s = %d %s", when.Format(time.RFC3339), r.Code, r.Body.String())
		}
		return phaseEJSON(t, r)
	}
	attend(day(25, 6)) // day 1: 90 Gems
	attend(day(26, 6)) // day 2: 120 Gems
	if acc.Gems != 210 || acc.AttendDay != 2 {
		t.Fatalf("after day 2: gems=%d day=%d", acc.Gems, acc.AttendDay)
	}
	attend(day(27, 6)) // day 3: 150 Gems and the item
	if acc.Gems != 360 || !containsStr(store.AccountInventoryCodes(acc), phaseEClothing) {
		t.Fatalf("item day: gems=%d closet=%v", acc.Gems, store.AccountInventoryCodes(acc))
	}
	if lines := gachaLedger(t, dir); len(lines) != 3 {
		t.Errorf("ledger lines after the item day = %d, want one per Gem credit (3)", len(lines))
	}

	// The item is owned now: the calendar shows the day's Gems plus its sell-back, and the attend pays them.
	phaseEClock(t, day(28, 6))
	rec = phaseECall(t, http.MethodGet, "/v4/dailybonus/info", "", "ptok")
	row := phaseEJSON(t, rec)["result"].(map[string]any)["bonus"].([]any)[2].(map[string]any)
	if row["bonusType"] != "GEM" || row["gem"] != float64(150+250) || row["itemCd"] != "" {
		t.Errorf("owned item row = %v", row)
	}
	attend(day(28, 6)) // day 4: 180 Gems
	if acc.Gems != 360+180 {
		t.Fatalf("gems after day 4 = %d", acc.Gems)
	}
	attend(day(28, 7)) // repeat the same game day: nothing
	if acc.Gems != 540 {
		t.Errorf("repeat attend credited: gems=%d", acc.Gems)
	}

	// A new calendar starts its own count: the normal cycle restarts at day 1 on 2026-11-08.
	attend(time.Date(2026, time.November, 8, 6, 30, 0, 0, jst))
	if acc.Gems != 540+90 || acc.AttendDay != 1 {
		t.Errorf("normal restart: gems=%d day=%d", acc.Gems, acc.AttendDay)
	}
}

// TestSeasonOwnedItemPaysAtAttend: an account that already owns the day-3 item is credited the day's
// Gems plus the item's sell-back in one ledger line that names the item.
func TestSeasonOwnedItemPaysAtAttend(t *testing.T) {
	phaseEFixture(t)
	acc, dir := phaseEAccount(t, 0, []string{phaseEClothing})
	acc.AttendDay, acc.AttendKey = 2, AttendDayKey(time.Date(2026, time.October, 26, 6, 30, 0, 0, jst))
	phaseEClock(t, time.Date(2026, time.October, 27, 6, 30, 0, 0, jst))
	if rec := phaseECall(t, http.MethodGet, "/v4/dailybonus/attend", "", "ptok"); rec.Code != http.StatusOK {
		t.Fatalf("attend = %d %s", rec.Code, rec.Body.String())
	}
	if acc.Gems != 150+250 || acc.AttendDay != 3 {
		t.Fatalf("gems=%d day=%d", acc.Gems, acc.AttendDay)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 1 || lines[0].Delta != 150+250 || !strings.Contains(lines[0].Reason, "item "+phaseEClothing) {
		t.Errorf("ledger = %+v", lines)
	}
	if n := len(store.AccountInventoryCodes(acc)); n != 1 {
		t.Errorf("closet duplicated: %v", store.AccountInventoryCodes(acc))
	}
}

// TestSeasonRoomItemDay: Winter day 7 pays 450 Gems, 15 Cash and 3 tickets plus a room item
// (RUCH004RC, furniture). The grant lands in the room items, the calendar then shows the day's Gems
// plus the furniture's sell-back, and the skin serves the day.
func TestSeasonRoomItemDay(t *testing.T) {
	phaseEFixture(t)
	acc, dir := phaseEAccount(t, 0, nil)
	const room7 = "RUCH004RC"
	acc.AttendDay, acc.AttendKey = 6, AttendDayKey(time.Date(2026, time.December, 19, 6, 30, 0, 0, jst))
	phaseEClock(t, time.Date(2026, time.December, 20, 6, 30, 0, 0, jst))
	rec := phaseECall(t, http.MethodGet, "/v4/dailybonus/info", "", "ptok")
	row := phaseEJSON(t, rec)["result"].(map[string]any)["bonus"].([]any)[6].(map[string]any)
	if row["bonusType"] != "ITEM" || row["itemCd"] != room7 || row["gem"] != float64(450) || row["cash"] != float64(15) {
		t.Fatalf("day 7 row before = %v", row)
	}
	if r := phaseECall(t, http.MethodGet, "/v4/dailybonus/attend", "", "ptok"); r.Code != http.StatusOK {
		t.Fatalf("attend = %d %s", r.Code, r.Body.String())
	}
	if acc.Gems != 450 || acc.Cash != 15 || acc.FaceTickets != 3 || !hasRoomCode(acc, room7) {
		t.Fatalf("day 7: gems=%d cash=%d tickets=%d room=%v", acc.Gems, acc.Cash, acc.FaceTickets, acc.RoomItems)
	}
	if lines := gachaLedger(t, dir); len(lines) != 3 {
		t.Errorf("ledger lines = %d, want one per currency (3)", len(lines))
	}
	rec = phaseECall(t, http.MethodGet, "/v4/dailybonus/info", "", "ptok")
	row = phaseEJSON(t, rec)["result"].(map[string]any)["bonus"].([]any)[6].(map[string]any)
	if row["bonusType"] != "GEM" || row["gem"] != float64(450+375) || row["itemCd"] != "" {
		t.Errorf("owned room item row = %v", row)
	}
	id := strconv.Itoa(scheduleAt(time.Date(2026, time.December, 20, 6, 30, 0, 0, jst)).withID().ID)
	if skin := skinGet("/arts_attendance_en_00134_" + id + "/UIImage_hd/cherry_day07.png"); skin.Code != http.StatusOK {
		t.Errorf("winter day 7 tile = %d", skin.Code)
	}
}

// TestSeasonMissingItemFallsBack: a season item that the archive lacks pays the day's normal Gems,
// grants nothing, shows the normal row, and logs the code; it never fails the attend.
func TestSeasonMissingItemFallsBack(t *testing.T) {
	phaseEFixture(t) // CUAE00737 (Winter day 3) is not in the fixture archive
	acc, dir := phaseEAccount(t, 0, nil)
	acc.AttendDay, acc.AttendKey = 2, AttendDayKey(time.Date(2026, time.December, 16, 6, 30, 0, 0, jst))
	logs := captureLog(t)
	phaseEClock(t, time.Date(2026, time.December, 17, 6, 30, 0, 0, jst))
	rec := phaseECall(t, http.MethodGet, "/v4/dailybonus/info", "", "ptok")
	row := phaseEJSON(t, rec)["result"].(map[string]any)["bonus"].([]any)[2].(map[string]any)
	if row["bonusType"] != "GEM" || row["gem"] != float64(150) || row["itemCd"] != "" {
		t.Errorf("missing item row = %v", row)
	}
	if rec := phaseECall(t, http.MethodGet, "/v4/dailybonus/attend", "", "ptok"); rec.Code != http.StatusOK {
		t.Fatalf("attend = %d %s", rec.Code, rec.Body.String())
	}
	if acc.Gems != 150 || acc.AttendDay != 3 || len(store.AccountInventoryCodes(acc)) != 0 {
		t.Errorf("fallback: gems=%d day=%d closet=%v", acc.Gems, acc.AttendDay, store.AccountInventoryCodes(acc))
	}
	if lines := gachaLedger(t, dir); len(lines) != 1 || lines[0].Delta != 150 {
		t.Errorf("ledger = %+v", lines)
	}
	if !strings.Contains(logs.String(), "cherry: season item missing code=CUAE00737") {
		t.Errorf("no missing-item log line: %q", logs.String())
	}
}

// TestSeasonNewYearHasNoItemDays: New Year pays the normal calendar until its codes are chosen.
func TestSeasonNewYearHasNoItemDays(t *testing.T) {
	sched := scheduleAt(time.Date(2027, time.January, 3, 12, 0, 0, 0, jst))
	if sched.Name != "newyear" {
		t.Fatalf("calendar %q, want newyear", sched.Name)
	}
	for i, d := range sched.Cycle {
		if d.Item != "" || d != normalSchedule().Cycle[i] {
			t.Errorf("day %d = %+v, want the normal reward", i+1, d)
		}
	}
}

// captureLog routes the standard logger into a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// TestSeasonSkinServed: a season's skin is served under its own scheduleId and draws its item day;
// an unknown scheduleId gets the normal skin.
func TestSeasonSkinServed(t *testing.T) {
	phaseEFixture(t)
	id := strconv.Itoa(scheduleAt(time.Date(2026, time.October, 25, 6, 0, 0, 0, jst)).withID().ID)
	base := "/arts_attendance_en_00134_" + id + "/"
	ini := skinGet(base + "ini/DailybonusMainPopupNew.ini")
	if ini.Code != 200 || !strings.Contains(ini.Body.String(), "[cherry_day03.png]") {
		t.Fatalf("season ini %d %q", ini.Code, ini.Body.String())
	}
	plist := skinGet(base + "DailybonusMainPopupNew.plist")
	if plist.Code != 200 || !strings.Contains(plist.Body.String(), "UIResource/UIImage/cherry_day03.png") {
		t.Fatalf("season plist %d", plist.Code)
	}
	tile := skinGet(base + "UIImage_hd/cherry_day03.png")
	if tile.Code != 200 {
		t.Fatalf("season tile %d", tile.Code)
	}
	if _, err := png.Decode(bytes.NewReader(tile.Body.Bytes())); err != nil {
		t.Fatalf("season tile is not a PNG: %v", err)
	}
	normal := skinGet("/arts_attendance_en_00134_1/UIImage_hd/cherry_day03.png")
	if normal.Code != 200 || bytes.Equal(normal.Body.Bytes(), tile.Body.Bytes()) {
		t.Errorf("season tile must differ from the normal calendar's day 3")
	}
}

// TestAdminSeasonOverride: the staff tool forces a calendar regardless of the date, and "auto" undoes it.
func TestAdminSeasonOverride(t *testing.T) {
	defer seasonOverride.Store(nil)
	when := time.Date(2026, time.October, 1, 12, 0, 0, 0, jst)
	phaseEClock(t, when)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/season", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		handleAdminSeason(rec, req)
		return rec
	}
	if rec := post(`{"name":"halloween"}`); rec.Code != http.StatusOK || scheduleAt(when).Name != "halloween" {
		t.Fatalf("force halloween = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"name":"none"}`); rec.Code != http.StatusOK || scheduleAt(time.Date(2026, time.October, 26, 12, 0, 0, 0, jst)).Name != "" {
		t.Fatalf("force none = %d", rec.Code)
	}
	if rec := post(`{"name":"easter"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown season = %d, want 400", rec.Code)
	}
	if rec := post(`{"name":"auto"}`); rec.Code != http.StatusOK || scheduleAt(when).Name != "" {
		t.Errorf("auto = %d, calendar %q", rec.Code, scheduleAt(when).Name)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasRoomCode(acc *store.Account, code string) bool {
	for _, it := range acc.RoomItems {
		if it.Cd == code {
			return true
		}
	}
	return false
}
