package economy

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Circle HTTP (https://fapi.play.naver.jp/v4/circle/..., spec section 4). Every reply's shape is the one the native
// parser reads (the parser address is in each view's comment). Replies are {"result": ...} with HTTP 200; a refusal is a
// non-2xx status with {"errorCode": "<code>"}, which the client maps to its text (economy/circle.go circleErr*).
//
// Routes are matched by their shape: numeric path segments become "#", so "POST apply/join/#/approval/#" is one case.

// RegisterCircleRoutes serves /v4/circle/... and the two logo pictures under /cherry/circle/. The MENU entry's game
// info (playhome/games/lp_cir) is registered with the other game infos in web.
func RegisterCircleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/circle/", handleCircle)
	mux.HandleFunc("/cherry/circle/", HandleCircleArt)
}

// circleReply is the envelope every Circle reply uses.
type circleReply struct {
	Result any `json:"result"`
}

// looseInt is an integer the client may send as a number or as a numeric string.
type looseInt int64

func (l *looseInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		*l = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*l = looseInt(n)
	return nil
}

// circleSearchRequest is the body of POST circle/search (RequestCircleSearch 0x24bb5e0): circleName or circleCode by
// mode, searchType, page, size. circle/search/interest sends interestCodeList instead of the names.
type circleSearchRequest struct {
	CircleName       string     `json:"circleName"`
	CircleCode       string     `json:"circleCode"`
	SearchType       string     `json:"searchType"`
	InterestCodeList []looseInt `json:"interestCodeList"`
	Page             looseInt   `json:"page"`
	Size             looseInt   `json:"size"`
}

// circleInfoJSON is one circle as the client reads it: ParserData of stCircleInfo (0x1a9cd30 [S]). The ids are strings
// (asString); logoId, logoBgId, memberCount and maxMemberCount are ints (asInt). The logo keys are logoImgPath,
// logoDpImgPath and logoBgImgPath (the list keys logoImagePath and logoDpImagePath are another parser's, see
// circleLogoJSON). Paths are relative: the client prefixes its static base (play-static), where HandleCircleArt serves them.
type circleInfoJSON struct {
	CircleID        string `json:"circleId"`
	CircleCode      string `json:"circleCode"`
	CircleName      string `json:"circleName"`
	LogoID          int64  `json:"logoId"`
	LogoImgPath     string `json:"logoImgPath"`
	LogoDpImgPath   string `json:"logoDpImgPath"`
	LogoBgID        int64  `json:"logoBgId"`
	LogoBgImgPath   string `json:"logoBgImgPath"`
	CircleIntroduce string `json:"circleIntroduce"`
	MemberCount     int    `json:"memberCount"`
	MaxMemberCount  int    `json:"maxMemberCount"`
	RegistDate      string `json:"registDate"`
	MasterAid       string `json:"masterAid"`
	CreateDate      string `json:"createDate"`
	ModifyDate      string `json:"modifyDate"`
	JoinType        string `json:"joinType"`
}

// circleAvatarJSON is one avatar in a circle: ParserData of stCircleAvatarInfo (0x1a9d2b0 [S]). Every field is a string.
// memberLevel is the role: "MASTER" (leader, 2), "MANAGER" (vice, 1), anything else a plain member (0), per
// SetMemberLevel's comparisons in the same parser.
type circleAvatarJSON struct {
	AvatarID string `json:"avatarId"`
	// stCircleAvatarInfo::ParserData 0x1a9d2b0 [S] takes the row's aid from the last of these keys present, so an empty
	// one zeroes it and the client silently drops the row: they must be left out when empty.
	SenderAid           string `json:"senderAid,omitempty"`
	MemberAid           string `json:"memberAid,omitempty"`
	ApplyAid            string `json:"applyAid,omitempty"`
	ReceiverAid         string `json:"receiverAid,omitempty"`
	AvatarName          string `json:"avatarName"`
	ProfileImagePath    string `json:"profileImagePath"`
	ObsProfileImagePath string `json:"obsProfileImagePath"`
	ProfileBackground   string `json:"profileBackground"`
	BadgeIcon           string `json:"badgeIcon"`
	Level               string `json:"level"`
	MemberLevel         string `json:"memberLevel"`
	RegistDate          string `json:"registDate"`
	CircleJoinedDate    string `json:"circleJoinedDate"`
}

// circleInterestJSON is one entry of circleInterestList; interestNumberCode is read with asInt (parser 0x1b7a5ec).
type circleInterestJSON struct {
	InterestNumberCode int64 `json:"interestNumberCode"`
}

// circleMyInfoJSON is the reply of circle/join/info when the player is in a circle. The client's
// NaCircleDataManager::SetMyCircleInfo (0x2504210 [S]) reads circleMasterExt (the circle, through stCircleInfo::ParserData),
// circleInterestList and circleMemberBySelf.level ("MASTER" leader, "MANAGER" vice, otherwise a member). The circle's
// own keys are repeated at the top level for the same reader's other callers.
type circleMyInfoJSON struct {
	circleInfoJSON
	CircleMasterExt    circleInfoJSON       `json:"circleMasterExt"`
	CircleInterestList []circleInterestJSON `json:"circleInterestList"`
	CircleMemberBySelf circleLevelJSON      `json:"circleMemberBySelf"`
}

type circleLevelJSON struct {
	Level string `json:"level"`
}

// circleInvitedJSON is the reply of circle/join/info when the player is in no circle (ResMyCircleInfo 0x1a793b0 [S]:
// result has invitedCount, an int).
type circleInvitedJSON struct {
	InvitedCount int `json:"invitedCount"`
}

// circleDetailJSON is circle/visit/<id> (ParserData of stCircleInfoDetail 0x1b7a5ec [S]): the circle is read from
// circleMasterExt; circleInterestList holds objects; circleMemberList holds avatar objects; invitedYn is a bool;
// joinedCircleId and appliedJoinCircleId are strings; previousSeasonRank an int.
type circleDetailJSON struct {
	CircleMasterExt     circleInfoJSON       `json:"circleMasterExt"`
	CircleInterestList  []circleInterestJSON `json:"circleInterestList"`
	RoomskinDpImagePath string               `json:"roomskinDpImagePath"`
	InvitedYn           bool                 `json:"invitedYn"`
	JoinedCircleID      string               `json:"joinedCircleId"`
	AppliedJoinCircleID string               `json:"appliedJoinCircleId"`
	PreviousSeasonRank  int                  `json:"previousSeasonRank"`
	CircleMemberList    []circleAvatarJSON   `json:"circleMemberList"`
}

// circleInviteJSON is one entry of an invite list (ParserData of stInviteCircleInfo 0x1c21988 [S]): the circle's keys
// plus the inviter's (senderAid) and the invitee's avatar keys. registDate is the invitation's date and wins over the
// circle's (the outer field), so the two do not collide.
type circleInviteJSON struct {
	circleInfoJSON
	SenderAid   string `json:"senderAid"`
	ReceiverAid string `json:"receiverAid"`
	AvatarID    string `json:"avatarId"`
	AvatarName  string `json:"avatarName"`
	MemberAid   string `json:"memberAid"`
	MemberLevel string `json:"memberLevel"`
	RegistDate  string `json:"registDate"`
}

// circleInviteListJSON is circle/apply/invite/avatar (ResInviteCircleList 0x1c22558 [S] -> stInviteCircleInfo::ParserData):
// applyJoinList and inviteList are arrays, registDate a string.
type circleInviteListJSON struct {
	ApplyJoinList []circleAvatarJSON `json:"applyJoinList"`
	InviteList    []circleInviteJSON `json:"inviteList"`
	RegistDate    string             `json:"registDate"`
}

// circleApplyInviteJSON is circle/apply/invite/<id> (ResCircleInviteAndJoinApplyList 0x1a65ee8 -> stCircleInviteAndJoinApply
// 0x25f5bfc [S]): inviteList and applyJoinList are arrays of avatar objects.
type circleApplyInviteJSON struct {
	InviteList    []circleAvatarJSON `json:"inviteList"`
	ApplyJoinList []circleAvatarJSON `json:"applyJoinList"`
}

// circleSearchJSON is circle/search and circle/search/interest (ResCircleSearch 0x1a641a8 -> SetResponseSearchData
// 0x24e33c8 [S]): items is an array of circle objects; previousCursor, nextCursor and totalCount are strings.
type circleSearchJSON struct {
	Items          []circleInfoJSON `json:"items"`
	PreviousCursor string           `json:"previousCursor"`
	NextCursor     string           `json:"nextCursor"`
	TotalCount     string           `json:"totalCount"`
}

// circleDefaultItemJSON is circle/create/default/item (stCircleDefaultRoomItem::ParserData 0x1a9dc24 [S]): finalPrice is
// the price (+56), paymentType "GEM" selects Gems (any other value selects Cash), cpId must be
// non-empty (CircleCpID; CheckCpid 0x24edd14).
type circleDefaultItemJSON struct {
	ProductCode        string `json:"productCode"`
	ProductName        string `json:"productName"`
	ProductImagePath   string `json:"productImagePath"`
	MaxMemberCount     int    `json:"maxMemberCount"`
	PaymentType        string `json:"paymentType"`
	OriginPrice        int64  `json:"originPrice"`
	DiscountPercentage int    `json:"discountPercentage"`
	DiscountStartDate  string `json:"discountStartDate"`
	DiscountEndDate    string `json:"discountEndDate"`
	FinalPrice         int64  `json:"finalPrice"`
	CpID               string `json:"cpId"`
	Purchasable        bool   `json:"purchasable"`
}

// circleLogoJSON is one logo or background choice (CircleImageInfoParser 0x250bd6c [S]): result is an array, logoId an
// int, logoImagePath and logoDpImagePath strings. The client downloads logoImagePath (ReqDownloadCircleImage 0x24beecc
// from the static base, cached under cache/circle_symbol_image/).
type circleLogoJSON struct {
	LogoID          int64  `json:"logoId"`
	LogoImagePath   string `json:"logoImagePath"`
	LogoDpImagePath string `json:"logoDpImagePath"`
}

// circleCreatedJSON is the reply of circle/create (ResCircleMakeInfo 0x1a840ec [S]): circleMaster is an object with
// circleId (the parser reads result.circleMaster.circleId, not a string).
type circleCreatedJSON struct {
	CircleMaster struct {
		CircleID string `json:"circleId"`
	} `json:"circleMaster"`
	CircleID string `json:"circleId"`
}

// circleRankRowJSON is one circle of a rank reply: the circle's keys plus its points and rank. The client reads both per
// row and inside myCircle (stCircleRankingInfo::ParseData 0x1a9e490 [S]: myCirclePoint asInt64, myCircleRank asString);
// the top level of the reply is never read for them. circleInfoJSON itself (search, recruit, join) stays as it is.
type circleRankRowJSON struct {
	circleInfoJSON
	MyCirclePoint int64  `json:"myCirclePoint"`
	MyCircleRank  string `json:"myCircleRank"`
}

// circleRankJSON is circle/point/rank/total and .../season (ResCircleTotalRank 0x1c19b94 [S]): seasonId an int, myCircle a
// row (or {} outside a circle), topRankCircleList an array of rows in rank order.
type circleRankJSON struct {
	SeasonID          int64               `json:"seasonId"`
	MyCircle          any                 `json:"myCircle"`
	TopRankCircleList []circleRankRowJSON `json:"topRankCircleList"`
}

// circleRankSeasonJSON is circle/point/rank/season: the current season's ranking plus the banner keys that
// stCircleRankSeasonInfo::ParseData 0x1a9e1d4 [S] reads from the top level of this reply only (the total route sends none).
type circleRankSeasonJSON struct {
	circleRankJSON
	SeasonName      string `json:"seasonName"`
	BannerImagePath string `json:"bannerImagePath"`
	GachaTicket     int64  `json:"gachaTicket"`
	SeasonStartDate string `json:"seasonStartDate"`
	GainEndDate     string `json:"gainEndDate"`
	SeasonEndDate   string `json:"seasonEndDate"`
	GachaID         int64  `json:"gachaId"`
	GachaImagePath  string `json:"gachaImagePath"`
	GachaName       string `json:"gachaName"`
}

// circleHallEntryJSON is one entry of entryList (SetHallOfFameInfo 0x2603e14 [S]): seasonId, rank, memberCount,
// maxMemberCount and totalGainPoint are ints; circleId and circleName strings.
type circleHallEntryJSON struct {
	SeasonID       int64  `json:"seasonId"`
	Rank           int64  `json:"rank"`
	CircleID       string `json:"circleId"`
	CircleName     string `json:"circleName"`
	MemberCount    int    `json:"memberCount"`
	MaxMemberCount int    `json:"maxMemberCount"`
	TotalGainPoint int64  `json:"totalGainPoint"`
}

// circleHallJSON is circle/point/rank/entry/top3 (ResCircleHallOfFame 0x1a5c478 -> SetHallOfFameInfo 0x2603e14 [S]).
type circleHallJSON struct {
	SeasonID        int64                 `json:"seasonId"`
	SeasonName      string                `json:"seasonName"`
	BannerImagePath string                `json:"bannerImagePath"`
	SeasonStartDate string                `json:"seasonStartDate"`
	SeasonEndDate   string                `json:"seasonEndDate"`
	GainEndDate     string                `json:"gainEndDate"`
	EntryList       []circleHallEntryJSON `json:"entryList"`
}

// circlePointRewardJSON is one point step as stCirclePointRewardInfo::PaserData reads it (0x250b538 [S], the same keys
// in rewardAchieveList, seasonRewardList and currentReward): ints for seasonId, rewardSeq, rewardAmount and
// achievePoint; rewardType and registered (epoch ms, read as seconds) are strings; circleAchieved and memberReceivedYn
// are bools.
type circlePointRewardJSON struct {
	SeasonID         int64  `json:"seasonId"`
	RewardSeq        int64  `json:"rewardSeq"`
	RewardType       string `json:"rewardType"`
	RewardAmount     int64  `json:"rewardAmount"`
	AchievePoint     int64  `json:"achievePoint"`
	Registered       string `json:"registered"`
	CircleAchieved   bool   `json:"circleAchieved"`
	MemberReceivedYn bool   `json:"memberReceivedYn"`
}

// circleMemberPointJSON is one entry of memberGainPointList (the season popup's member rows): the avatar's keys
// (stCircleAvatarInfo) with seasonId, totalGainPoint (int) and availSeasonAttendYn (bool).
type circleMemberPointJSON struct {
	circleAvatarJSON
	SeasonID            int64 `json:"seasonId"`
	TotalGainPoint      int64 `json:"totalGainPoint"`
	AvailSeasonAttendYn bool  `json:"availSeasonAttendYn"`
}

// circlePointSeasonJSON is circle/point/season/info/<id> (stCirclePointSeasonInfo::PaserData 0x24a5a7c [S]): ints and
// strings as named, bools are asBool. The season popup (entry_core.c, NaCircleManager::MsgProc case 11882) also reads
// seasonRewardList (the steps), currentReward (the next step) and memberGainPointList from the same reply.
type circlePointSeasonJSON struct {
	SeasonID             int64                   `json:"seasonId"`
	SeasonName           string                  `json:"seasonName"`
	GachaTicket          int64                   `json:"gachaTicket"`
	SeasonStartDate      string                  `json:"seasonStartDate"`
	GainEndDate          string                  `json:"gainEndDate"`
	SeasonEndDate        string                  `json:"seasonEndDate"`
	CircleTotalGainPoint string                  `json:"circleTotalGainPoint"`
	RewardReceivable     bool                    `json:"rewardReceivable"`
	AllReceived          bool                    `json:"allReceived"`
	AvailSeasonAttendYn  bool                    `json:"availSeasonAttendYn"`
	SeasonRewardList     []circlePointRewardJSON `json:"seasonRewardList"`
	CurrentReward        circlePointRewardJSON   `json:"currentReward"`
	MemberGainPointList  []circleMemberPointJSON `json:"memberGainPointList"`
}

// circleRewardJSON is circle/point/season/reward/<season>/<seq> (stCirclePointRewardDetailInfo::PaserData 0x250b538 [S]):
// rewardAchieveList is an array; gachaDetail is an object (the gacha row, with previewGardenImageUrl, previewImageUrl and
// collection inside it, as GachaItemInfo::SetData reads it); vipGrade a string and vipGachaDiscountRate an int.
type circleRewardJSON struct {
	RewardAchieveList    []circlePointRewardJSON `json:"rewardAchieveList"`
	GachaDetail          any                     `json:"gachaDetail"`
	VipGrade             string                  `json:"vipGrade"`
	VipGachaDiscountRate int64                   `json:"vipGachaDiscountRate"`
}

// circleGachaJSON is gachaDetail when the circle page offers the Cherry Gacha: its row, then the detail's previews and
// collection (the same gacha route serves the pulls).
type circleGachaJSON struct {
	gachaRow
	PreviewGardenImageURL string          `json:"previewGardenImageUrl"`
	PreviewImageURL       string          `json:"previewImageUrl"`
	Collection            gachaCollection `json:"collection"`
}

// circleEmpty is the empty object `{}`.
type circleEmpty struct{}

// handleCircle dispatches /v4/circle/... by route shape.
func handleCircle(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/circle/"), "/"), "/")
	var nums []int64
	pat := make([]string, len(p))
	for i, s := range p {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && httpx.IsAllDigits(s) {
			pat[i] = "#"
			nums = append(nums, n)
		} else {
			pat[i] = s
		}
	}
	token := httpx.CookieValue(r, "AV_AUTH")
	now := time.Now()
	key := r.Method + " " + strings.Join(pat, "/")
	switch key {
	case "GET join/info":
		circleJoinInfoReply(w, token)
	case "GET create/default/item":
		writeCircleResult(w, circleDefaultItemView())
	case "POST create":
		var req CircleCreateRequest
		if !readCircleBody(w, r, &req) {
			return
		}
		made, err := CreateCircle(token, req, now)
		if err != nil {
			writeCircleError(w, err)
			return
		}
		writeCircleResult(w, circleCreatedView(made))
	case "GET logo/sb/list":
		writeCircleResult(w, circleLogoView(ActiveCircleData().Logos))
	case "GET logo/bg/list":
		writeCircleResult(w, circleLogoView(ActiveCircleData().Backgrounds))
	case "POST modify/withint":
		var req CircleModifyRequest
		if !readCircleBody(w, r, &req) {
			return
		}
		writeCircleDone(w, ModifyCircle(token, req, now))
	case "GET recruit/list":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) { return recruitView(b), nil })
	case "GET apply/invite/avatar":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) { return inviteListView(acc.Aid, b), nil })
	case "GET apply/invite/#":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
			return applyInviteView(acc.Aid, b, nums[0]), nil
		})
	case "GET visit/#":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) { return visitView(acc.Aid, b, nums[0]) })
	case "POST apply/join/#":
		writeCircleDone(w, JoinCircle(token, nums[0], r.URL.Query().Get("cancleAndApplyYn") == "true", now))
	case "POST apply/join/cancel/#":
		writeCircleDone(w, CancelApply(token, nums[0]))
	case "POST apply/join/approval/#/#": // device: the id comes after the verb, whatever the spec's table says
		writeCircleDone(w, ApproveApply(token, nums[0], strconv.FormatInt(nums[1], 10), now))
	case "POST apply/join/refuse/#/#":
		writeCircleDone(w, RefuseApply(token, nums[0], strconv.FormatInt(nums[1], 10)))
	case "POST invite/#/#":
		writeCircleDone(w, InviteCircle(token, nums[0], strconv.FormatInt(nums[1], 10), now))
	case "POST invite/cancel/#/#":
		writeCircleDone(w, CancelInvite(token, nums[0], strconv.FormatInt(nums[1], 10)))
	case "POST invite/approval/#":
		writeCircleDone(w, AcceptInvite(token, nums[0], now))
	case "POST invite/refuse/#":
		writeCircleDone(w, RefuseInvite(token, nums[0]))
	case "POST invite/target/status":
		var req circleInviteStatusRequest
		if !readCircleBody(w, r, &req) {
			return
		}
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
			return inviteStatusView(b, int64(req.CircleID), req.InviteTargetAidList), nil
		})
	case "POST leave/#":
		writeCircleDone(w, LeaveCircle(token, nums[0], now))
	case "POST close/issue/code/#":
		key, code, err := IssueCloseCode(token, nums[0])
		if err != nil {
			writeCircleError(w, err)
			return
		}
		writeCircleResult(w, struct {
			Result string `json:"result"`
			Key    string `json:"key"`
			Code   int    `json:"code"`
		}{"", key, code})
	case "POST search":
		circleSearch(w, r, token, false)
	case "POST search/interest":
		circleSearch(w, r, token, true)
	case "GET point/rank/total/#":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
			// nums[0] is the viewer's circle id, not a season: the total counts every season, and seasonId is the current one.
			return circleRankView(acc.Aid, b, circleSeasonAt(now), true), nil
		})
	case "GET point/rank/season/#":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
			// nums[0] is the viewer's circle id (the room's popup sends it here), not a season: always the current one.
			return circleRankSeasonView(acc.Aid, b, now), nil
		})
	case "GET point/rank/entry/top3":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) { return circleHallView(b, now), nil })
	case "GET point/season/info/#":
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
			return circlePointSeasonView(nums[0], acc.Aid, b, now)
		})
	case "GET point/season/reward/#/#":
		gacha := circleGachaDetail() // built before the book lock: the catalog is not read under it
		circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
			return circleRewardView(acc.Aid, b, nums[0], nums[1], gacha), nil
		})
	default:
		// POST close/<id>/... takes any tail (the code and key the close screen sends are not checked).
		if r.Method == http.MethodPost && len(p) >= 2 && p[0] == "close" && len(nums) >= 1 && pat[1] == "#" {
			writeCircleDone(w, CloseCircle(token, nums[0]))
			return
		}
		httpx.ServeNotFound(w)
	}
}

// writeCircleResult writes {"result": v} with HTTP 200.
func writeCircleResult(w http.ResponseWriter, v any) {
	httpx.WriteObj(w, circleReply{Result: v})
}

// writeCircleDone answers a request that carries no result body: {"result": {}} on success.
func writeCircleDone(w http.ResponseWriter, err error) {
	if err != nil {
		writeCircleError(w, err)
		return
	}
	writeCircleResult(w, circleEmpty{})
}

// writeCircleError writes a refusal: the client's error code in errorCode with a non-2xx status (ADI reads it that way).
func writeCircleError(w http.ResponseWriter, err error) {
	var ce *CircleError
	switch {
	case errors.As(err, &ce):
		httpx.WriteJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"errorCode":"%d","errorMessage":"cherry: circle"}`, ce.Code))
	case errors.Is(err, ErrCircleSession):
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
	default:
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
	}
}

// readCircleBody decodes a native JSON body into dst; it answers 400 and returns false when it cannot.
func readCircleBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return false
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, dst); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return false
	}
	return true
}

// circleReadReply answers a read for the requester: view runs with the account and the book locked, and its error
// (a missing circle) is the refusal.
func circleReadReply(w http.ResponseWriter, token string, view func(acc *store.Account, b *store.CircleBook) (any, error)) {
	var v any
	err := circleTx(token, false, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		var err error
		v, err = view(acc, b)
		return nil, err
	})
	if err != nil {
		writeCircleError(w, err)
		return
	}
	writeCircleResult(w, v)
}

// circleJoinInfoReply is circle/join/info. It is requested on every Garden load, so it never refuses: a request
// without a valid session gets the not-in-a-circle reply.
func circleJoinInfoReply(w http.ResponseWriter, token string) {
	v := any(circleInvitedJSON{})
	_ = circleTx(token, false, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		v = joinInfoView(acc.Aid, b)
		return nil, nil
	})
	writeCircleResult(w, v)
}

// circleSearch serves circle/search (by name or code) and circle/search/interest.
func circleSearch(w http.ResponseWriter, r *http.Request, token string, byInterest bool) {
	var req circleSearchRequest
	if !readCircleBody(w, r, &req) {
		return
	}
	page, size := max(int(req.Page), 1), int(req.Size)
	if size < 1 {
		size = 10
	}
	size = min(size, circlePageMax)
	match := func(c *store.Circle) bool {
		if byInterest {
			for _, code := range req.InterestCodeList {
				if slices.Contains(c.Interests, int64(code)) {
					return true
				}
			}
			return false
		}
		if req.CircleCode != "" {
			return c.Code == req.CircleCode
		}
		return strings.Contains(strings.ToLower(c.Name), strings.ToLower(req.CircleName))
	}
	circleReadReply(w, token, func(acc *store.Account, b *store.CircleBook) (any, error) {
		return searchView(b, match, page, size), nil
	})
}

// circleDefaultItemView is the creation price row. It is the only price Cherry offers.
func circleDefaultItemView() circleDefaultItemJSON {
	d := ActiveCircleData()
	return circleDefaultItemJSON{
		ProductCode:      d.Product.Code,
		ProductName:      d.Product.Name,
		ProductImagePath: circleArtPath(d.Backgrounds, d.Backgrounds[0].ID),
		MaxMemberCount:   d.Product.MaxMembers,
		PaymentType:      "GEM",
		OriginPrice:      d.Product.PriceGems,
		FinalPrice:       d.Product.PriceGems,
		CpID:             CircleCpID,
		Purchasable:      true,
	}
}

func circleCreatedView(c store.Circle) circleCreatedJSON {
	id := strconv.FormatInt(c.ID, 10)
	out := circleCreatedJSON{CircleID: id}
	out.CircleMaster.CircleID = id
	return out
}

// circleLogoView lists the logo or background choices (CircleImageInfoParser reads an array).
func circleLogoView(list []CircleArt) []circleLogoJSON {
	out := []circleLogoJSON{}
	for _, a := range list {
		path := "cherry/circle/" + a.Art
		out = append(out, circleLogoJSON{LogoID: a.ID, LogoImagePath: path, LogoDpImagePath: path})
	}
	return out
}

// circleArtPath is the relative path of the picture of art id in list ("" for an unknown id).
func circleArtPath(list []CircleArt, id int64) string {
	for _, a := range list {
		if a.ID == id {
			return "cherry/circle/" + a.Art
		}
	}
	return ""
}

func circleIDText(id int64) string { return strconv.FormatInt(id, 10) }

// circleInfoView is one circle's keys. The dates are epoch milliseconds, as the other Cherry records send them.
func circleInfoView(c *store.Circle) circleInfoJSON {
	d := ActiveCircleData()
	logo := circleArtPath(d.Logos, c.LogoID)
	bg := circleArtPath(d.Backgrounds, c.LogoBgID)
	created := strconv.FormatInt(c.Created, 10)
	return circleInfoJSON{
		CircleID: circleIDText(c.ID), CircleCode: c.Code, CircleName: c.Name,
		LogoID: c.LogoID, LogoImgPath: logo, LogoDpImgPath: logo,
		LogoBgID: c.LogoBgID, LogoBgImgPath: bg,
		CircleIntroduce: c.Intro, MemberCount: len(c.Members), MaxMemberCount: circleCapOf(c),
		RegistDate: created, MasterAid: leaderAid(c), CreateDate: created,
		ModifyDate: strconv.FormatInt(c.Modified, 10), JoinType: c.JoinType,
	}
}

// wireLevel maps a role to the client's memberLevel string: MASTER, MANAGER or MEMBER (anything but MASTER and
// MANAGER reads as a plain member).
func wireLevel(role string) string {
	switch role {
	case roleLeader:
		return "MASTER"
	case roleVice:
		return "MANAGER"
	}
	return "MEMBER"
}

// circleAvatarView is one avatar of circle c. The name comes from the account table (locked by the caller).
func circleAvatarView(aid, role string, date int64) circleAvatarJSON {
	name := ""
	if a := store.AccountByAidLocked(aid); a != nil {
		name = a.Name
	}
	ms := strconv.FormatInt(date, 10)
	return circleAvatarJSON{
		AvatarID: aid, MemberAid: aid, AvatarName: name, MemberLevel: wireLevel(role),
		RegistDate: ms, CircleJoinedDate: ms,
	}
}

func membersView(c *store.Circle) []circleAvatarJSON {
	out := []circleAvatarJSON{}
	for _, m := range c.Members {
		out = append(out, circleAvatarView(m.Aid, m.Role, m.Joined))
	}
	return out
}

func interestsView(c *store.Circle) []circleInterestJSON {
	out := []circleInterestJSON{}
	for _, code := range c.Interests {
		out = append(out, circleInterestJSON{InterestNumberCode: code})
	}
	return out
}

// joinInfoView is circle/join/info for aid: the circle when aid is in one, else the count of invitations.
func joinInfoView(aid string, b *store.CircleBook) any {
	if ci := circleOf(b, aid); ci >= 0 {
		c := &b.Circles[ci]
		info := circleInfoView(c)
		return circleMyInfoJSON{
			circleInfoJSON:     info,
			CircleMasterExt:    info,
			CircleInterestList: interestsView(c),
			CircleMemberBySelf: circleLevelJSON{Level: wireLevel(roleOf(c, aid))},
		}
	}
	n := 0
	for i := range b.Circles {
		if inviteIndex(&b.Circles[i], aid) >= 0 {
			n++
		}
	}
	return circleInvitedJSON{InvitedCount: n}
}

// recruitView lists the open circles that have room, newest first (circle/recruit/list is an array).
func recruitView(b *store.CircleBook) []circleInfoJSON {
	out := []circleInfoJSON{}
	for i := len(b.Circles) - 1; i >= 0 && len(out) < circleListMax; i-- {
		c := &b.Circles[i]
		if c.JoinType == circleOpen && len(c.Members) < circleCapOf(c) {
			out = append(out, circleInfoView(c))
		}
	}
	return out
}

// searchView pages the open circles that match, newest first. Invite-only circles are never listed (arts 5354/5355).
func searchView(b *store.CircleBook, match func(c *store.Circle) bool, page, size int) circleSearchJSON {
	var hits []*store.Circle
	for i := len(b.Circles) - 1; i >= 0; i-- {
		c := &b.Circles[i]
		if c.JoinType == circleOpen && match(c) {
			hits = append(hits, c)
		}
	}
	total := len(hits)
	start, end := total, total // a page past the end is empty
	if page <= total {         // page <= 1000 here, so (page-1)*size cannot overflow
		start = min((page-1)*size, total)
		end = min(start+size, total)
	}
	items := []circleInfoJSON{}
	for _, c := range hits[start:end] {
		items = append(items, circleInfoView(c))
	}
	out := circleSearchJSON{Items: items, TotalCount: strconv.Itoa(total)}
	if page > 1 {
		out.PreviousCursor = strconv.Itoa(page - 1)
	}
	if end < total {
		out.NextCursor = strconv.Itoa(page + 1)
	}
	return out
}

// visitView is circle/visit/<id>. A missing circle is a refusal (the client shows its text).
func visitView(viewer string, b *store.CircleBook, id int64) (any, error) {
	ci := circleIndex(b, id)
	if ci < 0 {
		return nil, circleRefuse(circleErrNotFound)
	}
	c := &b.Circles[ci]
	joined := ""
	if mi := circleOf(b, viewer); mi >= 0 {
		joined = circleIDText(b.Circles[mi].ID)
	}
	applied := "" // the viewer's pending application, to any circle
	for i := range b.Circles {
		if applyIndex(&b.Circles[i], viewer) >= 0 {
			applied = circleIDText(b.Circles[i].ID)
		}
	}
	return circleDetailJSON{
		CircleMasterExt:     circleInfoView(c),
		CircleInterestList:  interestsView(c),
		RoomskinDpImagePath: circleArtPath(ActiveCircleData().Backgrounds, c.LogoBgID),
		InvitedYn:           inviteIndex(c, viewer) >= 0,
		JoinedCircleID:      joined,
		AppliedJoinCircleID: applied,
		CircleMemberList:    membersView(c),
	}, nil
}

// inviteListView is circle/apply/invite/avatar: the viewer's invitations, and (for a leader or vice) the applications
// to the viewer's circle.
func inviteListView(viewer string, b *store.CircleBook) circleInviteListJSON {
	out := circleInviteListJSON{ApplyJoinList: []circleAvatarJSON{}, InviteList: []circleInviteJSON{}}
	if ci := circleOf(b, viewer); ci >= 0 && canInvite(&b.Circles[ci], viewer) {
		c := &b.Circles[ci]
		for _, a := range c.Applies {
			av := circleAvatarView(a.Aid, roleMember, a.Date)
			av.ApplyAid = a.Aid
			out.ApplyJoinList = append(out.ApplyJoinList, av)
		}
	}
	for i := range b.Circles {
		c := &b.Circles[i]
		for _, inv := range c.Invites {
			if inv.Aid != viewer {
				continue
			}
			sender := circleAvatarView(inv.By, roleMember, inv.Date)
			out.InviteList = append(out.InviteList, circleInviteJSON{
				circleInfoJSON: circleInfoView(c),
				SenderAid:      inv.By, ReceiverAid: viewer,
				AvatarID: inv.By, AvatarName: sender.AvatarName, MemberAid: inv.By,
				MemberLevel: "MEMBER", RegistDate: strconv.FormatInt(inv.Date, 10),
			})
		}
	}
	return out
}

// applyInviteView is circle/apply/invite/<id>: a leader or vice sees the circle's invitees and applicants; anyone else
// sees empty lists.
func applyInviteView(viewer string, b *store.CircleBook, id int64) circleApplyInviteJSON {
	out := circleApplyInviteJSON{InviteList: []circleAvatarJSON{}, ApplyJoinList: []circleAvatarJSON{}}
	ci := circleIndex(b, id)
	if ci < 0 || !canInvite(&b.Circles[ci], viewer) {
		return out
	}
	c := &b.Circles[ci]
	for _, inv := range c.Invites {
		av := circleAvatarView(inv.Aid, roleMember, inv.Date)
		av.SenderAid = inv.By
		out.InviteList = append(out.InviteList, av)
	}
	for _, a := range c.Applies {
		av := circleAvatarView(a.Aid, roleMember, a.Date)
		av.ApplyAid = a.Aid
		out.ApplyJoinList = append(out.ApplyJoinList, av)
	}
	return out
}

// circleGachaDetail is gachaDetail of the reward page: the Cherry Gacha when the circle data offers it, else {}.
func circleGachaDetail() any {
	if !ActiveCircleData().Gacha.Enabled {
		return circleEmpty{}
	}
	pool := gachaPoolNow()
	return circleGachaJSON{gachaRow: gachaRowFor(pool), Collection: gachaCollectionFor(pool)}
}

// circleMS is an epoch-millisecond date as the client's string.
func circleMS(ms int64) string { return strconv.FormatInt(ms, 10) }

// circlePointsTotal is the circle's points in season n, or in all its seasons when all is set.
func circlePointsTotal(c *store.Circle, n int64, all bool) int64 {
	if all {
		var sum int64
		for _, s := range c.Seasons {
			sum += s.Total
		}
		return sum
	}
	if s := circleSeasonRec(c, n, false); s != nil {
		return s.Total
	}
	return 0
}

// circleRanked is a circle with points in the ranked season.
type circleRanked struct {
	c   *store.Circle
	pts int64
}

// circleRanking lists the circles with points, most points first (then by id).
func circleRanking(b *store.CircleBook, season int64, all bool) []circleRanked {
	var out []circleRanked
	for i := range b.Circles {
		c := &b.Circles[i]
		if pts := circlePointsTotal(c, season, all); pts > 0 {
			out = append(out, circleRanked{c: c, pts: pts})
		}
	}
	slices.SortStableFunc(out, func(x, y circleRanked) int {
		if x.pts != y.pts {
			return cmp.Compare(y.pts, x.pts)
		}
		return cmp.Compare(x.c.ID, y.c.ID)
	})
	return out
}

// circleRankView is circle/point/rank/total (all seasons) or .../season for the viewer's season: the top circles as rows,
// and the viewer's circle as a row ({} when the viewer is in no circle). Rows follow circleRanking, so a row's rank is its
// place; a circle with no points is in no row and has rank "".
func circleRankView(viewer string, b *store.CircleBook, season int64, all bool) circleRankJSON {
	ranked := circleRanking(b, season, all)
	out := circleRankJSON{SeasonID: season, MyCircle: circleEmpty{}, TopRankCircleList: []circleRankRowJSON{}}
	for i, r := range ranked {
		if i >= circleListMax {
			break
		}
		out.TopRankCircleList = append(out.TopRankCircleList, circleRankRowView(r.c, r.pts, strconv.Itoa(i+1)))
	}
	if ci := circleOf(b, viewer); ci >= 0 {
		c := &b.Circles[ci]
		rank := ""
		for i, r := range ranked {
			if r.c.ID == c.ID {
				rank = strconv.Itoa(i + 1)
				break
			}
		}
		out.MyCircle = circleRankRowView(c, circlePointsTotal(c, season, all), rank)
	}
	return out
}

// circleRankRowView is one circle of a rank reply with its points and its rank ("" when it has none).
func circleRankRowView(c *store.Circle, pts int64, rank string) circleRankRowJSON {
	return circleRankRowJSON{circleInfoJSON: circleInfoView(c), MyCirclePoint: pts, MyCircleRank: rank}
}

// circleRankSeasonView is circle/point/rank/season: the ranking of the season at now, with the banner keys. The argument of
// the route is never a season, so the caller passes only the clock. gachaName is the Cherry Gacha's name when the circle
// page offers it, else "".
func circleRankSeasonView(viewer string, b *store.CircleBook, now time.Time) circleRankSeasonJSON {
	season := circleSeasonAt(now)
	start, end := circleSeasonBounds(season)
	out := circleRankSeasonJSON{
		circleRankJSON:  circleRankView(viewer, b, season, false),
		SeasonName:      circleSeasonName(season),
		SeasonStartDate: circleMS(start), GainEndDate: circleMS(end), SeasonEndDate: circleMS(end),
	}
	if ActiveCircleData().Gacha.Enabled {
		out.GachaName = ActiveGacha().Name
	}
	return out
}

// circleHallView is the hall of fame: the top three circles of the last finished season (none in season 1).
func circleHallView(b *store.CircleBook, now time.Time) circleHallJSON {
	out := circleHallJSON{EntryList: []circleHallEntryJSON{}}
	prev := circleSeasonAt(now) - 1
	if prev < 1 {
		return out
	}
	start, end := circleSeasonBounds(prev)
	out.SeasonID, out.SeasonName = prev, circleSeasonName(prev)
	out.SeasonStartDate, out.SeasonEndDate, out.GainEndDate = circleMS(start), circleMS(end), circleMS(end)
	for i, r := range circleRanking(b, prev, false) {
		if i >= 3 {
			break
		}
		out.EntryList = append(out.EntryList, circleHallEntryJSON{
			SeasonID: prev, Rank: int64(i + 1), CircleID: circleIDText(r.c.ID), CircleName: r.c.Name,
			MemberCount: len(r.c.Members), MaxMemberCount: circleCapOf(r.c), TotalGainPoint: r.pts,
		})
	}
	return out
}

// circlePointRewardView is one point step of season: its Gems as a GEM reward, reached when the circle total is at the
// target, claimed when this member has taken it.
func circlePointRewardView(season, seq int64, s CircleStep, total int64, claimed bool) circlePointRewardJSON {
	return circlePointRewardJSON{
		SeasonID: season, RewardSeq: seq, RewardType: "GEM", RewardAmount: s.Gems, AchievePoint: s.Points,
		CircleAchieved: total >= s.Points, MemberReceivedYn: claimed,
	}
}

// circlePointSeasonView is circle/point/season/info/<circleId> for viewer in the current season.
func circlePointSeasonView(circleID int64, viewer string, b *store.CircleBook, now time.Time) (circlePointSeasonJSON, error) {
	ci := circleIndex(b, circleID)
	if ci < 0 {
		return circlePointSeasonJSON{}, circleRefuse(circleErrNotFound)
	}
	c := &b.Circles[ci]
	season := circleSeasonAt(now)
	start, end := circleSeasonBounds(season)
	total, ms := circlePointsOf(c, season, viewer)
	eligible := false
	if mi := memberIndex(c, viewer); mi >= 0 {
		eligible = circleEligible(c.Members[mi], season)
	}
	steps := ActiveCircleData().Points.Steps
	out := circlePointSeasonJSON{
		SeasonID: season, SeasonName: circleSeasonName(season),
		SeasonStartDate: circleMS(start), GainEndDate: circleMS(end), SeasonEndDate: circleMS(end),
		CircleTotalGainPoint: strconv.FormatInt(total, 10), AvailSeasonAttendYn: eligible, AllReceived: len(steps) > 0,
		SeasonRewardList: []circlePointRewardJSON{}, MemberGainPointList: []circleMemberPointJSON{},
	}
	for i, s := range steps {
		seq := int64(i + 1)
		claimed := slices.Contains(ms.Steps, seq)
		out.SeasonRewardList = append(out.SeasonRewardList, circlePointRewardView(season, seq, s, total, claimed))
		if !claimed {
			out.AllReceived = false
		}
		if eligible && !claimed && total >= s.Points {
			out.RewardReceivable = true
		}
	}
	if info := circlePointInfo(total, ms.Steps, eligible); len(steps) > 0 {
		stage := min(info.LastRewardSeq+1, info.MaxRewardSeq)
		out.CurrentReward = circlePointRewardView(season, stage, steps[stage-1], total, slices.Contains(ms.Steps, stage))
	}
	for _, m := range c.Members {
		_, mms := circlePointsOf(c, season, m.Aid)
		out.MemberGainPointList = append(out.MemberGainPointList, circleMemberPointJSON{
			circleAvatarJSON: circleAvatarView(m.Aid, m.Role, m.Joined), SeasonID: season,
			TotalGainPoint: mms.Points, AvailSeasonAttendYn: circleEligible(m, season),
		})
	}
	return out, nil
}

// circleRewardView is circle/point/season/reward/<season>/<seq> for viewer: the step (none when seq is not a step of the
// season) and the gacha detail built by the caller.
func circleRewardView(viewer string, b *store.CircleBook, season, seq int64, gacha any) circleRewardJSON {
	out := circleRewardJSON{RewardAchieveList: []circlePointRewardJSON{}, GachaDetail: gacha}
	steps := ActiveCircleData().Points.Steps
	if seq < 1 || seq > int64(len(steps)) {
		return out
	}
	var total int64
	var ms store.CircleMemberSeason
	if ci := circleOf(b, viewer); ci >= 0 {
		total, ms = circlePointsOf(&b.Circles[ci], season, viewer)
	}
	out.RewardAchieveList = append(out.RewardAchieveList,
		circlePointRewardView(season, seq, steps[seq-1], total, slices.Contains(ms.Steps, seq)))
	return out
}

// HandleCircleArt serves GET /cherry/circle/<file>: the logo and background pictures the circle data names, read in
// place from the archive. Any other name is not found.
func HandleCircleArt(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/cherry/circle/")
	if r.Method != http.MethodGet || !circleArtListed(name) {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(circleArtFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

// circleArtListed reports whether name is a picture the circle data offers.
func circleArtListed(name string) bool {
	d := ActiveCircleData()
	return slices.ContainsFunc(d.Logos, func(a CircleArt) bool { return a.Art == name }) ||
		slices.ContainsFunc(d.Backgrounds, func(a CircleArt) bool { return a.Art == name })
}

// circleInviteStatusRequest is the body of POST circle/invite/target/status (builder 0x1bcea18): the circle id and the
// friends shown on the Invite screen, all as strings.
type circleInviteStatusRequest struct {
	CircleID            looseInt `json:"circleId"`
	InviteTargetAidList []string `json:"inviteTargetAidList"`
}

// circleInviteStatusJSON is one row of the reply. SetInviteFriendInfo 0x24fa920 indexes result as an ARRAY with one
// element per requested aid (an object left every row on its loading spinner, device 2026-11-01): "INVITABLE" shows the
// invite button, "INVITING" the invited label, anything else a done label (member of this circle or not, decided from the
// client's member map). exit-invite.md section B.
type circleInviteStatusJSON struct {
	InviteTargetAid string `json:"inviteTargetAid"`
	InviteStatus    string `json:"inviteStatus"`
}

// inviteStatusView answers each requested aid: MEMBER when it is in this circle, INVITING when it holds this circle's
// invitation, INVITABLE when it is in no circle, else JOINED (in another circle).
func inviteStatusView(b *store.CircleBook, id int64, aids []string) []circleInviteStatusJSON {
	out := make([]circleInviteStatusJSON, 0, len(aids))
	ci := circleIndex(b, id)
	for _, aid := range aids {
		status := "JOINED"
		switch {
		case ci >= 0 && memberIndex(&b.Circles[ci], aid) >= 0:
			status = "MEMBER"
		case ci >= 0 && inviteIndex(&b.Circles[ci], aid) >= 0:
			status = "INVITING"
		case circleOf(b, aid) < 0:
			status = "INVITABLE"
		}
		out = append(out, circleInviteStatusJSON{InviteTargetAid: aid, InviteStatus: status})
	}
	return out
}
