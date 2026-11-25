package store

import (
	"slices"
	"strconv"
	"unicode/utf8"
)

const (
	MaxNewsAlerts  = 50
	MaxReadBanners = 100
)

// NewsAlert is one persisted notification in Account.News.Alerts (newest first).
type NewsAlert struct {
	Sn          string `json:"sn"`
	Type        string `json:"type"`
	At          int64  `json:"at"`
	Actor       string `json:"actor,omitempty"`
	ActorName   string `json:"actorName,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Message     string `json:"msg,omitempty"`
	PostType    string `json:"postType,omitempty"`
	Item        string `json:"item,omitempty"`
	Expires     int64  `json:"expires,omitempty"`
	CircleName  string `json:"circleName,omitempty"`
	CircleLogo  string `json:"circleLogo,omitempty"`
	CircleBg    string `json:"circleBg,omitempty"`
	PetName     string `json:"petName,omitempty"`
	PetCategory string `json:"petCat,omitempty"`
}

// NewsState holds an account's What's NEW read watermarks, seen banner IDs, and personal alerts.
// Replaced by cloning on every write so rollback copies (*acc = prev) never alias mutated slices.
type NewsState struct {
	NextSn      int64       `json:"nextSn,omitempty"`
	NewsReadAt  int64       `json:"newsReadAt,omitempty"`
	AlertReadAt int64       `json:"alertReadAt,omitempty"`
	ReadBanners []int64     `json:"readBanners,omitempty"`
	Alerts      []NewsAlert `json:"alerts,omitempty"`
}

// CloneNewsState returns a deep copy of ns, or nil when ns is nil.
func CloneNewsState(ns *NewsState) *NewsState {
	if ns == nil {
		return nil
	}
	cp := *ns
	cp.ReadBanners = slices.Clone(ns.ReadBanners)
	cp.Alerts = slices.Clone(ns.Alerts)
	return &cp
}

func truncateNewsRunes(s string, maxRunes int) string {
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

// AppendNewsAlertLocked prepends alert to acc.News via a cloned NewsState, assigning a monotonic
// numeric Sn and trimming Alerts to MaxNewsAlerts. Caller holds AccountsMu and must save acc.
func AppendNewsAlertLocked(acc *Account, alert NewsAlert) {
	if acc == nil || acc.Aid == "" || acc.Aid == "0" || acc.Aid == FriendAID || (alert.Actor != "" && alert.Actor == acc.Aid) {
		return
	}
	ns := CloneNewsState(acc.News)
	if ns == nil {
		ns = &NewsState{}
	}
	ns.NextSn++
	alert.Sn = strconv.FormatInt(ns.NextSn, 10)
	alert.Message = truncateNewsRunes(alert.Message, 200)
	alerts := make([]NewsAlert, 0, min(len(ns.Alerts)+1, MaxNewsAlerts))
	alerts = append(alerts, alert)
	alerts = append(alerts, ns.Alerts...)
	if len(alerts) > MaxNewsAlerts {
		alerts = alerts[:MaxNewsAlerts]
	}
	ns.Alerts = alerts
	acc.News = ns
}

// AddNewsAlert records alert on targetAid's account and saves it. Safe no-op for unknown, invalid,
// synthetic, or self-targeted alerts. Must never be called while holding AccountsMu, SocialMu,
// CirclesMu, or lpn room locks.
func AddNewsAlert(targetAid string, alert NewsAlert) {
	if targetAid == "" || targetAid == "0" || targetAid == FriendAID || (alert.Actor != "" && alert.Actor == targetAid) {
		return
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := AccountByAidLocked(targetAid)
	if acc == nil {
		return
	}
	prev := *acc
	AppendNewsAlertLocked(acc, alert)
	if err := SaveLocked(nil, acc); err != nil {
		*acc = prev
	}
}

// MarkNewsReadLocked advances NewsReadAt ("NEWS") or AlertReadAt ("ALERT") on a cloned NewsState
// when atMs > previous value. Caller holds AccountsMu.
func MarkNewsReadLocked(acc *Account, which string, atMs int64) bool {
	if acc == nil || atMs <= 0 {
		return false
	}
	switch which {
	case "NEWS":
		if acc.News != nil && atMs <= acc.News.NewsReadAt {
			return false
		}
		ns := CloneNewsState(acc.News)
		if ns == nil {
			ns = &NewsState{}
		}
		ns.NewsReadAt = atMs
		acc.News = ns
		return true
	case "ALERT":
		if acc.News != nil && atMs <= acc.News.AlertReadAt {
			return false
		}
		ns := CloneNewsState(acc.News)
		if ns == nil {
			ns = &NewsState{}
		}
		ns.AlertReadAt = atMs
		acc.News = ns
		return true
	default:
		return false
	}
}

// MarkBannerReadLocked records bannerID in ReadBanners on a cloned NewsState if not already present,
// capping ReadBanners at MaxReadBanners by dropping the oldest IDs. Caller holds AccountsMu.
func MarkBannerReadLocked(acc *Account, bannerID int64) bool {
	if acc == nil || bannerID <= 0 {
		return false
	}
	if acc.News != nil && slices.Contains(acc.News.ReadBanners, bannerID) {
		return false
	}
	ns := CloneNewsState(acc.News)
	if ns == nil {
		ns = &NewsState{}
	}
	rb := append(ns.ReadBanners, bannerID)
	if len(rb) > MaxReadBanners {
		rb = rb[len(rb)-MaxReadBanners:]
	}
	ns.ReadBanners = rb
	acc.News = ns
	return true
}
