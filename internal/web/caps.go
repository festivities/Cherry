package web

import (
	"strconv"
	"time"

	"cherry/internal/config"
	"cherry/internal/economy"
	"cherry/internal/store"
)

// What one player may write and hold (PLAN 8.6b part 5). The per-day and total caps apply in identity modes only: the lab
// keeps every write. The stock client does not reach them in play. Each count scans the in-memory list it counts, O(n) per
// write; ponytail: per-player counters if the lists grow. Over a cap the route's own refusal answers, which is the shape the
// client already handles there. Callers hold store.SocialMu (the counts) or store.AccountsMu (the pins).
const (
	maxPostsPerDay         = 50   // diary posts, Share to Diary posts included
	maxEntriesPerDay       = 200  // guestbook entries
	maxCommentsPerDay      = 500  // diary comments
	maxLikesPerDay         = 2000 // new diary likes (changing a like's type does not count again)
	maxPendingApplications = 100  // friend applications a player has sent and not had answered
	maxFriends             = 500  // accepted friendships a player has
	maxPins                = 500  // My Pins entries
)

// capsOn reports whether the per-player caps apply.
func capsOn() bool { return config.IdentityMode() }

// onGameDay reports whether the millisecond timestamp ms falls in game day (economy.AttendDayKey, the day starts at 06:00 JST).
func onGameDay(ms string, day int64) bool {
	n, err := strconv.ParseInt(ms, 10, 64)
	return err == nil && economy.AttendDayKey(time.UnixMilli(n)) == day
}

// todayKey is the game day of the current time.
func todayKey() int64 { return economy.AttendDayKey(time.Now()) }

// postsToday counts the diary posts aid wrote on day. Caller holds SocialMu.
func postsToday(aid string, day int64) int {
	n := 0
	for _, p := range store.DiaryPosts {
		if p.AvatarID == aid && onGameDay(p.RegDate, day) {
			n++
		}
	}
	return n
}

// entriesToday counts the guestbook entries aid wrote on day. Caller holds SocialMu.
func entriesToday(aid string, day int64) int {
	n := 0
	for _, e := range store.GuestbookEntries {
		if e.GuestAvtNo == aid && onGameDay(e.RegYmdt, day) {
			n++
		}
	}
	return n
}

// commentsToday counts the diary comments aid wrote on day. Caller holds SocialMu.
func commentsToday(aid string, day int64) int {
	n := 0
	for _, c := range store.DiaryComments {
		if c.CommentAvatarID == aid && onGameDay(c.Date, day) {
			n++
		}
	}
	return n
}

// likesToday counts the diary likes aid made on day. Caller holds SocialMu.
func likesToday(aid string, day int64) int {
	n := 0
	for _, l := range store.DiaryLikes {
		if l.LikeAvatarID == aid && onGameDay(l.Date, day) {
			n++
		}
	}
	return n
}

// friendCountLocked counts aid's friendships in state; outgoing counts only the applications aid sent (state pending).
// Caller holds SocialMu.
func friendCountLocked(aid, state string, outgoing bool) int {
	n := 0
	for _, f := range store.Friendships {
		switch {
		case f.State != state:
		case outgoing && f.A == aid, !outgoing && (f.A == aid || f.B == aid):
			n++
		}
	}
	return n
}

// Protocol caps, all modes: a count the stock client never exceeds (the sizes are its own, see each comment).
const (
	sessionStatusMax     = 200 // aids in one /v4/session/status request: the client sends chunks of 200
	createAvatarMaxItems = 64  // item codes in one /v4/create/avatar: a creation names a handful of parts
	avatarSaveMaxItems   = 128 // items in one /v4/avatar/save/v2 outfit: the worn set, a few dozen at most
)

// friendsFullLocked reports whether a friend step would pass a player's limit: an accept needs room for one more friend on
// both sides, a new application room for one more outgoing application. i is the friendship's index (-1 for none). Caller
// holds SocialMu.
func friendsFullLocked(actor, other string, accept bool, i int) bool {
	if accept {
		return i >= 0 && store.Friendships[i].State == "pending" && store.Friendships[i].B == actor &&
			(friendCountLocked(actor, "accepted", false) >= maxFriends || friendCountLocked(other, "accepted", false) >= maxFriends)
	}
	return (i < 0 || store.Friendships[i].State == "removed") && friendCountLocked(actor, "pending", true) >= maxPendingApplications
}
