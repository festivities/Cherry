package economy

import (
	"fmt"
	"net/http"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Economy Phase B: 7-day daily login calendar (dailybonus/*).
//
// Native flow (libgame.so arm64): at Garden entry the home event list always holds event
// 26; NaGardenEventManager::CheckReqEventPopupWebInfo @0x1f450d4 sends GET dailybonus/today
// (ResTodayData @0x1a60e80) and, when `attendable` is true, opens the calendar through
// NaAttendanceManager::ShowAttendance @0x191bdd4 (GET dailybonus/info, ResData @0x1c2a5ac).
// NaQuestManager::ReqQuestInfo also calls today. The calendar's button (id 242, only
// enabled while info.attendable) sends GET dailybonus/attend (ResAttendToday @0x1b2a0ec,
// NaAttendanceMainLayer::MsgProc @0x1957570); the reward popup is drawn from the cached
// info row bonus[todayCount-1], not from the attend reply, which only refreshes the
// Gem/Cash HUD and the "next sticker time" text (nextAttdFrom = time LEFT, hour or min).

// calDay is one reward row of a calendar (the normal cycle or a season, season.go).
// Type is the native bonusType string: GEM = Gems popup, CASH = Cash popup, VOUCH = Face Shop
// ticket popup (NaAttendanceMainLayer::GetRewardType @0x19196e4). An item day has Type
// itemRewardType and Item set. A row's popup shows only its Type reward; the other columns are
// still credited (the HUD refreshes from the attend reply).
type calDay struct {
	Type    string
	Gems    int64
	Cash    int64
	Tickets int64
	Item    string
}

// itemRewardType is the bonusType of an item day. Static (libgame.so arm64, 2026-10-22): the
// static init of GetRewardType's table (sub_14CE4C8) puts "ITEM" at index 1, which the reward
// popup (SetItemDpImage) draws from the row's itemCd.
const itemRewardType = "ITEM"

// The reward amounts of the normal cycle are in calendars.json (ActiveCalendars).

const (
	attendBoundaryShift = 3 * 3600 // day boundary 06:00 JST = 21:00 UTC
	attendDailyBonusID  = "134"    // atoi() -> "arts_attendance_<lang>_00134_1/" skin version key
)

// NowFunc is the clock; tests replace it.
var NowFunc = time.Now

// AttendDayKey numbers days that start at 06:00 JST.
func AttendDayKey(t time.Time) int64 { return (t.Unix() + attendBoundaryShift) / 86400 }

// AttendState derives the calendar view for an account at time t. A season has its own cycle:
// when the last attend fell in another calendar, the count starts again at day 1.
func AttendState(acc *store.Account, t time.Time) (todayCount int, attendable bool, hour, min int) {
	key := AttendDayKey(t)
	base := acc.AttendDay
	if acc.AttendKey != 0 && scheduleAt(keyTime(acc.AttendKey)).Name != scheduleAt(t).Name {
		base = 0
	}
	attended := acc.AttendKey == key && acc.AttendDay > 0
	todayCount = base%7 + 1
	if attended {
		todayCount = acc.AttendDay
	}
	rem := (key+1)*86400 - attendBoundaryShift - t.Unix()
	return todayCount, !attended, int(rem / 3600), int(rem % 3600 / 60)
}

// attendRows builds bonus[] for the calendar in force. An item day the account does not own
// shows the item (bonusType ITEM, itemCd) with its Gems; an owned one shows the day's Gems plus the
// item's sell-back Gems; a missing item shows the day's normal reward.
func attendRows(acc *store.Account, sched calSchedule, todayCount int, attendable bool) []map[string]any {
	rows := make([]map[string]any, 0, len(sched.Cycle))
	for i, r := range sched.Cycle {
		day := i + 1
		switch {
		case r.Item == "":
		case !seasonItemAvailable(r.Item):
			r.Type, r.Item = "GEM", ""
		case itemOwnedLocked(acc, r.Item):
			r.Type, r.Gems, r.Item = "GEM", r.Gems+sellBackGems(r.Item), ""
		}
		row := map[string]any{
			"count": day, "attendYN": day < todayCount || (day == todayCount && !attendable),
			"bonusType": r.Type, "bonusCategory": "", "gem": r.Gems, "cash": r.Cash, "heart": 0,
			"powder": 0, "vipPoint": 0, "itemCd": r.Item, "attdYmdt": "", "voucherCode": "",
			"gachaCategoryCode": "", "ecoinCode": "", "ecoinImgPath": "", "ecoinName": "",
			"ecoinAmt": 0, "smile": 0, "energy": 0, "luckyPowd": 0, "luckyPowdCnt": 0,
			"craftItem": 0, "craftItemCnt": 0, "luckyPowdImgUrl": "", "craftItemImgUrl": "",
			"craftItemName": "", "craftItemGrade": 0, "craftItemCategory": 0,
			"itemSpecialEffects": "", "itemGrade": "", "dyeItemImgUrl": "", "dyeItemName": "",
			"dyeItemCnt": 0, "dyeItemColorCnt": 0, "dyeType": 0, "dyeItemBleach": false,
			"voucherUsePlace": "",
		}
		if r.Tickets > 0 {
			row["voucherCode"], row["voucherUsePlace"] = "FACESHOP_TICKET", "FACESHOP"
		}
		rows = append(rows, row)
	}
	return rows
}

func registerLoginBonusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/dailybonus/today", handleAttendToday)
	mux.HandleFunc("/v4/dailybonus/info", handleAttendInfo)
	mux.HandleFunc("/v4/dailybonus/attend", handleAttend)
}

// ResTodayData reads nextAttdFrom from the ROOT object (asInt), unlike info/attend where
// it sits inside result (asString + atoi); both are sent here.
func handleAttendToday(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	var count int
	var able bool
	var h, m int
	if acc != nil {
		count, able, h, m = AttendState(acc, NowFunc())
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	next := map[string]any{"hour": h, "min": m}
	httpx.WriteObj(w, map[string]any{"nextAttdFrom": next, "result": map[string]any{
		"avtNo": attendDailyBonusID, "dailyBonusId": attendDailyBonusID, "todayCount": count,
		"attendable": able, "nextAttdFrom": next,
	}})
}

// ResData @0x1c2a5ac: bonus is an array of exactly bonusCount objects.
func handleAttendInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	var count int
	var able bool
	var h, m int
	var rows []map[string]any
	var sched calSchedule
	now := NowFunc()
	if acc != nil {
		sched = scheduleAt(now)
		count, able, h, m = AttendState(acc, now)
		rows = attendRows(acc, sched, count, able)
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	start, end := sched.window(now)
	httpx.WriteObj(w, map[string]any{"result": map[string]any{
		"avtNo": attendDailyBonusID, "title": sched.Title, "dailyBonusId": attendDailyBonusID,
		"scheduleId": sched.ID, "startYmdt": start, "endYmdt": end,
		"bonusCount": len(sched.Cycle), "todayCount": count, "attendable": able,
		"lastDay":      count == len(sched.Cycle),
		"nextAttdFrom": map[string]string{"hour": fmt.Sprint(h), "min": fmt.Sprint(m)},
		"bonus":        rows,
	}})
}

// handleAttend credits today's reward once per day. A repeat the same day credits nothing
// and still answers 200 with balances (an error reply would raise a generic popup; the
// button is client-disabled once attendable is false, so a repeat is only a race).
func handleAttend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	now := NowFunc()
	if count, able, _, _ := AttendState(acc, now); able {
		prev := *acc
		sched := scheduleAt(now)
		rw := sched.Cycle[count-1]
		reason := sched.reason(count)
		credit := store.LedgerDeltas{Gems: rw.Gems, Cash: rw.Cash, FaceTickets: rw.Tickets}
		var err error
		if rw.Item != "" && seasonItemAvailable(rw.Item) {
			var sellBack int64
			sellBack, err = itemGrantLocked(acc, rw.Item)
			credit.Gems += sellBack // a duplicate's sell-back rides on the day's Gems: one ledger line
			reason += " item " + rw.Item
		}
		var lines []store.LedgerLine
		if err == nil {
			lines, err = store.SpendLocked(acc, credit, reason)
		}
		acc.AttendDay, acc.AttendKey = count, AttendDayKey(now)
		if err == nil {
			err = store.SaveAccountsLocked()
		}
		if err != nil {
			*acc = prev
			httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
			return
		}
		store.FlushLedgerLocked(lines)
	}
	_, _, h, m := AttendState(acc, now)
	httpx.WriteObj(w, map[string]any{"result": map[string]any{
		"totalCoinBalance": acc.Gems, "freeCoinBalance": acc.Gems, "payedCoinBalance": 0,
		"totalCashBalance": acc.Cash, "freeCashBalance": acc.Cash, "payedCashBalance": 0,
		"reviewable":   false,
		"nextAttdFrom": map[string]string{"hour": fmt.Sprint(h), "min": fmt.Sprint(m)},
	}})
}

// ---- staff tool: simulate day changes ----

// POST /admin/attend {"aid":"1002","shiftDays":1} moves the last-attend day back N days
// (default 1) so the next attend counts as a new day; {"aid":"1002","reset":true} clears
// the cycle (next attend = day 1).
//
//	curl -X POST http://127.0.0.1:8099/admin/attend -d "{\"aid\":\"1002\",\"shiftDays\":1}"
func handleAdminAttend(w http.ResponseWriter, r *http.Request) {
	if !adminLoopback(r) {
		httpx.ServeNotFound(w)
		return
	}
	var req struct {
		Aid       string `json:"aid"`
		ShiftDays *int64 `json:"shiftDays"`
		Reset     bool   `json:"reset"`
	}
	if !readAdminJSON(r, &req) || req.Aid == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(req.Aid)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	prev := *acc
	if req.Reset {
		acc.AttendDay, acc.AttendKey = 0, 0
	} else {
		shift := int64(1)
		if req.ShiftDays != nil {
			shift = *req.ShiftDays
		}
		if shift < 0 || shift > 10000 {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		acc.AttendKey -= shift
	}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	count, able, _, _ := AttendState(acc, NowFunc())
	httpx.WriteObj(w, map[string]any{"aid": acc.Aid, "attendDay": acc.AttendDay, "nextDay": count, "attendable": able})
}
