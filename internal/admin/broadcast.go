package admin

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/auth"
	"cherry/internal/store"
)

// Online announcement broadcast (owner only): pushes either a non-blocking top notification banner
// (catg "postbox", pType "PBO") or a blocking maintenance popup (catg "maint", displayType "POPUP")
// to every currently connected :10123 session, then writes an audit row.

const (
	broadcastKindBanner  = "banner"
	broadcastKindMaint   = "maint"
	broadcastMessageMax  = 400
	broadcastReasonMax   = 200
	broadcastDurationMin = 1
	broadcastDurationMax = 1440
	broadcastRecentLimit = 20
)

type broadcastForm struct {
	Kind, Message, Duration, Reason string
}

type broadcastRow struct {
	When, By, Action, Detail string
}

type broadcastPage struct {
	OnlineSessions int
	Form           broadcastForm
	Recent         []broadcastRow
}

func (s *Server) broadcastOwner(w http.ResponseWriter, sess session) bool {
	if !s.isOwner(sess) {
		s.logger.Printf("cherry: broadcast refused for a non-owner")
		s.fail(w, http.StatusForbidden, "Only the owner can send a broadcast announcement.")
		return false
	}
	return true
}

func recentBroadcasts() ([]broadcastRow, error) {
	banners, err := store.AuditSearch(store.AuditFilter{Action: "broadcast-banner"}, 0, broadcastRecentLimit)
	if err != nil {
		return nil, err
	}
	maints, err := store.AuditSearch(store.AuditFilter{Action: "broadcast-maint"}, 0, broadcastRecentLimit)
	if err != nil {
		return nil, err
	}
	combined := make([]store.AuditRow, 0, len(banners)+len(maints))
	combined = append(combined, banners...)
	combined = append(combined, maints...)
	slices.SortFunc(combined, func(a, b store.AuditRow) int {
		return cmp.Compare(b.ID, a.ID)
	})
	if len(combined) > broadcastRecentLimit {
		combined = combined[:broadcastRecentLimit]
	}
	out := make([]broadcastRow, 0, len(combined))
	for _, r := range combined {
		out = append(out, broadcastRow{
			When:   r.Time,
			By:     auth.MaskIdentity(r.Actor),
			Action: r.Action,
			Detail: r.Detail,
		})
	}
	return out, nil
}

// handleBroadcast: GET /broadcast (owner only).
func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.broadcastOwner(w, sess) {
		return
	}
	recent, err := recentBroadcasts()
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Broadcast announcement")
	v.Broadcast = &broadcastPage{
		OnlineSessions: s.sessionCount(),
		Form:           broadcastForm{Kind: broadcastKindBanner, Duration: "5"},
		Recent:         recent,
	}
	s.render(w, http.StatusOK, "broadcast", v)
}

// handleBroadcastSend: POST /broadcast/send (privileged, owner only).
func (s *Server) handleBroadcastSend(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.broadcastOwner(w, sess) {
		return
	}
	kind := r.PostFormValue("kind")
	msg := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("message"), "\r\n", "\n"))
	durText := strings.TrimSpace(r.PostFormValue("duration"))
	reason := strings.TrimSpace(r.PostFormValue("reason"))

	if kind != broadcastKindBanner && kind != broadcastKindMaint {
		s.fail(w, http.StatusBadRequest, "Choose either the in-game banner or the maintenance popup.")
		return
	}
	if n := utf8.RuneCountInString(msg); n < 1 || n > broadcastMessageMax {
		s.fail(w, http.StatusBadRequest, fmt.Sprintf("The message is 1 to %d characters.", broadcastMessageMax))
		return
	}
	if utf8.RuneCountInString(reason) > broadcastReasonMax { // optional (owner, 2026-11-28)
		s.fail(w, http.StatusBadRequest, fmt.Sprintf("The reason is at most %d characters.", broadcastReasonMax))
		return
	}

	var (
		sent, failed int
		action       string
		detail       map[string]any
	)
	if kind == broadcastKindBanner {
		sent, failed = s.broadcastBanner(msg)
		action = "broadcast-banner"
		detail = map[string]any{
			"kind":    kind,
			"sent":    sent,
			"failed":  failed,
			"message": msg,
			"reason":  reason,
		}
	} else {
		mins, err := strconv.Atoi(durText)
		if err != nil || mins < broadcastDurationMin || mins > broadcastDurationMax {
			s.fail(w, http.StatusBadRequest, fmt.Sprintf("The duration is a whole number of minutes from %d to %d.", broadcastDurationMin, broadcastDurationMax))
			return
		}
		sent, failed = s.broadcastMaint(msg, time.Duration(mins)*time.Minute)
		action = "broadcast-maint"
		detail = map[string]any{
			"kind":             kind,
			"sent":             sent,
			"failed":           failed,
			"duration_minutes": mins,
			"message":          msg,
			"reason":           reason,
		}
	}

	if err := store.Audit(sess.identity, action, "online-players", detail); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/broadcast?notice=broadcast-sent")
}
