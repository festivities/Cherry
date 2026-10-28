package economy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// giftFixture: CUTO00001 (tops) and RUCH00001 (chair) with art. Sender 9601 (Gems) and receiver 9602, linked by a
// friendship in state relation ("" = none). Ledger lines go to the returned dir.
func giftFixture(t *testing.T, gems int64, relation string) (dir string) {
	t.Helper()
	shopFixture(t, map[string][]string{
		"dress/225000001":    {"dp.png"},
		"interior/120400001": {"dp.png"},
	})
	testutil.InstallAccounts(t, map[string]*store.Account{
		"gs-send": {AccessToken: "gs-send", SessionKey: "sk", Aid: "9601", Name: "Sender", Gender: "FEMALE", Gems: gems},
		"gs-recv": {AccessToken: "gs-recv", SessionKey: "sk", Aid: "9602", Name: "Receiver", Gender: "MALE"},
	})
	testutil.SetLab(t, "9601", false)
	testutil.SetLab(t, "9602", false)
	dir = gachaLedgerDir(t)
	store.SocialMu.Lock()
	old := store.Friendships
	store.Friendships = nil
	if relation != "" {
		store.Friendships = []store.Friendship{{A: "9601", B: "9602", State: relation}}
	}
	store.SocialMu.Unlock()
	t.Cleanup(func() { store.SocialMu.Lock(); store.Friendships = old; store.SocialMu.Unlock() })
	return dir
}

func giftPost(token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v4/gift/send", strings.NewReader(body))
	req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	rec := httptest.NewRecorder()
	HandleGiftSend(rec, req)
	return rec
}

func giftBody(aid, code string, price int64, coinUnit, msg string) string {
	return fmt.Sprintf(`{"language":"ja","deviceType":"Android","receiveUser":{"aid":%q},"coinUnit":%q,"giftMessage":%q,`+
		`"itemType":"DRESS","items":[{"itemCode":%q,"price":%d}],"brandShopId":"","brandShopNo":"","cpId":""}`, aid, coinUnit, msg, code, price)
}

// giftReply decodes the result object of a gift reply.
func giftReply(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Result == nil {
		t.Fatalf("gift reply status %d body %q: %v", rec.Code, rec.Body.String(), err)
	}
	return out.Result
}

// giftState reads an account's Gems and mailbox under the account lock.
func giftState(aid string) (int64, []store.MailRow) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	return acc.Gems, slices.Clone(acc.Mail)
}

func giftList(t *testing.T, cat, special string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleGiftShopList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/gift/list/"+cat+"/"+special, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("gift list %s/%s: status %d", cat, special, rec.Code)
	}
	return rec.Body.String()
}

// TestGiftListShape: DRESS and ROOM list their items as an array (Gems, sold now, art); FACE, GARDEN and unknown
// categories get the empty array; every SPECIAL lists the same rows.
func TestGiftListShape(t *testing.T) {
	phaseEClock(t, time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))
	giftFixture(t, 0, "accepted")
	const code = "CUTO00001"
	dress := giftList(t, "DRESS", "ALL")
	var out struct {
		Result struct {
			Items []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(dress), &out); err != nil {
		t.Fatalf("DRESS body %q: %v", dress, err)
	}
	if !strings.Contains(dress, `"items":[{`) {
		t.Fatalf("DRESS items is not a non-empty array: %s", dress)
	}
	var row map[string]any
	for _, it := range out.Result.Items {
		if it["itemCode"] == code {
			row = it
		}
	}
	if row == nil {
		t.Fatalf("DRESS lacks %s: %s", code, dress)
	}
	if want := giftPrice(code, dealsOn(NowFunc())); row["discountedPrice"] != float64(want) || row["paymentType"] != "GEM" || row["itemType"] != "DRESS" {
		t.Errorf("DRESS row %s = %v, want discountedPrice %d paymentType GEM itemType DRESS", code, row, want)
	}
	if strings.Contains(dress, "RUCH00001") {
		t.Errorf("DRESS lists a furniture item")
	}
	room := giftList(t, "ROOM", "ALL")
	if !strings.Contains(room, `"itemCode":"RUCH00001"`) || strings.Contains(room, code) {
		t.Errorf("ROOM = %s, want RUCH00001 only", room)
	}
	if got := giftList(t, "DRESS", "PREMIUM"); got != dress {
		t.Errorf("DRESS/PREMIUM differs from DRESS/ALL")
	}
	for _, cat := range []string{"FACE", "GARDEN", "BOGUS"} {
		if got := giftList(t, cat, "ALL"); got != emptyItems {
			t.Errorf("%s = %s, want %s", cat, got, emptyItems)
		}
	}
}

// TestGiftSendHappy: the price is charged in Gems on the sender's ledger and the item lands in the receiver's
// mailbox with the sender's name and the gift message; the reply carries the sender's new balance.
func TestGiftSendHappy(t *testing.T) {
	phaseEClock(t, time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))
	dir := giftFixture(t, 1000, "accepted")
	const code = "CUTO00001"
	want := giftPrice(code, dealsOn(NowFunc()))
	rec := giftPost("gs-send", giftBody("9602", code, want, "GEM", "enjoy"))
	res := giftReply(t, rec)
	if rec.Code != http.StatusOK || res["balance"] != float64(1000-want) || res["balanceUnit"] != "GEM" {
		t.Fatalf("send = %d %v, want 200 balance %d GEM", rec.Code, res, 1000-want)
	}
	if gems, _ := giftState("9601"); gems != 1000-want {
		t.Errorf("sender gems = %d, want %d", gems, 1000-want)
	}
	_, mail := giftState("9602")
	if len(mail) != 1 || mail[0].Type != store.MailTypeItem || mail[0].Item != code || mail[0].Message != "enjoy" || mail[0].Sender != "Sender" {
		t.Fatalf("receiver mail = %+v, want one item %s from Sender saying enjoy", mail, code)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 1 || lines[0].Aid != "9601" || lines[0].Currency != "gems" || lines[0].Delta != -want {
		t.Errorf("ledger = %+v, want one gems line of %d for 9601", lines, -want)
	}
}

// TestGiftSendRefusals: each refusal answers 200 with result.errorCode and changes neither side.
func TestGiftSendRefusals(t *testing.T) {
	phaseEClock(t, time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))
	const code = "CUTO00001"
	const cash = "GULS00003" // a listed Garden landscape: Cash only
	cases := []struct {
		name, relation, aid, code, coin string
		gems, off                       int64 // off: the price sent, relative to the current price
		errCode                         string
	}{
		{"not a friend", "", "9602", code, "GEM", 1000, 0, errUnknown},
		{"request still pending", "pending", "9602", code, "GEM", 1000, 0, errUnknown},
		{"synthetic friend has no account", "accepted", store.FriendAID, code, "GEM", 1000, 0, errUnknown},
		{"self", "accepted", "9601", code, "GEM", 1000, 0, errUnknown},
		{"wrong price", "accepted", "9602", code, "GEM", 1000, 1, errUpdated},
		{"Cash coin unit", "accepted", "9602", code, "CASH", 1000, 0, errUnknown},
		{"Cash landscape", "accepted", "9602", cash, "GEM", 1000, 0, errUnknown},
		{"not enough Gems", "accepted", "9602", code, "GEM", 0, 0, errNoMoney},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			giftFixture(t, tc.gems, tc.relation) // the catalog (and so the deal set) is read after the fixture
			want := giftPrice(code, dealsOn(NowFunc()))
			rec := giftPost("gs-send", giftBody(tc.aid, tc.code, want+tc.off, tc.coin, "hi"))
			res := giftReply(t, rec)
			if rec.Code != http.StatusOK || res["errorCode"] != tc.errCode {
				t.Fatalf("reply = %d %v, want 200 errorCode %s", rec.Code, res, tc.errCode)
			}
			if gems, _ := giftState("9601"); gems != tc.gems {
				t.Errorf("sender gems = %d, want %d unchanged", gems, tc.gems)
			}
			if _, mail := giftState("9602"); len(mail) != 0 {
				t.Errorf("receiver mail = %+v, want none", mail)
			}
		})
	}
}

// TestGiftSendMailboxFullRollsBack: a full receiver mailbox refuses the gift and the sender keeps the Gems.
func TestGiftSendMailboxFullRollsBack(t *testing.T) {
	phaseEClock(t, time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))
	giftFixture(t, 1000, "accepted")
	store.AccountsMu.Lock()
	recv := store.AccountByAidLocked("9602")
	for i := range MailMaxRows() {
		recv.Mail = append(recv.Mail, store.MailRow{Seq: int64(i + 1), Type: store.MailTypeGems, Amount: 1, Expires: NowFunc().Add(time.Hour).UnixMilli()})
	}
	store.AccountsMu.Unlock()
	const code = "CUTO00001"
	rec := giftPost("gs-send", giftBody("9602", code, giftPrice(code, dealsOn(NowFunc())), "GEM", ""))
	if res := giftReply(t, rec); res["errorCode"] != errUnknown {
		t.Fatalf("reply = %v, want errorCode %s", res, errUnknown)
	}
	if gems, _ := giftState("9601"); gems != 1000 {
		t.Errorf("sender gems = %d after a refused gift, want 1000", gems)
	}
}

// TestGiftOwnedItemSellsBackOnReceive: a gift the receiver already owns pays the receiver its sell-back Gems when
// the mail is received (the existing item grant rule), and grants nothing new.
func TestGiftOwnedItemSellsBackOnReceive(t *testing.T) {
	phaseEClock(t, time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC))
	giftFixture(t, 0, "accepted")
	const code = "CUTO00001"
	store.AccountsMu.Lock()
	recv := store.AccountByAidLocked("9602")
	recv.InventoryCodes = []string{code}
	lines, err := receiveLocked(recv, store.MailRow{Seq: 1, Type: store.MailTypeItem, Item: code, Expires: NowFunc().Add(time.Hour).UnixMilli()})
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if gems, _ := giftState("9602"); gems != sellBackGems(code) || len(lines) != 1 {
		t.Errorf("receiver gems = %d lines %d, want sell-back %d in one line", gems, len(lines), sellBackGems(code))
	}
	if got := store.AccountOwnedCodes(recv); len(got) != 1 {
		t.Errorf("owned = %v, want the one code it had", got)
	}
}
