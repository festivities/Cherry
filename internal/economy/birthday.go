package economy

// Avatar birthday reward (birthday-reddot-20261121/spec.md sections 1 and 4). On an avatar's birthday (the anniversary
// of its creation, GMT+9) the Garden shows an icon while that turn's claim window is open (home/integrated/info
// birthday_home). The icon opens the receive list (birthday/info/year); the claim (birthday/reward/year/<turn>) is
// recorded once and gives a Gem gacha discount for discount_days. The turn is the full years since creation (1 for the
// first birthday). The numbers are Cherry-made: rewards.json birthday.

import (
	"encoding/json"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// birthdayZone is GMT+9, the calendar the client shows.
var birthdayZone = time.FixedZone("JST", 9*3600)

// IsAvatarBirthday: now (GMT+9) is an anniversary of the creation time createdMs, a year or more after it. The creation
// day itself is not a birthday. It draws the Garden hat (profile birthday, web/social.go).
func IsAvatarBirthday(createdMs int64, now time.Time) bool {
	if createdMs <= 0 {
		return false
	}
	born, today := time.UnixMilli(createdMs).In(birthdayZone), now.In(birthdayZone)
	return today.Year() > born.Year() && today.Month() == born.Month() && today.Day() == born.Day()
}

// birthdayAnniversary is midnight (GMT+9) of the birthday of born in year. ok is false in a year without that date
// (29 February in a common year), the same rule IsAvatarBirthday uses.
func birthdayAnniversary(born time.Time, year int) (time.Time, bool) {
	d := time.Date(year, born.Month(), born.Day(), 0, 0, 0, 0, birthdayZone)
	return d, d.Month() == born.Month() && d.Day() == born.Day()
}

// birthdayTurn is the turn of the latest birthday on or before now, and the end (Unix ms) of its claim window: claimDays
// days from that midnight. Turn 0 means no birthday yet (no creation time, or younger than a year).
func birthdayTurn(createdMs int64, now time.Time, claimDays int) (turn int, windowEnd int64) {
	if createdMs <= 0 {
		return 0, 0
	}
	born := time.UnixMilli(createdMs).In(birthdayZone)
	today := now.In(birthdayZone)
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, birthdayZone)
	for year := today.Year(); year > born.Year(); year-- {
		if d, ok := birthdayAnniversary(born, year); ok && !d.After(todayStart) {
			return year - born.Year(), d.AddDate(0, 0, claimDays).UnixMilli()
		}
	}
	return 0, 0
}

// birthdayDiscountEnd is when the gacha discount of a claim made at claimMs ends (Unix ms).
func birthdayDiscountEnd(claimMs int64) int64 {
	return claimMs + (time.Duration(ActiveRewards().Birthday.DiscountDays) * 24 * time.Hour).Milliseconds()
}

// birthdayState is the receive list of an avatar created at createdMs: its turn, status and expiry (Unix ms: the window
// end, or the discount end once claimed).
func birthdayState(createdMs int64, claims map[int]int64, now time.Time) (int, string, int64) {
	turn, end := birthdayTurn(createdMs, now, ActiveRewards().Birthday.ClaimDays)
	switch {
	case turn == 0:
		return 0, "UNQUALIFIED", 0
	case claims[turn] != 0:
		return turn, "RECEIVED", birthdayDiscountEnd(claims[turn])
	case now.UnixMilli() < end:
		return turn, "RECEIVABLE", end
	}
	return turn, "EXPIRED", end
}

// birthdayOffer is the Gem gacha discount an account has now: percent off, and when its claim started and the discount
// ends (Unix ms). The zero value is no discount.
type birthdayOffer struct{ Percent, Start, End int64 }

func (o birthdayOffer) active() bool { return o.Percent > 0 }

// birthdayOfferFor is the discount of the latest claim, while it lasts.
func birthdayOfferFor(claims map[int]int64, now time.Time) birthdayOffer {
	var claimed int64
	for _, ms := range claims {
		claimed = max(claimed, ms)
	}
	if claimed == 0 || now.UnixMilli() >= birthdayDiscountEnd(claimed) {
		return birthdayOffer{}
	}
	return birthdayOffer{Percent: ActiveRewards().Birthday.GachaDiscountPercent, Start: claimed, End: birthdayDiscountEnd(claimed)}
}

// birthdayPriceOff is price less percent, rounded down and at least 1 Gem. A free price stays free; percent 0 keeps it.
func birthdayPriceOff(price, percent int64) int64 {
	if price <= 0 || percent <= 0 {
		return price
	}
	return max(price*(100-percent)/100, 1)
}

// gachaOfferFor is the requester's birthday discount (none without an avatar).
func gachaOfferFor(r *http.Request) birthdayOffer {
	snap, ok := store.AccountForRequest(r)
	if !ok {
		return birthdayOffer{}
	}
	return birthdayOfferFor(snap.BirthdayClaims, NowFunc())
}

// birthdayInfo is result of birthday/info/year (spec 1.3): avatarTurn and gachaDiscount asInt, expireDate asString (Unix
// ms), status asString.
type birthdayInfo struct {
	AvatarTurn    int    `json:"avatarTurn"`
	GachaDiscount int64  `json:"gachaDiscount"`
	ExpireDate    string `json:"expireDate"`
	Status        string `json:"status"`
}

// birthdayInfoFor is the receive list of the requester; without an avatar it is UNQUALIFIED.
func birthdayInfoFor(r *http.Request) birthdayInfo {
	snap, ok := store.AccountForRequest(r)
	if !ok {
		return birthdayInfo{Status: "UNQUALIFIED", ExpireDate: "0"}
	}
	turn, status, expire := birthdayState(store.AvatarBirthday(snap.Aid), snap.BirthdayClaims, NowFunc())
	info := birthdayInfo{AvatarTurn: turn, Status: status, ExpireDate: strconv.FormatInt(expire, 10)}
	if status == "RECEIVABLE" || status == "RECEIVED" {
		info.GachaDiscount = ActiveRewards().Birthday.GachaDiscountPercent
	}
	return info
}

// birthdayHome is result.birthday_home (spec 1.5 and 4.1): displayYn (asBool) shows the Garden icon of the birthday list,
// birthdayType "year" is its type; the nextPush keys drive local pushes and are off at "0". The turns are asInt.
type birthdayHome struct {
	DisplayYn        bool   `json:"displayYn"`
	BirthdayType     string `json:"birthdayType"`
	NextPushDay      string `json:"nextPushDay"`
	NextPushYear     string `json:"nextPushYear"`
	NextPushYearTurn int    `json:"nextPushYearTurn"`
	NextPushDayTurn  int    `json:"nextPushDayTurn"`
}

// birthdayHomeFor: the icon is on while the requester's claim window is open and its turn is not claimed.
func birthdayHomeFor(r *http.Request) birthdayHome {
	info := birthdayInfoFor(r)
	return birthdayHome{DisplayYn: info.Status == "RECEIVABLE", BirthdayType: "year", NextPushDay: "0", NextPushYear: "0",
		NextPushYearTurn: info.AvatarTurn, NextPushDayTurn: info.AvatarTurn}
}

// HandleBirthdayInfoYear serves GET birthday/info/year: the receive list of the requester.
func HandleBirthdayInfoYear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteObj(w, struct {
		Result birthdayInfo `json:"result"`
	}{birthdayInfoFor(r)})
}

// HandleBirthdayInfoDay serves GET birthday/info/day: the service-anniversary list, which Cherry does not offer.
func HandleBirthdayInfoDay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteObj(w, struct {
		Result birthdayInfo `json:"result"`
	}{birthdayInfo{Status: "UNQUALIFIED", ExpireDate: "0"}})
}

// HandleBirthdayHome serves GET birthday/home: the birthday_home object of the requester (displayYn false when off).
func HandleBirthdayHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteObj(w, struct {
		Result birthdayHome `json:"result"`
	}{birthdayHomeFor(r)})
}

// birthdayClaimReply is the result of birthday/reward/year/<turn>: true records the claim, false changes nothing.
type birthdayClaimReply struct {
	Result bool `json:"result"`
}

// HandleBirthdayRewardYear serves GET birthday/reward/year/<turn> (spec 1.4). The turn must be the requester's current
// turn, its window must be open and the turn unclaimed; then the claim is recorded once. Anything else is false.
func HandleBirthdayRewardYear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	turn, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v4/birthday/reward/year/"))
	snap, ok := store.AccountForRequest(r)
	if err != nil || !ok {
		httpx.WriteObj(w, birthdayClaimReply{})
		return
	}
	created := store.AvatarBirthday(snap.Aid)
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByTokenLocked(httpx.CookieValue(r, "AV_AUTH"))
	now := NowFunc()
	cur, end := birthdayTurn(created, now, ActiveRewards().Birthday.ClaimDays)
	if acc == nil || acc.Aid != snap.Aid || cur == 0 || turn != cur || now.UnixMilli() >= end || acc.BirthdayClaims[turn] != 0 {
		httpx.WriteObj(w, birthdayClaimReply{})
		return
	}
	prev := *acc
	claims := make(map[int]int64, len(acc.BirthdayClaims)+1) // replaced, never edited in place
	maps.Copy(claims, acc.BirthdayClaims)
	claims[turn] = now.UnixMilli()
	acc.BirthdayClaims = claims
	if err := store.SaveLocked(nil, acc); err != nil { // no ledger line: a claim moves no balance
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteObj(w, birthdayClaimReply{Result: true})
}

// birthdayHomeIntegrated is the birthday_home member of home/integrated/info. An avatar that has had no birthday yet gets
// none (the reply stays as it was device-verified). After its first birthday the member is always sent, displayYn true only
// while the claim is open: the client parses birthday_home only when present, so displayYn false is what clears an icon
// after a claim or when the window ends (spec 4.1).
func birthdayHomeIntegrated(r *http.Request) string {
	h := birthdayHomeFor(r)
	if h.NextPushYearTurn == 0 {
		return ""
	}
	b, _ := json.Marshal(h) // ints and strings only: cannot fail
	return `,"birthday_home":` + string(b)
}
