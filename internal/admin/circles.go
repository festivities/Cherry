package admin

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// The Circles moderation pages (/circles and /circles/{id}): browse/search circles, inspect rosters, jump to a circle's
// chat log, edit name/intro, remove non-leader members, and disband circles.

type circlesPage struct {
	Query string
	Total int
	Rows  []circleListRow
}

type circleListRow struct {
	ID         int64
	Href       string
	ChatHref   string
	Code       string
	Name       string
	Intro      string
	JoinType   string
	LeaderName string
	LeaderAid  string
	LeaderHref string
	Members    int
	MaxMembers int
	Created    string
}

type circleDetailPage struct {
	ID          int64
	Code        string
	Name        string
	Intro       string
	JoinType    string
	MemberCount int
	MaxMembers  int
	Created     string
	Modified    string
	ChatHref    string
	Members     []circleMemberRow
}

type circleMemberRow struct {
	Aid      string
	Name     string
	Href     string
	Role     string
	Joined   string
	IsLeader bool
}

func circlePath(id int64) string {
	return "/circles/" + strconv.FormatInt(id, 10)
}

func circleChatHref(id int64) string {
	return "/chat?kind=circle&channel=" + strconv.FormatInt(id, 10)
}

func (s *Server) circleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.fail(w, http.StatusBadRequest, "The circle id is not valid.")
		return 0, false
	}
	return id, true
}

func snapshotCircles() []store.Circle {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	out := slices.Clone(store.CircleState.Circles)
	for i := range out {
		out[i].Members = slices.Clone(out[i].Members)
	}
	return out
}

func snapshotCircle(id int64) (store.Circle, bool) {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	for _, c := range store.CircleState.Circles {
		if c.ID == id {
			c.Members = slices.Clone(c.Members)
			return c, true
		}
	}
	return store.Circle{}, false
}

func circleMatches(c store.Circle, needle string, players map[string]store.Player) bool {
	if needle == "" {
		return true
	}
	if strings.Contains(strconv.FormatInt(c.ID, 10), needle) ||
		strings.Contains(strings.ToLower(c.Code), needle) ||
		strings.Contains(strings.ToLower(c.Name), needle) ||
		strings.Contains(strings.ToLower(c.Intro), needle) {
		return true
	}
	for _, m := range c.Members {
		if strings.Contains(strings.ToLower(m.Aid), needle) {
			return true
		}
		if p, ok := players[m.Aid]; ok && strings.Contains(strings.ToLower(p.Name), needle) {
			return true
		}
	}
	return false
}

func circleRoleRank(role string) int {
	switch role {
	case "leader":
		return 0
	case "vice":
		return 1
	default:
		return 2
	}
}

// handleCircles: GET /circles. Lists circles newest first, with optional ?q= filtering across ID, code, name, intro,
// and member AID or name.
func (s *Server) handleCircles(w http.ResponseWriter, r *http.Request, sess session) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > playerQueryMax {
		s.fail(w, http.StatusBadRequest, "The search is too long.")
		return
	}
	circles := snapshotCircles()
	var aids []string
	for _, c := range circles {
		for _, m := range c.Members {
			aids = append(aids, m.Aid)
		}
	}
	players := store.PlayersOfAids(aids)

	needle := strings.ToLower(q)
	var matched []store.Circle
	for _, c := range circles {
		if circleMatches(c, needle, players) {
			matched = append(matched, c)
		}
	}
	slices.SortFunc(matched, func(a, b store.Circle) int {
		if cmpCreated := cmp.Compare(b.Created, a.Created); cmpCreated != 0 {
			return cmpCreated
		}
		return cmp.Compare(b.ID, a.ID)
	})

	rows := make([]circleListRow, 0, len(matched))
	for _, c := range matched {
		var leaderAid, leaderName, leaderHref string
		for _, m := range c.Members {
			if m.Role == "leader" {
				leaderAid = m.Aid
				if p, ok := players[m.Aid]; ok {
					leaderName = p.Name
					leaderHref = playerPath(p.ID)
				}
				break
			}
		}
		rows = append(rows, circleListRow{
			ID:         c.ID,
			Href:       circlePath(c.ID),
			ChatHref:   circleChatHref(c.ID),
			Code:       c.Code,
			Name:       c.Name,
			Intro:      c.Intro,
			JoinType:   c.JoinType,
			LeaderName: leaderName,
			LeaderAid:  leaderAid,
			LeaderHref: leaderHref,
			Members:    len(c.Members),
			MaxMembers: economy.CircleMaxMembers(c),
			Created:    unixMsDate(c.Created),
		})
	}

	v := s.base(r, sess, "Circles")
	v.Circles = circlesPage{
		Query: q,
		Total: len(circles),
		Rows:  rows,
	}
	s.render(w, http.StatusOK, "circles", v)
}

// handleCircle: GET /circles/{id}. Shows one circle's overview, edit form, sorted roster, and disband form.
func (s *Server) handleCircle(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.circleID(w, r)
	if !ok {
		return
	}
	c, found := snapshotCircle(id)
	if !found {
		s.fail(w, http.StatusNotFound, "No such circle.")
		return
	}
	aids := make([]string, len(c.Members))
	for i, m := range c.Members {
		aids[i] = m.Aid
	}
	players := store.PlayersOfAids(aids)

	slices.SortFunc(c.Members, func(a, b store.CircleMember) int {
		if r := cmp.Compare(circleRoleRank(a.Role), circleRoleRank(b.Role)); r != 0 {
			return r
		}
		if j := cmp.Compare(a.Joined, b.Joined); j != 0 {
			return j
		}
		return cmp.Compare(a.Aid, b.Aid)
	})

	members := make([]circleMemberRow, 0, len(c.Members))
	for _, m := range c.Members {
		var name, href string
		if p, ok := players[m.Aid]; ok {
			name = p.Name
			href = playerPath(p.ID)
		}
		members = append(members, circleMemberRow{
			Aid:      m.Aid,
			Name:     name,
			Href:     href,
			Role:     m.Role,
			Joined:   unixMsDate(m.Joined),
			IsLeader: m.Role == "leader",
		})
	}

	v := s.base(r, sess, c.Name)
	v.Crumbs = []link{{Href: "/circles", Label: "Circles"}, {Label: c.Name}}
	v.Circle = circleDetailPage{
		ID:          c.ID,
		Code:        c.Code,
		Name:        c.Name,
		Intro:       c.Intro,
		JoinType:    c.JoinType,
		MemberCount: len(c.Members),
		MaxMembers:  economy.CircleMaxMembers(c),
		Created:     unixMsDate(c.Created),
		Modified:    unixMsDate(c.Modified),
		ChatHref:    circleChatHref(c.ID),
		Members:     members,
	}
	s.render(w, http.StatusOK, "circle", v)
}

// handleCircleEdit: POST /circles/{id}/edit (privileged). Form: name, intro, reason.
func (s *Server) handleCircleEdit(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.circleID(w, r)
	if !ok {
		return
	}
	if err := economy.StaffEditCircle(id, r.PostFormValue("name"), r.PostFormValue("intro"), r.PostFormValue("reason"), sess.identity, s.now()); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/circles/%d?notice=circle-edited", id))
}

// handleCircleKickMember: POST /circles/{id}/kick-member (privileged). Form: aid, reason.
func (s *Server) handleCircleKickMember(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.circleID(w, r)
	if !ok {
		return
	}
	if err := economy.StaffRemoveCircleMember(id, r.PostFormValue("aid"), r.PostFormValue("reason"), sess.identity, s.now()); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/circles/%d?notice=circle-member-removed", id))
}

// handleCircleDisband: POST /circles/{id}/disband (privileged). Form: reason.
func (s *Server) handleCircleDisband(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.circleID(w, r)
	if !ok {
		return
	}
	if err := economy.StaffDisbandCircle(id, r.PostFormValue("reason"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/circles?notice=circle-disbanded")
}
