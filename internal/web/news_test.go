package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

type newsListEnvelope struct {
	Type            string `json:"type"`
	NotiSn          string `json:"notiSn"`
	RegDate         string `json:"regDate"`
	RefID           string `json:"refId"`
	HeartReceivable bool   `json:"heartReceivable"`
	Data            string `json:"data"`
}

type newsListResp struct {
	Result struct {
		NewsList      []newsListEnvelope `json:"newsList"`
		AlertList     []newsListEnvelope `json:"alertList"`
		NewAlertCount int                `json:"newAlertCount"`
	} `json:"result"`
}

func fetchNewsList(t *testing.T, token, deviceType string) newsListResp {
	t.Helper()
	rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/noti/lists?timestamp=0&deviceType="+deviceType, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v4/noti/lists?deviceType=%s = %d %s", deviceType, rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode noti/lists raw: %v", err)
	}
	resObj, _ := raw["result"].(map[string]any)
	if resObj["newsList"] == nil || resObj["alertList"] == nil {
		t.Fatalf("newsList and alertList must be non-nil JSON arrays: %s", rec.Body.String())
	}
	var out newsListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode noti/lists: %v", err)
	}
	return out
}

func parseEnvelopeData(t *testing.T, env newsListEnvelope) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(env.Data), &m); err != nil {
		t.Fatalf("env %s data must be a JSON-encoded string: %q (%v)", env.Type, env.Data, err)
	}
	return m
}

func fetchBadges(t *testing.T, token string) map[string]int {
	t.Helper()
	rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/badge/infos/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v4/badge/infos/ = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Result map[string]int `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode badge/infos: %v", err)
	}
	return out.Result
}

func installNewsOperatorConfig(t *testing.T, doc string) {
	t.Helper()
	dir := webOperatorDir(t)
	path := filepath.Join(dir, "news.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := economy.LoadOperatorData(); err != nil {
		t.Fatalf("LoadOperatorData: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(path, data.NewsJSON, 0o600)
		_ = economy.LoadOperatorData()
	})
}

func TestNewsListAuthEmptyAndDeviceTypes(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"n-tok": {AccessToken: "n-tok", SessionKey: "sk", Aid: "9101", Name: "Alice"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	setClock(t, day0)

	if unauth := serve(t, http.MethodGet, "/v4/noti/lists?timestamp=0&deviceType=Android"); unauth.Code != http.StatusNotFound {
		t.Fatalf("unauth noti/lists = %d, want 404", unauth.Code)
	}
	for _, dev := range []string{"Android", "iPhone", "iPad"} {
		got := fetchNewsList(t, "n-tok", dev)
		if len(got.Result.NewsList) != 0 || len(got.Result.AlertList) != 0 || got.Result.NewAlertCount != 0 {
			t.Fatalf("empty %s = %+v", dev, got.Result)
		}
	}
}

func TestNewsAllAlertTypesDiaryFriendsBadgesAndPersistence(t *testing.T) {
	testutil.UseDatabase(t)
	root, _ := thumbFixture(t)
	writeThumbItem(t, root, "dress", "225000001", "dp.png", []byte("x"))
	recordPicture(t, store.MediaDiary, "9102", "9102", "1760000100", "9102_1760000100")

	clock := setClock(t, day0)
	installSocialTestAccounts(t, map[string]*store.Account{
		"tok1": {AccessToken: "tok1", SessionKey: "sk1", Aid: "9101", Name: "Alice", Gender: "FEMALE", Gems: 50000,
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9101_1", Bg: "profilebg/5.jpg"}},
		"tok2": {AccessToken: "tok2", SessionKey: "sk2", Aid: "9102", Name: "Bob", Gender: "MALE", Gems: 50000,
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9102_1", Bg: "profilebg/12.jpg"}},
		"tok3": {AccessToken: "tok3", SessionKey: "sk3", Aid: "9103", Name: "Cara", Gender: "FEMALE", Gems: 50000},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.ResetCircles()
	t.Cleanup(store.ResetCircles)

	// 1. Bob sends a friend request to Alice -> FRIEND_RECEIVE on Alice.
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9101"}`); rec.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body.String())
	}
	list := fetchNewsList(t, "tok1", "Android")
	if len(list.Result.AlertList) != 1 || list.Result.AlertList[0].Type != "FRIEND_RECEIVE" {
		t.Fatalf("expected FRIEND_RECEIVE, got %+v", list.Result.AlertList)
	}
	frSn := list.Result.AlertList[0].NotiSn

	// Alice accepts Bob via News 2-segment route -> FRIEND_ACCEPT on Bob; FRIEND_RECEIVE disappears from Alice.
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok1", http.MethodPut, "/v4/friend/accept/9102/"+frSn, "null"); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("news friend accept = %d %s", rec.Code, rec.Body.String())
	}
	listAliceAfterAccept := fetchNewsList(t, "tok1", "Android")
	for _, a := range listAliceAfterAccept.Result.AlertList {
		if a.Type == "FRIEND_RECEIVE" {
			t.Fatalf("stale FRIEND_RECEIVE still present after accept: %+v", a)
		}
	}
	listBob := fetchNewsList(t, "tok2", "iPhone")
	if len(listBob.Result.AlertList) != 1 || listBob.Result.AlertList[0].Type != "FRIEND_ACCEPT" {
		t.Fatalf("expected FRIEND_ACCEPT on Bob, got %+v", listBob.Result.AlertList)
	}

	// 2. Heart send Bob -> Alice -> HEART_RECEIVE with heartReceivable: true.
	*clock = clock.Add(time.Second)
	if sent, _ := economy.HeartSend("9102", "9101", *clock); !sent {
		t.Fatal("HeartSend failed")
	}

	// 3. Diary posts:
	//    - Bob (friend) posts text diary
	//    - Bob (friend) posts photo diary
	//    - Cara (stranger) posts public photo diary (should appear in Recommended, NOT as DIARY_WRITE_SAVE)
	//    - Bob posts private/blind post (should NOT appear)
	*clock = clock.Add(time.Second)
	writePost := func(tok, body string) string {
		rec := socialTestRequestAs(t, tok, http.MethodPost, "/v4/diary2/ext/write", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("diary write = %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Result store.DiaryPost `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Result.DiaryNo
	}
	bobTextNo := writePost("tok2", `{"avatarId":"9102","title":"t","content":"Bob text post","openState":"public"}`)
	bobPhotoURL := "/lineplay/d/download.nhn?oid=9102_1760000100&ctime=1760000100&userid=9102"
	bobPhotoNo := writePost("tok2", fmt.Sprintf(`{"avatarId":"9102","title":"p","content":"Bob photo caption","openState":"public","imageTypeKind":"photo","images":[{"imageUrl":%q}]}`, bobPhotoURL))
	bobPrivNo := writePost("tok2", `{"avatarId":"9102","title":"s","content":"secret","openState":"private"}`)
	recordPicture(t, store.MediaDiary, "9103", "9103", "1760000101", "9103_1760000101")
	caraPhotoURL := "/lineplay/d/download.nhn?oid=9103_1760000101&ctime=1760000101&userid=9103"
	caraPhotoNo := writePost("tok3", fmt.Sprintf(`{"avatarId":"9103","title":"cp","content":"Cara recommended photo","openState":"public","imageTypeKind":"photo","images":[{"imageUrl":%q}]}`, caraPhotoURL))
	// Hot posts need activity: Bob comments on Cara's photo (Bob's own photo has none, so it is not a hot post).
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/diary2/comment/write/9103", fmt.Sprintf(`{"diaryNo":%s,"avatarID":"9102","content":"Great shot"}`, caraPhotoNo)); rec.Code != http.StatusOK {
		t.Fatalf("comment on Cara's photo = %d %s", rec.Code, rec.Body.String())
	}

	// Bob comments and likes Alice's diary -> DIARY_COMMENT_SAVE and DIARY_STICKER_SAVE on Alice.
	aliceDiaryNo := writePost("tok1", `{"avatarId":"9101","title":"a","content":"Alice diary","openState":"public"}`)
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/diary2/comment/write/9101", fmt.Sprintf(`{"diaryNo":%s,"avatarID":"9102","content":"Nice diary!"}`, aliceDiaryNo)); rec.Code != http.StatusOK {
		t.Fatalf("diary comment = %d %s", rec.Code, rec.Body.String())
	}
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/diary2/like/save/9101", fmt.Sprintf(`{"diaryNo":%s,"likeAvatarId":"9102","likeType":2}`, aliceDiaryNo)); rec.Code != http.StatusOK {
		t.Fatalf("diary like = %d %s", rec.Code, rec.Body.String())
	}
	// Unlike (likeType: 0) must NOT add another DIARY_STICKER_SAVE.
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/diary2/like/save/9101", fmt.Sprintf(`{"diaryNo":%s,"likeAvatarId":"9102","likeType":0}`, aliceDiaryNo)); rec.Code != http.StatusOK {
		t.Fatalf("diary unlike = %d %s", rec.Code, rec.Body.String())
	}

	// 4. Guestbook write Bob -> Alice (GUESTBOOK_WRITE_SAVE) and reply Alice -> Bob (GUESTBOOK_COMMENT_SAVE on Bob).
	*clock = clock.Add(time.Second)
	gbRec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/guestbook/write", `{"hostAvtNo":"9101","guestAvtNo":"9102","content":"Hello Alice!","secret":false}`)
	if gbRec.Code != http.StatusOK {
		t.Fatalf("gb write = %d %s", gbRec.Code, gbRec.Body.String())
	}
	var gbOut struct {
		Result struct {
			GuestHistNo string `json:"guestHistNo"`
		} `json:"result"`
	}
	_ = json.Unmarshal(gbRec.Body.Bytes(), &gbOut)
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/guestbookreply/write", fmt.Sprintf(`{"hostAvtNo":"9101","replyAvtNo":"9101","parentHistNo":%q,"content":"Thanks Bob!"}`, gbOut.Result.GuestHistNo)); rec.Code != http.StatusOK {
		t.Fatalf("gb reply = %d %s", rec.Code, rec.Body.String())
	}

	// 5. Gift send Bob -> Alice -> POSTBOX_RECEIVE on Alice (with live postExprDate and item imageUrl),
	//    then Alice claims gift -> POSTBOX_OPEN on Bob & postExprDate becomes "0" on Alice.
	*clock = clock.Add(time.Second)
	giftListRec := socialTestRequestAs(t, "tok2", http.MethodGet, "/v4/shop/gift/list/DRESS/ALL", "")
	if giftListRec.Code != http.StatusOK {
		t.Fatalf("gift list = %d %s", giftListRec.Code, giftListRec.Body.String())
	}
	var giftListOut struct {
		Result struct {
			Items []struct {
				ItemCode        string `json:"itemCode"`
				Price           int64  `json:"price"`
				DiscountedPrice int64  `json:"discountedPrice"`
				Discount        bool   `json:"discount"`
			} `json:"items"`
		} `json:"result"`
	}
	_ = json.Unmarshal(giftListRec.Body.Bytes(), &giftListOut)
	var price int64
	foundGiftItem := false
	for _, it := range giftListOut.Result.Items {
		if it.ItemCode == "CUTO00001" {
			price = it.Price
			if it.Discount {
				price = it.DiscountedPrice
			}
			foundGiftItem = true
			break
		}
	}
	if !foundGiftItem {
		t.Fatalf("CUTO00001 missing from gift list: %s", giftListRec.Body.String())
	}
	giftBody := fmt.Sprintf(`{"language":"en","deviceType":"Android","receiveUser":{"aid":"9101"},"coinUnit":"GEM","giftMessage":"Enjoy!","itemType":"DRESS","items":[{"itemCode":"CUTO00001","price":%d}]}`, price)
	giftReq := httptest.NewRequest(http.MethodPost, "/v4/gift/send", strings.NewReader(giftBody))
	giftReq.Header.Set("Cookie", `AV_AUTH="tok2"`)
	giftReq.Host = "fapi.example.test"
	giftRec := serveRequest(t, giftReq)
	if giftRec.Code != http.StatusOK || !strings.Contains(giftRec.Body.String(), `"balanceUnit":"GEM"`) {
		t.Fatalf("gift send = %d %s", giftRec.Code, giftRec.Body.String())
	}
	// Find postSeq on Alice before claim.
	preClaimList := fetchNewsList(t, "tok1", "Android")
	var postSeq, preExpr string
	for _, a := range preClaimList.Result.AlertList {
		if a.Type == "POSTBOX_RECEIVE" {
			postSeq = a.RefID
			d := parseEnvelopeData(t, a)
			preExpr, _ = d["postExprDate"].(string)
			imgURL, _ := d["imageUrl"].(string)
			if d["postType"] != "item" || d["postMsg"] != "Enjoy!" || !strings.HasPrefix(imgURL, "https://") || !strings.Contains(imgURL, "/arts_item_dress_225000001/dp.png") {
				t.Fatalf("POSTBOX_RECEIVE data = %+v", d)
			}
		}
	}
	if postSeq == "" || preExpr == "" || preExpr == "0" {
		t.Fatalf("missing POSTBOX_RECEIVE or live postExprDate: seq=%q expr=%q", postSeq, preExpr)
	}
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/postbox/receive/"+postSeq, ""); rec.Code != http.StatusOK {
		t.Fatalf("postbox receive = %d %s", rec.Code, rec.Body.String())
	}

	// 6. Circle alerts (must be ONLY in alertList and NEVER in newsList):
	//    Alice creates an OPEN circle; Bob applies -> CIRCLE_APPLY_JOIN on Alice;
	//    Alice approves -> CIRCLE_MEMBER_JOIN on Bob;
	//    Alice transfers master -> CIRCLE_CHANGE_MASTER on Bob;
	//    Bob invites Cara -> CIRCLE_INVITE_AVATAR on Cara;
	//    Cara accepts -> CIRCLE_ACCEPT_INVITATION on Bob.
	*clock = clock.Add(time.Second)
	d := economy.ActiveCircleData()
	createBody := fmt.Sprintf(`{"coinUnit":0,"deviceType":"Android","productCode":%q,"price":%d,"circleName":"NewsClub","circleIntroduce":"hi","logoId":%d,"logoBgId":%d,"joinType":"OPEN","interestCodeList":[101,301]}`,
		d.Product.Code, d.Product.PriceGems, d.Logos[0].ID, d.Backgrounds[0].ID)
	crec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/circle/create", createBody)
	if crec.Code != http.StatusOK {
		t.Fatalf("circle create = %d %s", crec.Code, crec.Body.String())
	}
	var cOut struct {
		Result struct {
			CircleID string `json:"circleId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(crec.Body.Bytes(), &cOut)
	cidStr := cOut.Result.CircleID
	cid, err := strconv.ParseInt(cidStr, 10, 64)
	if err != nil {
		t.Fatalf("circleId %q: %v", cidStr, err)
	}
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/circle/apply/join/"+cidStr, `{"cancleAndApplyYn":false}`); rec.Code != http.StatusOK {
		t.Fatalf("circle apply = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/circle/apply/join/approval/"+cidStr+"/9102", ""); rec.Code != http.StatusOK {
		t.Fatalf("circle approve = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := economy.CircleTransferMaster("9101", cid, "9102"); err != nil {
		t.Fatalf("CircleTransferMaster: %v", err)
	}
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9103"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "tok3", http.MethodPut, "/v4/friend/accept/9102", "null"); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "tok2", http.MethodPost, "/v4/circle/invite/"+cidStr+"/9103", ""); rec.Code != http.StatusOK {
		t.Fatalf("circle invite = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "tok3", http.MethodPost, "/v4/circle/invite/approval/"+cidStr, ""); rec.Code != http.StatusOK {
		t.Fatalf("circle accept invite = %d %s", rec.Code, rec.Body.String())
	}

	// 7. Also verify ROOM_INVITE_RECEIVE & SQUARE_INVITE_RECEIVE serialization on Alice.
	*clock = clock.Add(time.Second)
	store.AddNewsAlert("9101", store.NewsAlert{Type: "ROOM_INVITE_RECEIVE", At: clock.UnixMilli(), Actor: "9102", ActorName: "Bob"})
	*clock = clock.Add(time.Second)
	store.AddNewsAlert("9101", store.NewsAlert{Type: "SQUARE_INVITE_RECEIVE", At: clock.UnixMilli(), Actor: "9102", ActorName: "Bob"})

	// Verify Alice's full newsList & alertList!
	aliceList := fetchNewsList(t, "tok1", "Android")
	seenAlerts := map[string]bool{}
	for _, a := range aliceList.Result.AlertList {
		seenAlerts[a.Type] = true
		dataMap := parseEnvelopeData(t, a)
		switch a.Type {
		case "HEART_RECEIVE":
			if !a.HeartReceivable {
				t.Fatalf("HEART_RECEIVE heartReceivable = false, want true")
			}
			if dataMap["avatarProfile"] != "lineplay/pr/9102_1" || dataMap["obsProfileImagePath"] != "/lineplay/pr/9102_1" || dataMap["avatarBg"] != "profilebg/12.jpg" {
				t.Fatalf("HEART_RECEIVE actor profile = %+v", dataMap)
			}
		case "POSTBOX_RECEIVE":
			if dataMap["postExprDate"] != "0" {
				t.Fatalf("claimed POSTBOX_RECEIVE postExprDate = %v, want \"0\"", dataMap["postExprDate"])
			}
		case "CIRCLE_APPLY_JOIN":
			if dataMap["circleId"] != cidStr || dataMap["circleName"] != "NewsClub" ||
				dataMap["circleLogoImgPath"] == "" || dataMap["circleLogoBgImgPath"] == "" {
				t.Fatalf("CIRCLE_APPLY_JOIN fields = %+v", dataMap)
			}
		}
	}
	for _, want := range []string{
		"HEART_RECEIVE", "DIARY_COMMENT_SAVE", "DIARY_STICKER_SAVE",
		"GUESTBOOK_WRITE_SAVE", "POSTBOX_RECEIVE", "CIRCLE_APPLY_JOIN",
		"ROOM_INVITE_RECEIVE", "SQUARE_INVITE_RECEIVE",
	} {
		if !seenAlerts[want] {
			t.Errorf("Alice alertList missing %s (got %v)", want, seenAlerts)
		}
	}
	// Check Alice's newsList: NO CIRCLE_*, has DIARY_WRITE_SAVE (Bob's text & photo, not private),
	// has Recommended post from Cara (`goSomewhere(diaryDetail)`), and non-Circle alerts.
	seenNews := map[string]bool{}
	seenDiaryIDs := map[string]bool{}
	var recFound bool
	for _, n := range aliceList.Result.NewsList {
		if strings.HasPrefix(n.Type, "CIRCLE_") {
			t.Fatalf("CIRCLE_* must never appear in newsList: %+v", n)
		}
		seenNews[n.Type] = true
		dMap := parseEnvelopeData(t, n)
		if n.Type == "DIARY_WRITE_SAVE" {
			seenDiaryIDs[n.RefID] = true
			if n.RefID == bobPhotoNo {
				if dMap["avatarProfile"] != "lineplay/pr/9102_1" || dMap["obsProfileImagePath"] != "/lineplay/pr/9102_1" {
					t.Fatalf("Bob photo DIARY_WRITE_SAVE actor profile = %+v", dMap)
				}
				if dMap["imageLocationType"] != "obs" || dMap["imageTypeKind"] != "obs" || dMap["imageUrl"] != bobPhotoURL {
					t.Fatalf("Bob photo DIARY_WRITE_SAVE = %+v", dMap)
				}
			}
			if n.RefID == bobTextNo {
				if dMap["imageLocationType"] != "none" || dMap["imageTypeKind"] != "none" || dMap["message"] != "Bob text post" {
					t.Fatalf("Bob text DIARY_WRITE_SAVE = %+v", dMap)
				}
			}
			if n.RefID == bobPrivNo || n.RefID == caraPhotoNo {
				t.Fatalf("unexpected post in friend DIARY_WRITE_SAVE: %s", n.RefID)
			}
		}
		if n.Type == "BANNERS" && dMap["linkType"] == "goSomewhere(diaryDetail)" && n.RefID == caraPhotoNo {
			recFound = true
			if dMap["link"] != "9103" || dMap["categoryTypeNm"] != "Cara" || dMap["image"] != caraPhotoURL || int(dMap["bannerId"].(float64)) != 0 {
				t.Fatalf("recommended post mismatch: env=%+v data=%+v", n, dMap)
			}
		}
	}
	if !seenDiaryIDs[bobTextNo] || !seenDiaryIDs[bobPhotoNo] || !recFound {
		t.Fatalf("missing diary or recommended entries: seenDiaryIDs=%v recFound=%v", seenDiaryIDs, recFound)
	}

	// Verify Bob received FRIEND_ACCEPT, POSTBOX_OPEN, GUESTBOOK_COMMENT_SAVE, CIRCLE_MEMBER_JOIN, CIRCLE_CHANGE_MASTER, CIRCLE_ACCEPT_INVITATION.
	bobList := fetchNewsList(t, "tok2", "Android")
	bobTypes := map[string]bool{}
	for _, a := range bobList.Result.AlertList {
		bobTypes[a.Type] = true
	}
	for _, want := range []string{"FRIEND_ACCEPT", "POSTBOX_OPEN", "GUESTBOOK_COMMENT_SAVE", "CIRCLE_MEMBER_JOIN", "CIRCLE_CHANGE_MASTER", "CIRCLE_ACCEPT_INVITATION"} {
		if !bobTypes[want] {
			t.Errorf("Bob alertList missing %s (got %v)", want, bobTypes)
		}
	}
	// Verify Cara received CIRCLE_INVITE_AVATAR.
	caraList := fetchNewsList(t, "tok3", "Android")
	if len(caraList.Result.AlertList) == 0 || caraList.Result.AlertList[0].Type != "CIRCLE_INVITE_AVATAR" {
		t.Fatalf("Cara alertList = %+v, want CIRCLE_INVITE_AVATAR", caraList.Result.AlertList)
	}

	// 8. Badge counts and independent NEWS vs ALERT resets (even while fake clock is day0 < time.Now())!
	badgesBefore := fetchBadges(t, "tok1")
	if badgesBefore["NEWS"] == 0 || badgesBefore["ALERT"] == 0 || aliceList.Result.NewAlertCount != badgesBefore["ALERT"] {
		t.Fatalf("badgesBefore=%+v newAlertCount=%d", badgesBefore, aliceList.Result.NewAlertCount)
	}
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/r/badge/reset/NEWS", "null"); rec.Code != http.StatusOK {
		t.Fatalf("reset NEWS = %d", rec.Code)
	}
	badgesAfterNews := fetchBadges(t, "tok1")
	if badgesAfterNews["NEWS"] != 0 || badgesAfterNews["ALERT"] != badgesBefore["ALERT"] {
		t.Fatalf("after NEWS reset: %+v (want NEWS=0, ALERT=%d)", badgesAfterNews, badgesBefore["ALERT"])
	}
	*clock = clock.Add(time.Second)
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/r/badge/reset/ALERT", "null"); rec.Code != http.StatusOK {
		t.Fatalf("reset ALERT = %d", rec.Code)
	}
	badgesAfterAlert := fetchBadges(t, "tok1")
	if badgesAfterAlert["NEWS"] != 0 || badgesAfterAlert["ALERT"] != 0 {
		t.Fatalf("after ALERT reset: %+v", badgesAfterAlert)
	}
	if afterResetList := fetchNewsList(t, "tok1", "Android"); afterResetList.Result.NewAlertCount != 0 {
		t.Fatalf("newAlertCount after reset = %d, want 0", afterResetList.Result.NewAlertCount)
	}

	// 9. SQLite persistence round-trip: reload stores from DB and verify News state survived!
	if err := store.ReloadStores(); err != nil {
		t.Fatalf("ReloadStores: %v", err)
	}
	reloadedList := fetchNewsList(t, "tok1", "Android")
	if len(reloadedList.Result.AlertList) != len(aliceList.Result.AlertList) || reloadedList.Result.NewAlertCount != 0 {
		t.Fatalf("reloaded AlertList len=%d (want %d) newAlertCount=%d", len(reloadedList.Result.AlertList), len(aliceList.Result.AlertList), reloadedList.Result.NewAlertCount)
	}
}

func TestNewsOperatorBannersReadViewAndImageRoutes(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"b-tok": {AccessToken: "b-tok", SessionKey: "sk", Aid: "9201", Name: "Viewer"},
	})
	setClock(t, day0)
	doc := `{
		"origin": "cherry",
		"note": "test banners",
		"include_season_banners": false,
		"include_recommended_posts": false,
		"recommended_max": 3,
		"banners": [
			{
				"id": 1970000010,
				"category": "Announcement",
				"title": "Discord Server Open",
				"link": "openWithBrowser",
				"url": "https://discord.gg/example",
				"image": "",
				"background": "#1c3a5e",
				"icon": "",
				"ending_soon": true,
				"start": "2026-10-01",
				"end": "2026-10-15"
			}
		]
	}`
	installNewsOperatorConfig(t, doc)

	list := fetchNewsList(t, "b-tok", "Android")
	if len(list.Result.NewsList) != 1 || list.Result.NewsList[0].Type != "BANNERS" {
		t.Fatalf("newsList = %+v", list.Result.NewsList)
	}
	bd := parseEnvelopeData(t, list.Result.NewsList[0])
	if bd["flag"] != "NEW" || bd["showEndingNotiYn"] != "Y" || bd["linkType"] != "openWithBrowser" || bd["link"] != "https://discord.gg/example" {
		t.Fatalf("banner data = %+v", bd)
	}
	if _, ok := bd["eventPeriodStartYmdt"].(float64); !ok {
		t.Fatalf("eventPeriodStartYmdt must be JSON number, got %T", bd["eventPeriodStartYmdt"])
	}
	if _, ok := bd["eventPeriodEndYmdt"].(float64); !ok {
		t.Fatalf("eventPeriodEndYmdt must be JSON number, got %T", bd["eventPeriodEndYmdt"])
	}

	imgPath, _ := bd["image"].(string)
	if !strings.HasPrefix(imgPath, "cherry/news/1970000010-") {
		t.Fatalf("image path = %q", imgPath)
	}
	// Image works on both HTTPS mux and plain port (/img/read/cherry/news/...).
	httpsRec := serve(t, http.MethodGet, "/"+imgPath)
	if httpsRec.Code != http.StatusOK || httpsRec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("HTTPS banner image = %d", httpsRec.Code)
	}
	plainRec := httptest.NewRecorder()
	NewPlainMux().ServeHTTP(plainRec, httptest.NewRequest(http.MethodGet, "/img/read/"+imgPath, nil))
	if plainRec.Code != http.StatusOK || plainRec.Body.Len() != httpsRec.Body.Len() {
		t.Fatalf("plain banner image = %d len=%d, want %d len=%d", plainRec.Code, plainRec.Body.Len(), http.StatusOK, httpsRec.Body.Len())
	}

	// Mark banner viewed -> flag clears and persists.
	if rec := socialTestRequestAs(t, "b-tok", http.MethodGet, "/v4/noti/banner/view/1970000010", ""); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("banner view = %d %s", rec.Code, rec.Body.String())
	}
	afterView := fetchNewsList(t, "b-tok", "Android")
	if parseEnvelopeData(t, afterView.Result.NewsList[0])["flag"] != "" {
		t.Fatalf("expected flag cleared after view: %s", afterView.Result.NewsList[0].Data)
	}
}

func TestNewsFriendAcceptAndCheckExistAndHaveHeart(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"t1": {AccessToken: "t1", SessionKey: "sk1", Aid: "9301", Name: "One"},
		"t2": {AccessToken: "t2", SessionKey: "sk2", Aid: "9302", Name: "Two"},
		"t3": {AccessToken: "t3", SessionKey: "sk3", Aid: "9303", Name: "Three"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	setClock(t, day0)

	// News 2-segment accept error cases:
	// 1. Unknown/deleted target -> 19509
	if rec := socialTestRequestAs(t, "t1", http.MethodPut, "/v4/friend/accept/99999/1", "null"); rec.Code != 200 || rec.Body.String() != `{"result":false,"errorCode":"19509"}` {
		t.Fatalf("deleted target = %d %s", rec.Code, rec.Body.String())
	}
	// 2. No pending request -> 19406
	if rec := socialTestRequestAs(t, "t1", http.MethodPut, "/v4/friend/accept/9302/1", "null"); rec.Code != 200 || rec.Body.String() != `{"result":false,"errorCode":"19406"}` {
		t.Fatalf("missing request = %d %s", rec.Code, rec.Body.String())
	}
	// 3. Reverse pending (9301 sent to 9302, then 9301 tries to accept 9302) -> 19508
	if rec := socialTestRequestAs(t, "t1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9302"}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "t1", http.MethodPut, "/v4/friend/accept/9302/1", "null"); rec.Code != 200 || rec.Body.String() != `{"result":false,"errorCode":"19508"}` {
		t.Fatalf("reverse pending = %d %s", rec.Code, rec.Body.String())
	}
	// 4. Success -> {"result":true}, then already friends -> 19505
	if rec := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/9301/1", "null"); rec.Code != 200 || rec.Body.String() != `{"result":true}` {
		t.Fatalf("news accept = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "t2", http.MethodPut, "/v4/friend/accept/9301/1", "null"); rec.Code != 200 || rec.Body.String() != `{"result":false,"errorCode":"19505"}` {
		t.Fatalf("already friends = %d %s", rec.Code, rec.Body.String())
	}
	// 5. Normal 1-segment accept still returns {"result":1}.
	if rec := socialTestRequestAs(t, "t3", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"9301"}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "t1", http.MethodPut, "/v4/friend/accept/9303", "null"); rec.Code != 200 || rec.Body.String() != `{"result":1}` {
		t.Fatalf("1-segment accept = %d %s", rec.Code, rec.Body.String())
	}

	// Diary checkExist 3-segment behavior:
	pubRec := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/diary2/ext/write", `{"avatarId":"9302","title":"t","content":"pub","openState":"public"}`)
	privRec := socialTestRequestAs(t, "t2", http.MethodPost, "/v4/diary2/ext/write", `{"avatarId":"9302","title":"t","content":"priv","openState":"private"}`)
	var pubOut, privOut struct {
		Result store.DiaryPost `json:"result"`
	}
	_ = json.Unmarshal(pubRec.Body.Bytes(), &pubOut)
	_ = json.Unmarshal(privRec.Body.Bytes(), &privOut)

	if rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/diary2/ext/checkExist/9302/"+pubOut.Result.DiaryNo+"/123", ""); rec.Code != 200 || rec.Body.String() != `{"result":true}` {
		t.Fatalf("existing post checkExist = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/diary2/ext/checkExist/9302/999999/123", ""); rec.Code != 400 || rec.Body.String() != `{"errorCode":"200003"}` {
		t.Fatalf("deleted post checkExist = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/diary2/ext/checkExist/99999/1/123", ""); rec.Code != 400 || rec.Body.String() != `{"errorCode":"10001"}` {
		t.Fatalf("deleted avatar checkExist = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/diary2/ext/checkExist/9302/"+privOut.Result.DiaryNo+"/123", ""); rec.Code != 400 || rec.Body.String() != `{"errorCode":"200002"}` {
		t.Fatalf("private post checkExist = %d %s", rec.Code, rec.Body.String())
	}

	// GET /v4/heart/haveHeart (no trailing aid):
	if rec := socialTestRequestAs(t, "t1", http.MethodGet, "/v4/heart/haveHeart", ""); rec.Code != 200 || rec.Body.String() != `{"result":true}` {
		t.Fatalf("haveHeart = %d %s", rec.Code, rec.Body.String())
	}

	// Verify gem / cash / faceTickets POSTBOX_RECEIVE alerts map to "gem", "cash", "gift".
	adminMail(t, "127.0.0.1:9", `{"aid":"9301","type":"gems","amount":50,"message":"g"}`)
	adminMail(t, "127.0.0.1:9", `{"aid":"9301","type":"cash","amount":10,"message":"c"}`)
	adminMail(t, "127.0.0.1:9", `{"aid":"9301","type":"faceTickets","amount":2,"message":"t"}`)
	mailList := fetchNewsList(t, "t1", "Android")
	gotPostTypes := map[string]bool{}
	for _, a := range mailList.Result.AlertList {
		if a.Type == "POSTBOX_RECEIVE" {
			d := parseEnvelopeData(t, a)
			pt, _ := d["postType"].(string)
			gotPostTypes[pt] = true
		}
	}
	for _, want := range []string{"gem", "cash", "gift"} {
		if !gotPostTypes[want] {
			t.Errorf("POSTBOX_RECEIVE missing postType %q: got %v", want, gotPostTypes)
		}
	}
}
