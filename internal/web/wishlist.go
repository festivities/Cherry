package web

import (
	"log"
	"net/http"
	"slices"
	"strings"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Wish list: the Gift Shop tip "View items in your wish list here", the Gift Shop wish list popup, a profile's
// wish list and the postbox. Client side (IDA, menu audit 2026-10-28):
//
//	GET  /v4/diary/wish/list/<aid> -> {"result":[{"itemCode","itemType","selling","specialEffects"}...]}
//	     diary2WishList 0x1ad6364 = baseHostName(1) + "diary/wish" + "/list/" + aid. ParseWishListJsonData 0x23757d0
//	     iterates result as an array. CNaGiftShopLayer::init 0x228501c asks for the logged-in aid; the profile popup
//	     (NaProfilePopupData::ShowPopup case 7, 0x224e8f0) asks for the profile owner's aid.
//	POST /v4/diary/wish/save       body ["<code>",...], the whole new list -> {"result":true}
//	     ReqSaveWishListData 0x235c644 builds the body from the codes only; diary2WishListSave 0x1ad6470 is
//	     "diary/wish/save"; ResDiaryWishListSave 0x1a83508 reads result as a bool and posts 11361 (no consumer found).
//
// A failed GET or save goes to ErrDiaryWishList 0x1abb67c / ErrDiaryWishListSave 0x1abb708, which show
// DiaryDefaultErrPopup or NetworkErrPopup 0x1aba460 (10077 "An unexpected error has occurred").
// [I] Visibility: the reply has no visibility field. The owner and accepted friends read a list; anyone else gets
// an empty one (200), so no error popup shows.

func handleWishList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary/wish/list/"), "/")
	if !store.ValidAvatarID(aid) {
		httpx.ServeNotFound(w)
		return
	}
	var codes []string
	if wishVisible(viewer.Aid, aid) {
		codes = wishesOf(aid)
	}
	httpx.WriteObj(w, map[string]any{"result": economy.WishRows(codes)})
}

func handleWishSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var codes []string
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &codes)
	}
	if err != nil || codes == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	found, saved, err := editWishes(httpx.CookieValue(r, "AV_AUTH"), func(prev []string) ([]string, bool) {
		if !validWishList(prev, codes) {
			return nil, false
		}
		return slices.Clone(codes), true
	})
	switch {
	case !found:
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
	case !saved:
		log.Printf("cherry: wish save refused (%d codes)", len(codes)) // counts only, never the codes
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
	case err != nil:
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
	default:
		httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
	}
}

// validWishList accepts at most economy.WishlistMax distinct codes. A code the list does not hold yet must be sold
// now; codes already on the list stay even when the shops stop selling them, so the player can still remove them.
func validWishList(prev, next []string) bool {
	if len(next) > economy.WishlistMax {
		return false
	}
	for i, code := range next {
		if slices.Index(next, code) != i {
			return false
		}
		if !slices.Contains(prev, code) && !economy.WishlistOK(code) {
			return false
		}
	}
	return true
}

// wishVisible: the owner and accepted friends read a wish list.
func wishVisible(viewer, owner string) bool {
	if viewer == owner {
		return true
	}
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	return store.FriendshipIndex(viewer, owner, "accepted") >= 0
}

// wishesOf copies the wish list of the account with avatar id aid. The synthetic friend and deleted accounts have none.
func wishesOf(aid string) []string {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil
	}
	return slices.Clone(acc.Wishes)
}

// editWishes replaces the wish list of the account behind token with edit(prev) and saves it; on a save error the
// old list comes back (the Account.Pins pattern). found is false without an account; saved is false when edit
// refuses the list. Lock order: accountsMu only.
func editWishes(token string, edit func(prev []string) ([]string, bool)) (found, saved bool, err error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[token]
	if acc == nil {
		return false, false, nil
	}
	next, valid := edit(acc.Wishes)
	if !valid {
		return true, false, nil
	}
	prev := acc.Wishes
	acc.Wishes = next
	if err = store.SaveAccountsLocked(); err != nil {
		acc.Wishes = prev
		return true, true, err
	}
	return true, true, nil
}
