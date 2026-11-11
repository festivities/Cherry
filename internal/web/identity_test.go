package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/economy"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// devSignUp runs the dev-fake sign-in through the game mux (start, callback, exchange) for the identity dev-<as> and returns
// the uniqueCode the exchange minted.
func devSignUp(t *testing.T, game http.Handler, as string) string {
	t.Helper()
	const verifier = "identity-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
	const state = "identitystate0123456"
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		game.ServeHTTP(rec, req)
		return rec
	}
	start := serve(httptest.NewRequest(http.MethodGet,
		"/auth/v1/discord/start?challenge="+pkceChallenge(verifier)+"&return=raspberry%3A%2F%2Fauth&state="+state+"&as="+as, nil))
	done := serve(httptest.NewRequest(http.MethodGet, start.Header().Get("Location"), nil))
	ret, err := url.Parse(done.Header().Get("Location"))
	if err != nil || ret.Query().Get("ticket") == "" {
		t.Fatalf("callback for %s = %d %q", as, done.Code, done.Header().Get("Location"))
	}
	body, _ := json.Marshal(map[string]string{"ticket": ret.Query().Get("ticket"), "verifier": verifier})
	ex := serve(httptest.NewRequest(http.MethodPost, "/auth/v1/exchange", bytes.NewReader(body)))
	var minted struct {
		UniqueCode string `json:"uniqueCode"`
	}
	if ex.Code != http.StatusOK || json.Unmarshal(ex.Body.Bytes(), &minted) != nil || minted.UniqueCode == "" {
		t.Fatalf("exchange for %s = %d %s", as, ex.Code, ex.Body.String())
	}
	return minted.UniqueCode
}

// withAccount runs fn on the account of token under the store lock; the test reads and changes it in place.
func withAccount(t *testing.T, token string, fn func(*store.Account)) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByTokenLocked(token)
	if acc == nil {
		t.Fatal("no account for the token")
	}
	fn(acc)
}

func gemsOf(t *testing.T, token string) (gems int64) {
	t.Helper()
	withAccount(t, token, func(acc *store.Account) { gems = acc.Gems })
	return gems
}

// createAvatarID creates an avatar with the given payload and returns its aid.
func createAvatarID(t *testing.T, avAuth, payload string) string {
	t.Helper()
	rec := createAvatar(t, avAuth, []byte(payload), "")
	var avatar avatarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &avatar); err != nil || rec.Code != http.StatusOK || avatar.Result == nil || avatar.Result.AvatarID == "" {
		t.Fatalf("create avatar = %d %s", rec.Code, rec.Body.String())
	}
	return avatar.Result.AvatarID
}

// TestIdentityModeNoTokenActsOnNoAccount: in discord and dev-fake a request without a known token acts on no account: the
// welcome gift gets the stock no-gift reply, a spend is refused as an unknown session, createSession is 404, and a profile
// write is refused; the balances do not move.
func TestIdentityModeNoTokenActsOnNoAccount(t *testing.T) {
	cfg := devFakeConfig()
	useConfig(t, cfg)
	guestAccountTest(t)
	svc, err := auth.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := guestGenerateCode(t, devSignUp(t, NewGameMux(svc), "idb"))
	av := avAuthValue(t, createSession(t, tok))
	createAvatarID(t, av, `{"name":"Bea","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`)
	withAccount(t, tok, func(acc *store.Account) { acc.Gems, acc.FaceTickets = 500, 2 })

	rec := decorReq(t, "", http.MethodPost, "/v4/create/complete", "{}")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rewardCoin":0`) {
		t.Fatalf("welcome gift without a token = %d %s, want the no-gift reply", rec.Code, rec.Body.String())
	}
	rec = decorReq(t, "", http.MethodPost, faceShopPurchaseURL, `{"saveItemList":[{"itemCode":"CUEY0030Q"}],"useVoucher":"true"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a spend without a token = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if rec := createSession(t, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("createSession without a token = %d, want 404", rec.Code)
	}
	if rec := decorReq(t, "", http.MethodPost, "/v4/profile/image", `{"imageUrl":"/lineplay/pr/1_1","wbImageUrl":"/lineplay/pr/1_1","bgUrl":"profilebg/1.jpg"}`); rec.Code == http.StatusOK {
		t.Fatalf("a profile write without a token = %d, want a refusal", rec.Code)
	}
	withAccount(t, tok, func(acc *store.Account) {
		if acc.Gems != 500 || acc.FaceTickets != 2 || acc.Welcomed {
			t.Fatalf("a token-less request moved the identity's account: gems %d tickets %d welcomed %v", acc.Gems, acc.FaceTickets, acc.Welcomed)
		}
	})
	if rec := decorReq(t, tok, http.MethodPost, "/v4/create/complete", "{}"); !strings.Contains(rec.Body.String(), `"rewardCoin":300`) {
		t.Fatalf("the owner's own welcome gift = %d %s", rec.Code, rec.Body.String())
	}
}

// freshGiftsWorthNothing asserts that the account's creation items and starter room items are owned and each recycles for 0
// Gems: the gift marker (store.GiftPaid) is on every one of them.
func freshGiftsWorthNothing(t *testing.T, token string, creation ...string) {
	t.Helper()
	withAccount(t, token, func(acc *store.Account) {
		for _, code := range creation {
			if !slices.Contains(store.AccountOwnedCodes(acc), code) {
				t.Fatalf("creation item %s is not owned", code)
			}
			if price, _ := economy.ClosetPrice(acc.PaidPrices, code); price != 0 || acc.PaidPrices[code] != store.GiftPaid {
				t.Fatalf("creation item %s sells for %d (marker %d), want 0 and the gift marker", code, price, acc.PaidPrices[code])
			}
		}
		if len(acc.RoomItems) == 0 {
			t.Fatal("the starter room is empty")
		}
		for _, it := range acc.RoomItems {
			if price, _ := economy.RoomItemPrice(it); price != 0 || it.Paid != store.GiftPaid {
				t.Fatalf("starter room item %s sells for %d (paid %d), want 0 and the gift marker", it.Cd, price, it.Paid)
			}
		}
	})
}

// recycleCloset sells one owned clothing item through the closet recycle route. The item is taken off first (a worn item is
// refused as in use); the balance after the sale is returned.
func recycleCloset(t *testing.T, token, code string) (balance float64) {
	t.Helper()
	withAccount(t, token, func(acc *store.Account) { acc.ItemCodes = nil })
	rec := decorReq(t, token, http.MethodPost, "/v4/inven/recycle/closet/v2",
		`{"usePurchasePrice":true,"appliedRecycleMultiple":1.0,"recycleItems":[{"invenSeq":1,"itemCode":"`+code+`","count":1,"exepectedPrice":"0","price":"0"}]}`+"\x00")
	var out struct {
		Result struct {
			Balance float64 `json:"balance"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("recycle %s = %d %s", code, rec.Code, rec.Body.String())
	}
	return out.Result.Balance
}

// TestDeleteAvatarKeepsOnceGrantsForTheIdentity: the welcome gift and the once-only grants stay with the identity across
// Delete Avatar; the creation items and the starter room are granted again on every creation, and they are gifts worth 0.
// The old tokens end with the delete (AV_AUTH, the own token and the sign-in alias); the same identity signs in again to a
// fresh avatar, reaching the same identity record.
func TestDeleteAvatarKeepsOnceGrantsForTheIdentity(t *testing.T) {
	cfg := devFakeConfig()
	useConfig(t, cfg)
	guestAccountTest(t)
	svc, err := auth.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	game := NewGameMux(svc)

	tok := guestGenerateCode(t, devSignUp(t, game, "ida"))
	if sess := sessionOf(t, tok); sess.Aid != "0" {
		t.Fatalf("a new identity's session aid = %s, want 0", sess.Aid)
	}
	var ownToken string
	withAccount(t, tok, func(acc *store.Account) { ownToken = acc.AccessToken })
	av := avAuthValue(t, createSession(t, tok))
	first := createAvatarID(t, av, `{"name":"Ida","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`)
	if rec := decorReq(t, av, http.MethodPost, "/v4/create/complete", "{}"); !strings.Contains(rec.Body.String(), `"rewardCoin":300`) {
		t.Fatalf("first welcome gift = %d %s", rec.Code, rec.Body.String())
	}
	withAccount(t, tok, func(acc *store.Account) {
		if err := room.EnsureRoomLocked(acc); err != nil || len(acc.RoomItems) == 0 {
			t.Fatalf("the first avatar got no starter room: %v, %d items", err, len(acc.RoomItems))
		}
	})
	freshGiftsWorthNothing(t, tok, "CUON00164")

	rec := socialTestRequestAs(t, av, http.MethodGet, "/v4/avatar/"+first+"/removeCode", "")
	var del struct {
		Result struct {
			Key  string `json:"key"`
			Code int    `json:"code"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &del) != nil || del.Result.Key == "" {
		t.Fatalf("removeCode = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, av, http.MethodPost, "/v4/r/avatar/"+first+"/"+del.Result.Key+"/"+strconv.Itoa(del.Result.Code), "null"); rec.Code != http.StatusOK {
		t.Fatalf("Delete Avatar = %d %s", rec.Code, rec.Body.String())
	}

	// Every token of the deleted avatar ends: the own token, the sign-in alias and the AV_AUTH the client held.
	store.AccountsMu.Lock()
	revoked := store.AccountByTokenLocked(ownToken) == nil && store.AccountByTokenLocked(tok) == nil && store.AccountByTokenLocked(av) == nil
	store.AccountsMu.Unlock()
	if !revoked {
		t.Fatal("a token of the deleted avatar still resolves to an account")
	}
	if rec := checkSession(t, av); rec.Code != http.StatusNotFound {
		t.Fatalf("checkSession with the old AV_AUTH = %d, want 404", rec.Code)
	}

	tok2 := guestGenerateCode(t, devSignUp(t, game, "ida"))
	if sess := sessionOf(t, tok2); sess.Aid != "0" {
		t.Fatalf("the identity signed in again to aid %s, want a fresh avatar (0)", sess.Aid)
	}
	store.AccountsMu.Lock()
	same := store.IdentityAccountLocked("dev-ida") == store.AccountByTokenLocked(tok2) && store.AccountByTokenLocked(tok2) != nil
	store.AccountsMu.Unlock()
	if !same {
		t.Fatal("the second sign-in did not reach the identity's account")
	}
	av2 := avAuthValue(t, createSession(t, tok2))
	second := createAvatarID(t, av2, `{"name":"Ida2","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUAE00B9R"]}`)
	if second == first || second == "0" {
		t.Fatalf("the fresh avatar's aid = %s (the deleted one was %s)", second, first)
	}

	if rec := decorReq(t, av2, http.MethodPost, "/v4/create/complete", "{}"); !strings.Contains(rec.Body.String(), `"rewardCoin":0`) || gemsOf(t, tok2) != 0 {
		t.Fatalf("the welcome gift was paid again: %d %s, gems %d", rec.Code, rec.Body.String(), gemsOf(t, tok2))
	}
	withAccount(t, tok2, func(acc *store.Account) {
		if err := room.EnsureRoomLocked(acc); err != nil || len(acc.RoomItems) == 0 {
			t.Fatalf("the starter room was not granted to the fresh avatar: %v, %d items", err, len(acc.RoomItems))
		}
	})
	freshGiftsWorthNothing(t, tok2, "CUAE00B9R")
	if balance := recycleCloset(t, tok2, "CUAE00B9R"); balance != 0 {
		t.Fatalf("selling a re-granted creation item paid %v, want 0", balance)
	}
	welcome := 0
	for _, line := range readLedger(t) {
		if line.Reason == "welcome gift" {
			welcome++
		}
	}
	if welcome != 1 {
		t.Fatalf("welcome gift ledger lines = %d, want one", welcome)
	}
}

// TestLabShowcaseOnlyInLabGuest: the showcase pets of a lab aid are granted in lab-guest only.
func TestLabShowcaseOnlyInLabGuest(t *testing.T) {
	acc := &store.Account{AccessToken: "labtok", SessionKey: "sk", Aid: "1001", Name: "Lab"}
	testutil.InstallAccounts(t, map[string]*store.Account{"labtok": acc})
	testutil.UseDatabase(t)

	useConfig(t, devFakeConfig())
	store.AccountsMu.Lock()
	err := room.EnsurePetsLocked(acc)
	store.AccountsMu.Unlock()
	if err != nil || len(acc.Pets) != 0 {
		t.Fatalf("identity mode granted %d showcase pets (err %v)", len(acc.Pets), err)
	}

	useConfig(t, config.Config{})
	store.AccountsMu.Lock()
	err = room.EnsurePetsLocked(acc)
	store.AccountsMu.Unlock()
	if err != nil || len(acc.Pets) == 0 {
		t.Fatalf("lab-guest granted no showcase pets (err %v)", err)
	}
}

// TestCreationKitSellsAtItsPriceInLabGuest: lab-guest is unchanged: the creation item and the starter room items are not
// gifts, so they sell at their table price, as before identity mode existed.
func TestCreationKitSellsAtItsPriceInLabGuest(t *testing.T) {
	useConfig(t, config.Config{})
	guestAccountTest(t)
	token := avAuthValue(t, createSession(t, guestGenerate(t)))
	createAvatarID(t, token, `{"name":"Lab","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"]}`)
	withAccount(t, token, func(acc *store.Account) {
		if err := room.EnsureRoomLocked(acc); err != nil || len(acc.RoomItems) == 0 {
			t.Fatalf("the lab avatar got no starter room: %v", err)
		}
		for _, it := range acc.RoomItems {
			if it.Paid != 0 {
				t.Fatalf("lab-guest marked starter item %s as a gift (paid %d)", it.Cd, it.Paid)
			}
		}
		if acc.PaidPrices["CUON00164"] != 0 {
			t.Fatalf("lab-guest marked the creation item (paid %d)", acc.PaidPrices["CUON00164"])
		}
		if price, _ := economy.ClosetPrice(acc.PaidPrices, "CUON00164"); price <= 0 {
			t.Fatalf("the creation item sells for %d in lab-guest, want its table price", price)
		}
	})
	price, _ := withAccountPrice(t, token, "CUON00164")
	if balance := recycleCloset(t, token, "CUON00164"); balance != float64(price) {
		t.Fatalf("selling the creation item paid %v, want its table price %d", balance, price)
	}
}

// withAccountPrice is the closet sell price of code for the account of token.
func withAccountPrice(t *testing.T, token, code string) (int64, string) {
	t.Helper()
	var price int64
	var grade string
	withAccount(t, token, func(acc *store.Account) { price, grade = economy.ClosetPrice(acc.PaidPrices, code) })
	return price, grade
}
