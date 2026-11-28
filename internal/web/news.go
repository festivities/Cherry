package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

const maxNewsStreamItems = 40

type newsEnvelope struct {
	Type            string `json:"type"`
	NotiSn          string `json:"notiSn"`
	RegDate         string `json:"regDate"`
	RefID           string `json:"refId"`
	HeartReceivable bool   `json:"heartReceivable"`
	Data            string `json:"data"`
}

type newsBannerData struct {
	LinkType             string `json:"linkType"`
	GameID               string `json:"gameId"`
	Flag                 string `json:"flag"`
	Image                string `json:"image"`
	Link                 string `json:"link"`
	BannerID             int64  `json:"bannerId"`
	Description          string `json:"description"`
	EventStartYmdt       int64  `json:"eventStartYmdt"`
	EventEndYmdt         int64  `json:"eventEndYmdt"`
	EventPeriodTxt       string `json:"eventPeriodTxt"`
	ShowEndingNotiYn     string `json:"showEndingNotiYn"`
	CategoryType         string `json:"categoryType"`
	CategoryTypeNm       string `json:"categoryTypeNm"`
	IcnImage             string `json:"icnImage"`
	ImageSubPath         string `json:"imageSubPath"`
	EventPeriodStartYmdt int64  `json:"eventPeriodStartYmdt"`
	EventPeriodEndYmdt   int64  `json:"eventPeriodEndYmdt"`
}

type newsItemData struct {
	Game                  string `json:"game"`
	GameID                string `json:"gameId"`
	AvatarID              string `json:"avatarId"`
	Name                  string `json:"name"`
	AvatarProfile         string `json:"avatarProfile"`
	ObsProfileImagePath   string `json:"obsProfileImagePath"`
	AvatarBg              string `json:"avatarBg"`
	GiftBoxID             string `json:"giftBoxId"`
	Message               string `json:"message"`
	URL                   string `json:"url"`
	BadgeIcon             string `json:"badgeIcon"`
	ImageURL              string `json:"imageUrl"`
	ImageLocationType     string `json:"imageLocationType"`
	ImageTypeKind         string `json:"imageTypeKind"`
	PostMsg               string `json:"postMsg"`
	PostType              string `json:"postType"`
	PostExprDate          string `json:"postExprDate"`
	PetName               string `json:"petName"`
	PetCategory           string `json:"petCategory"`
	ViewAvatarID          string `json:"viewAvatarId"`
	CircleID              string `json:"circleId,omitempty"`
	CircleName            string `json:"circleName,omitempty"`
	CircleLogoImgPath     string `json:"circleLogoImgPath,omitempty"`
	CircleLogoBgImgPath   string `json:"circleLogoBgImgPath,omitempty"`
	CircleLogoDpImgPath   string `json:"circleLogoDpImgPath,omitempty"`
	CircleLogoBgDpImgPath string `json:"circleLogoBgDpImgPath,omitempty"`
}

type newsActorInfo struct {
	name string
	img  string
	bg   string
}

type newsActorResolver struct {
	cache map[string]newsActorInfo
}

func (r *newsActorResolver) resolve(aid, fallbackName string) newsActorInfo {
	if aid == "" {
		return newsActorInfo{name: fallbackName}
	}
	if r.cache == nil {
		r.cache = make(map[string]newsActorInfo)
	}
	if hit, ok := r.cache[aid]; ok {
		if hit.name == "" && fallbackName != "" {
			hit.name = fallbackName
		}
		return hit
	}
	var info newsActorInfo
	if aid == store.FriendAID {
		info.name = store.FriendName()
	} else if snap, ok := store.AccountByAvatarID(aid); ok {
		info.name = snap.Name
		info.img, _, info.bg = snap.ProfileImage.Fields()
	}
	r.cache[aid] = info
	if info.name == "" {
		info.name = fallbackName
	}
	return info
}

func newsTruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if len(s) <= maxRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	if !utf8.ValidString(s) {
		return string([]rune(s))
	}
	return s
}

func diaryPreviewText(post store.DiaryPost) string {
	text := strings.TrimSpace(post.Content)
	if text == "" {
		text = strings.TrimSpace(post.Title)
	}
	return newsTruncateRunes(text, 200)
}

// collectSocialNews gathers the viewer's pending friend requests, friends' public posts (newest first) and the
// News "hot post" cards: the top cfg.RecommendedMax photo posts of other players by bestRankLocked, only posts
// with some activity (a score above 0).
func collectSocialNews(viewerAid string, cfg *economy.NewsConfig) (pendingFrom map[string]bool, friendPosts, recommended []store.DiaryPost) {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	friends := make(map[string]bool)
	pendingFrom = make(map[string]bool)
	for _, f := range store.Friendships {
		switch {
		case f.State == "accepted" && f.A == viewerAid:
			friends[f.B] = true
		case f.State == "accepted" && f.B == viewerAid:
			friends[f.A] = true
		case f.State == "pending" && f.B == viewerAid:
			pendingFrom[f.A] = true
		}
	}
	type candidate struct {
		seq  uint64
		post store.DiaryPost
	}
	var friendCandidates []candidate
	for _, post := range store.DiaryPosts {
		if post.Blind || post.OpenState != "public" {
			continue
		}
		seq, err := strconv.ParseUint(post.DiaryNo, 10, 64)
		if err != nil {
			continue
		}
		if friends[post.AvatarID] {
			friendCandidates = append(friendCandidates, candidate{seq, post})
		}
	}
	sort.Slice(friendCandidates, func(i, j int) bool { return friendCandidates[i].seq > friendCandidates[j].seq })
	if len(friendCandidates) > maxNewsStreamItems {
		friendCandidates = friendCandidates[:maxNewsStreamItems]
	}
	friendPosts = make([]store.DiaryPost, len(friendCandidates))
	for i, c := range friendCandidates {
		friendPosts[i] = c.post
	}
	if cfg.IncludeRecommendedPosts && cfg.RecommendedMax > 0 {
		hot := bestRankLocked(cfg, func(post store.DiaryPost) bool {
			return !post.Blind && post.OpenState == "public" && post.AvatarID != viewerAid && hasDiaryPhoto(post)
		})
		for _, b := range hot {
			if b.score <= 0 || len(recommended) == cfg.RecommendedMax {
				break
			}
			recommended = append(recommended, b.post)
		}
	}
	return pendingFrom, friendPosts, recommended
}

func buildAlertEnvelope(viewerAid string, alert store.NewsAlert, actor newsActorInfo, mailBySeq map[string]store.MailRow, host string) newsEnvelope {
	data := newsItemData{
		AvatarID:            alert.Actor,
		Name:                actor.name,
		AvatarProfile:       strings.TrimPrefix(actor.img, "/"),
		ObsProfileImagePath: actor.img,
		AvatarBg:            actor.bg,
		ImageLocationType:   "none",
		ImageTypeKind:       "none",
	}
	heartReceivable := false
	switch alert.Type {
	case "HEART_RECEIVE":
		heartReceivable = economy.HeartReceivable(viewerAid, alert.Actor)
	case "DIARY_STICKER_SAVE", "DIARY_COMMENT_SAVE", "GUESTBOOK_WRITE_SAVE", "GUESTBOOK_COMMENT_SAVE":
		data.GiftBoxID = alert.Ref
		data.Message = alert.Message
	case "POSTBOX_RECEIVE":
		data.GiftBoxID = alert.Ref
		data.PostMsg = alert.Message
		data.PostType = alert.PostType
		if data.PostType == "" {
			data.PostType = "gift"
		}
		data.PostExprDate = "0"
		if row, ok := mailBySeq[alert.Ref]; ok && row.Expires > 0 {
			data.PostExprDate = strconv.FormatInt(row.Expires, 10)
		}
		if data.PostType == "item" && alert.Item != "" {
			data.ImageURL = economy.MailItemImageURL(alert.Item, host)
		}
	case "POSTBOX_OPEN":
		data.GiftBoxID = alert.Ref
	case "CIRCLE_APPLY_JOIN", "CIRCLE_INVITE_AVATAR", "CIRCLE_MEMBER_JOIN", "CIRCLE_CHANGE_MASTER", "CIRCLE_ACCEPT_INVITATION":
		data.CircleID = alert.Ref
		data.CircleName = alert.CircleName
		data.CircleLogoImgPath = alert.CircleLogo
		data.CircleLogoBgImgPath = alert.CircleBg
		data.CircleLogoDpImgPath = alert.CircleLogo
		data.CircleLogoBgDpImgPath = alert.CircleBg
	case "PET_SOMEONE_TOUCHED", "PET_HEART_RECEIVE":
		data.PetName = alert.PetName
		data.PetCategory = alert.PetCategory
	}
	raw, _ := json.Marshal(data)
	return newsEnvelope{
		Type:            alert.Type,
		NotiSn:          alert.Sn,
		RegDate:         strconv.FormatInt(alert.At, 10),
		RefID:           alert.Ref,
		HeartReceivable: heartReceivable,
		Data:            string(raw),
	}
}

func buildDiaryNewsEnvelope(post store.DiaryPost, actor newsActorInfo) newsEnvelope {
	seq, _ := strconv.ParseUint(post.DiaryNo, 10, 64)
	imgURL, locType, kind := "", "none", "none"
	if len(post.Images) > 0 && post.ImageLocationType != "" && post.ImageLocationType != "none" {
		imgURL = post.Images[0].ImageURL
		locType = post.ImageLocationType
		kind = post.ImageTypeKind
		if kind == "" || kind == "photo" || kind == "scrp_post" {
			if locType == "obs" {
				kind = "obs"
			} else {
				kind = locType
			}
		}
	}
	data := newsItemData{
		AvatarID:            post.AvatarID,
		Name:                actor.name,
		AvatarProfile:       strings.TrimPrefix(actor.img, "/"),
		ObsProfileImagePath: actor.img,
		AvatarBg:            actor.bg,
		GiftBoxID:           post.DiaryNo,
		Message:             diaryPreviewText(post),
		ImageURL:            imgURL,
		ImageLocationType:   locType,
		ImageTypeKind:       kind,
	}
	raw, _ := json.Marshal(data)
	return newsEnvelope{
		Type:    "DIARY_WRITE_SAVE",
		NotiSn:  strconv.FormatUint(1_000_000_000+seq, 10),
		RegDate: post.RegDate,
		RefID:   post.DiaryNo,
		Data:    string(raw),
	}
}

func handleNewsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked(viewer.Aid)
	var newsSnap *store.NewsState
	var mailSnap []store.MailRow
	if acc != nil {
		newsSnap = store.CloneNewsState(acc.News)
		mailSnap = slices.Clone(acc.Mail)
	}
	store.AccountsMu.Unlock()

	now := economy.NowFunc()
	cfg := economy.ActiveNews()
	pendingFrom, friendPosts, recPosts := collectSocialNews(viewer.Aid, cfg)

	mailBySeq := make(map[string]store.MailRow, len(mailSnap))
	for _, m := range mailSnap {
		mailBySeq[strconv.FormatInt(m.Seq, 10)] = m
	}
	readBannerSet := make(map[int64]bool)
	var alertReadAt int64
	var rawAlerts []store.NewsAlert
	if newsSnap != nil {
		alertReadAt = newsSnap.AlertReadAt
		rawAlerts = newsSnap.Alerts
		for _, id := range newsSnap.ReadBanners {
			readBannerSet[id] = true
		}
	}

	var actors newsActorResolver
	type newsStreamEntry struct {
		regMs int64
		seq   uint64
		env   newsEnvelope
	}
	alertList := make([]newsEnvelope, 0, len(rawAlerts))
	stream := make([]newsStreamEntry, 0, len(rawAlerts)+len(friendPosts))
	newAlertCount := 0

	for _, alert := range rawAlerts {
		if alert.Type == "FRIEND_RECEIVE" && !pendingFrom[alert.Actor] {
			continue
		}
		actor := actors.resolve(alert.Actor, alert.ActorName)
		env := buildAlertEnvelope(viewer.Aid, alert, actor, mailBySeq, r.Host)
		alertList = append(alertList, env)
		if alert.At > alertReadAt {
			newAlertCount++
		}
		if !strings.HasPrefix(alert.Type, "CIRCLE_") {
			snSeq, _ := strconv.ParseUint(alert.Sn, 10, 64)
			stream = append(stream, newsStreamEntry{regMs: alert.At, seq: snSeq, env: env})
		}
	}

	for _, post := range friendPosts {
		actor := actors.resolve(post.AvatarID, post.Nickname)
		env := buildDiaryNewsEnvelope(post, actor)
		regMs, _ := strconv.ParseInt(post.RegDate, 10, 64)
		snSeq, _ := strconv.ParseUint(env.NotiSn, 10, 64)
		stream = append(stream, newsStreamEntry{regMs: regMs, seq: snSeq, env: env})
	}
	sort.Slice(stream, func(i, j int) bool {
		if stream[i].regMs != stream[j].regMs {
			return stream[i].regMs > stream[j].regMs
		}
		return stream[i].seq > stream[j].seq
	})
	if len(stream) > maxNewsStreamItems {
		stream = stream[:maxNewsStreamItems]
	}

	banners := economy.ActiveNewsBanners(now)
	newsList := make([]newsEnvelope, 0, len(banners)+len(recPosts)+len(stream))
	for _, b := range banners {
		flag := "NEW"
		if readBannerSet[b.BannerID] {
			flag = ""
		}
		showEnding := "N"
		if b.EndingSoon {
			showEnding = "Y"
		}
		regMs := b.StartMs
		if regMs == 0 {
			regMs = now.UnixMilli()
		}
		bd := newsBannerData{
			LinkType:             b.LinkType,
			Link:                 b.Link,
			Flag:                 flag,
			Image:                b.Image,
			BannerID:             b.BannerID,
			Description:          b.Title,
			EventStartYmdt:       b.StartMs,
			EventEndYmdt:         b.EndMs,
			EventPeriodStartYmdt: b.StartMs,
			EventPeriodEndYmdt:   b.EndMs,
			ShowEndingNotiYn:     showEnding,
			CategoryTypeNm:       b.Category,
			IcnImage:             b.IconPath,
		}
		raw, _ := json.Marshal(bd)
		newsList = append(newsList, newsEnvelope{
			Type:    "BANNERS",
			NotiSn:  strconv.FormatInt(b.BannerID, 10),
			RegDate: strconv.FormatInt(regMs, 10),
			Data:    string(raw),
		})
	}
	for _, post := range recPosts {
		seq, _ := strconv.ParseUint(post.DiaryNo, 10, 64)
		notiSn := strconv.FormatUint(2_000_000_000+seq, 10)
		actor := actors.resolve(post.AvatarID, post.Nickname)
		bd := newsBannerData{
			LinkType:         "goSomewhere(diaryDetail)",
			Link:             post.AvatarID,
			BannerID:         0,
			Description:      diaryPreviewText(post),
			Image:            post.Images[0].ImageURL,
			ShowEndingNotiYn: "N",
			CategoryTypeNm:   actor.name,
			IcnImage:         strings.TrimPrefix(actor.img, "/"),
			ImageSubPath:     actor.bg,
		}
		raw, _ := json.Marshal(bd)
		regDate := post.RegDate
		if regDate == "" {
			regDate = "0"
		}
		newsList = append(newsList, newsEnvelope{
			Type:    "BANNERS",
			NotiSn:  notiSn,
			RegDate: regDate,
			RefID:   post.DiaryNo,
			Data:    string(raw),
		})
	}
	for _, item := range stream {
		newsList = append(newsList, item.env)
	}

	httpx.WriteObj(w, map[string]any{
		"result": map[string]any{
			"newsList":      newsList,
			"alertList":     alertList,
			"newAlertCount": newAlertCount,
		},
	})
}

func handleNewsBannerView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	bannerText := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/noti/banner/view/"), "/")
	bannerID, err := strconv.ParseInt(bannerText, 10, 64)
	if err != nil || bannerID <= 0 || strings.Contains(bannerText, "/") {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	if acc == nil || !store.ValidAvatarID(acc.Aid) || acc.Aid == "0" {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	prev := *acc
	if store.MarkBannerReadLocked(acc, bannerID) {
		if err := store.SaveLocked(nil, acc); err != nil {
			*acc = prev
			store.AccountsMu.Unlock()
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
	}
	store.AccountsMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

func newsBadgeCounts(aid string, now time.Time) (newsUnread, alertUnread int) {
	if aid == "" || aid == "0" {
		return 0, 0
	}
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked(aid)
	var newsSnap *store.NewsState
	var avatarCreated int64
	if acc != nil {
		newsSnap = store.CloneNewsState(acc.News)
		avatarCreated = acc.AvatarCreated
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		return 0, 0
	}

	var newsReadAt, alertReadAt int64
	readBannerSet := make(map[int64]bool)
	var rawAlerts []store.NewsAlert
	if newsSnap != nil {
		newsReadAt = newsSnap.NewsReadAt
		alertReadAt = newsSnap.AlertReadAt
		rawAlerts = newsSnap.Alerts
		for _, id := range newsSnap.ReadBanners {
			readBannerSet[id] = true
		}
	}
	newsCutoff := max(newsReadAt, avatarCreated)

	store.SocialMu.Lock()
	friends := make(map[string]bool)
	pendingFrom := make(map[string]bool)
	for _, f := range store.Friendships {
		switch {
		case f.State == "accepted" && f.A == aid:
			friends[f.B] = true
		case f.State == "accepted" && f.B == aid:
			friends[f.A] = true
		case f.State == "pending" && f.B == aid:
			pendingFrom[f.A] = true
		}
	}
	unreadPosts := 0
	for _, post := range store.DiaryPosts {
		if post.Blind || post.OpenState != "public" || !friends[post.AvatarID] {
			continue
		}
		if regMs, err := strconv.ParseInt(post.RegDate, 10, 64); err == nil && regMs > newsCutoff {
			unreadPosts++
		}
	}
	store.SocialMu.Unlock()

	newsUnread += unreadPosts
	for _, alert := range rawAlerts {
		if alert.Type == "FRIEND_RECEIVE" && !pendingFrom[alert.Actor] {
			continue
		}
		if alert.At > alertReadAt {
			alertUnread++
		}
		if !strings.HasPrefix(alert.Type, "CIRCLE_") && alert.At > newsCutoff {
			newsUnread++
		}
	}
	for _, b := range economy.ActiveNewsBanners(now) {
		if readBannerSet[b.BannerID] {
			continue
		}
		if (b.StartMs > 0 && b.StartMs > newsCutoff) || (b.StartMs == 0 && newsReadAt == 0) {
			newsUnread++
		}
	}
	return newsUnread, alertUnread
}

func resetNewsBadge(r *http.Request, badge string) {
	if badge != "NEWS" && badge != "ALERT" {
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	aid := ""
	if acc != nil && store.ValidAvatarID(acc.Aid) && acc.Aid != "0" {
		aid = acc.Aid
	}
	store.AccountsMu.Unlock()
	if aid == "" {
		return
	}

	now := economy.NowFunc()
	latestMs := now.UnixMilli()
	if badge == "NEWS" {
		store.SocialMu.Lock()
		friends := make(map[string]bool)
		for _, f := range store.Friendships {
			switch {
			case f.State == "accepted" && f.A == aid:
				friends[f.B] = true
			case f.State == "accepted" && f.B == aid:
				friends[f.A] = true
			}
		}
		for _, post := range store.DiaryPosts {
			if post.Blind || post.OpenState != "public" || !friends[post.AvatarID] {
				continue
			}
			if regMs, err := strconv.ParseInt(post.RegDate, 10, 64); err == nil && regMs > latestMs {
				latestMs = regMs
			}
		}
		store.SocialMu.Unlock()

		for _, b := range economy.ActiveNewsBanners(now) {
			if b.StartMs > latestMs {
				latestMs = b.StartMs
			}
		}
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc = store.AccountByAidLocked(aid)
	if acc == nil {
		return
	}
	if acc.News != nil {
		for _, alert := range acc.News.Alerts {
			if badge == "NEWS" && strings.HasPrefix(alert.Type, "CIRCLE_") {
				continue
			}
			if alert.At > latestMs {
				latestMs = alert.At
			}
		}
	}
	prev := *acc
	if store.MarkNewsReadLocked(acc, badge, latestMs) {
		if err := store.SaveLocked(nil, acc); err != nil {
			*acc = prev
		}
	}
}
