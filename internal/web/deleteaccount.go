package web

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// MENU > Settings > Delete Avatar (libgame NaSettingMainLayer::ShowDeleteAvataPopup ->
// NaSettingProfileDeleteAvataPopupLayer). Two requests, both with the AV_AUTH cookie:
//
//	GET  /v4/avatar/<avatarId>/removeCode     -> {"result":{"key":"<str>","code":<int>}}
//	     (ResAvatarDeleteCodeWithAvatarID @0x1a5be0c: result.key asString, result.code asInt;
//	     the popup shows the code, the player types it back, then confirms with string 15575)
//	POST /v4/r/avatar/<avatarId>/<key>/<code> -> {"result":true}  (body "null")
//	     (ResAvatarDeleteWithAvatarID @0x1b6a5d8: result asBool; true runs NaDataInvoker::DeleteAvatar,
//	     which wipes the client's local state and returns to the login scene.)
//
// Any non-2xx reply lands in ErrAvatarDeleteWithAvatarID @0x1a86a50, which always shows string
// 10130 "Network Error. Please try again later." (the 1011/1014 branches of ShowErrorDelete are
// unreachable: the error message always carries 0), so a wrong key/code uses 400 for that popup.
// Waiting periods ("8030: cannot delete any more avatars today") are not modelled.

const deleteCodeTTL = 10 * time.Minute

type pendingDelete struct {
	key     string
	code    int
	expires time.Time
}

var (
	deleteCodesMu sync.Mutex
	deleteCodes   = map[string]pendingDelete{} // by access token alias
)

func registerDeleteAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/avatar/{id}/removeCode", handleDeleteCode)
	mux.HandleFunc("/v4/r/avatar/{id}/{key}/{code}", handleAvatarDelete)
}

func handleDeleteCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !httpx.IsAllDigits(r.PathValue("id")) {
		httpx.ServeNotFound(w)
		return
	}
	token := httpx.CookieValue(r, "AV_AUTH")
	store.AccountsMu.Lock()
	acc := store.Accounts[token]
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(9000))
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	p := pendingDelete{key: randomHex(8), code: 1000 + int(n.Int64()), expires: time.Now().Add(deleteCodeTTL)}
	deleteCodesMu.Lock()
	deleteCodes[token] = p
	deleteCodesMu.Unlock()
	body, _ := json.Marshal(map[string]any{"result": map[string]any{"key": p.key, "code": p.code}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func handleAvatarDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !httpx.IsAllDigits(r.PathValue("id")) {
		httpx.ServeNotFound(w)
		return
	}
	token := httpx.CookieValue(r, "AV_AUTH")
	store.AccountsMu.Lock()
	acc := store.Accounts[token]
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	deleteCodesMu.Lock()
	p, ok := deleteCodes[token]
	delete(deleteCodes, token) // single use
	deleteCodesMu.Unlock()
	if !ok || time.Now().After(p.expires) || p.key != r.PathValue("key") || strconv.Itoa(p.code) != r.PathValue("code") {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if err := deleteAccount(acc); err != nil {
		log.Printf("cherry: delete account failed: %v", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

type deletedBackup struct {
	Time        string                      `json:"time"`
	Account     store.StoredAccount         `json:"account"`
	Tokens      []string                    `json:"tokens"`
	Posts       []store.DiaryPost           `json:"posts"`
	Guestbook   []store.SavedGuestbookEntry `json:"guestbook"`
	Friendships []store.Friendship          `json:"friendships"`
}

// deleteAccount removes acc, all its token aliases and its social rows. A full backup line is
// appended to deleted-accounts.jsonl first. Friends keep a "removed" friendship so their native
// caches (upsert-only) get a status -1 row on the next sync. The aid is never reused:
// nextAvatarID only grows. A deleted lab aid stays in labAids, which is harmless for that reason.
// Lock order: accountsMu, then socialMu.
func deleteAccount(acc *store.Account) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()

	var tokens []string
	for t, a := range store.Accounts {
		if a == acc {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return fmt.Errorf("account already gone")
	}
	aid := acc.Aid
	var posts []store.DiaryPost
	var book []store.SavedGuestbookEntry
	var rels []store.Friendship
	keepPosts := make([]store.DiaryPost, 0, len(store.DiaryPosts))
	for _, p := range store.DiaryPosts {
		if p.AvatarID == aid {
			posts = append(posts, p)
		} else {
			keepPosts = append(keepPosts, p)
		}
	}
	keepBook := make([]store.SavedGuestbookEntry, 0, len(store.GuestbookEntries))
	for _, e := range store.GuestbookEntries {
		if e.HostAvtNo == aid || e.GuestAvtNo == aid {
			book = append(book, e)
		} else {
			keepBook = append(keepBook, e)
		}
	}
	newFriends := append([]store.Friendship(nil), store.Friendships...)
	for i, f := range newFriends {
		if f.A == aid || f.B == aid {
			rels = append(rels, f)
			newFriends[i].State = "removed"
		}
	}
	if err := appendDeletedBackup(deletedBackup{
		Time: time.Now().UTC().Format(time.RFC3339), Account: store.StoredFrom(acc), Tokens: tokens,
		Posts: posts, Guestbook: book, Friendships: rels,
	}); err != nil {
		return err
	}

	prevAccounts, prevLatest := store.Accounts, store.LatestAcc
	next := make(map[string]*store.Account, len(store.Accounts))
	for t, a := range store.Accounts {
		if a != acc {
			next[t] = a
		}
	}
	store.Accounts = next
	if store.LatestAcc == acc { // the store requires a valid latest while accounts remain
		store.LatestAcc = nil
		var best uint64
		for _, a := range next {
			if n, _ := strconv.ParseUint(a.Aid, 10, 64); store.LatestAcc == nil || n > best {
				store.LatestAcc, best = a, n
			}
		}
	}
	if err := store.SaveAccountsLocked(); err != nil {
		store.Accounts, store.LatestAcc = prevAccounts, prevLatest
		return err
	}
	prevPosts, prevBook, prevFriends := store.DiaryPosts, store.GuestbookEntries, store.Friendships
	store.DiaryPosts, store.GuestbookEntries, store.Friendships = keepPosts, keepBook, newFriends
	if err := store.SaveSocialLocked(); err != nil {
		store.DiaryPosts, store.GuestbookEntries, store.Friendships = prevPosts, prevBook, prevFriends
		store.Accounts, store.LatestAcc = prevAccounts, prevLatest
		if rerr := store.SaveAccountsLocked(); rerr != nil {
			log.Printf("cherry: delete account rollback save failed: %v", rerr)
		}
		return err
	}
	store.FlushLedgerLocked([]store.LedgerLine{{Time: time.Now().UTC().Format(time.RFC3339), Aid: aid, Reason: "account deleted"}})
	return nil
}

// appendDeletedBackup writes one JSON line next to accounts.json; caller holds accountsMu.
func appendDeletedBackup(b deletedBackup) error {
	if store.AccountStorePath == "" {
		return nil // in-memory test sessions
	}
	line, err := json.Marshal(b)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(store.AccountStorePath), "deleted-accounts.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
