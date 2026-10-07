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

// Amounts in one table so they are easy to tune. Type is the native bonusType string:
// GEM = Gems popup, CASH = Cash popup, VOUCH = Face Shop ticket popup
// (NaAttendanceMainLayer::GetRewardType @0x19196e4). A row's popup shows only its Type
// reward; the other columns are still credited (the HUD refreshes from the attend reply).
var attendCycle = [7]struct {
	Type    string
	Gems    int64
	Cash    int64
	Tickets int64
}{
	{"GEM", 90, 0, 0},
	{"GEM", 120, 0, 0},
	{"GEM", 150, 0, 0},
	{"GEM", 180, 0, 0},
	{"GEM", 240, 0, 0},
	{"GEM", 300, 0, 0},
	{"VOUCH", 450, 15, 3},
}

const (
	attendBoundaryShift = 3 * 3600 // day boundary 06:00 JST = 21:00 UTC
	attendDailyBonusID  = "134"    // atoi() -> "arts_attendance_<lang>_00134_1/" skin version key
)

// NowFunc is the clock; tests replace it.
var NowFunc = time.Now

// AttendDayKey numbers days that start at 06:00 JST.
func AttendDayKey(t time.Time) int64 { return (t.Unix() + attendBoundaryShift) / 86400 }

// AttendState derives the calendar view for an account at time t.
func AttendState(acc *store.Account, t time.Time) (todayCount int, attendable bool, hour, min int) {
	key := AttendDayKey(t)
	attended := acc.AttendKey == key && acc.AttendDay > 0
	todayCount = acc.AttendDay%7 + 1
	if attended {
		todayCount = acc.AttendDay
	}
	rem := (key+1)*86400 - attendBoundaryShift - t.Unix()
	return todayCount, !attended, int(rem / 3600), int(rem % 3600 / 60)
}

func attendRows(acc *store.Account, todayCount int, attendable bool) []map[string]any {
	rows := make([]map[string]any, 0, 7)
	for i, r := range attendCycle {
		day := i + 1
		row := map[string]any{
			"count": day, "attendYN": day < todayCount || (day == todayCount && !attendable),
			"bonusType": r.Type, "bonusCategory": "", "gem": r.Gems, "cash": r.Cash, "heart": 0,
			"powder": 0, "vipPoint": 0, "itemCd": "", "attdYmdt": "", "voucherCode": "",
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
	if acc != nil {
		count, able, h, m = AttendState(acc, NowFunc())
		rows = attendRows(acc, count, able)
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{
		"avtNo": attendDailyBonusID, "title": "Daily Login", "dailyBonusId": attendDailyBonusID,
		"scheduleId": AttendScheduleID, "startYmdt": "20200101000000", "endYmdt": "20991231235959",
		"bonusCount": len(attendCycle), "todayCount": count, "attendable": able,
		"lastDay":      count == len(attendCycle),
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
		rw := attendCycle[count-1]
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: rw.Gems, Cash: rw.Cash, FaceTickets: rw.Tickets}, fmt.Sprintf("daily login day %d", count))
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
