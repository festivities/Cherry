package economy

import (
	"log"
	"maps"
	"slices"
	"strconv"
	"time"

	"cherry/internal/store"
)

const (
	questMaxActiveRequests = 3
	questErrCallInProgress = 199428 // AST 12130: "This quest cannot be accepted while another is in progress."
)

// QuestRow is the normalized view of a daily Task, Request, or Episode chapter.
type QuestRow struct {
	ID       int64
	Name     string
	Desc     string
	Icon     string
	NPCIcon  string
	Gems     int64
	Cash     int64
	Status   uint64 // Requests/Episodes: 1 = not accepted, 2 = accepted / in progress, 3 = ready to complete, 0 = locked; Tasks: 1 = in progress, 2 = claimed, 3 = claimable
	Current  uint64
	Complete uint64
}

// EpisodeRow extends QuestRow with optional locked-chapter prerequisites.
type EpisodeRow struct {
	QuestRow
	Locked      bool
	PreReqTitle string
	PreReqIcon  string
	PreReqCur   uint64
	PreReqMax   uint64
}

func tabQuestStatus(cur, goal int64, accepted bool) uint64 {
	if cur >= goal {
		return 3
	}
	if accepted {
		return 2
	}
	return 1
}

// RequestListView is the player's Requests tab state for the current game day.
type RequestListView struct {
	Rows          []QuestRow
	TodayDone     int64
	MaxDaily      int64
	FreeCallsLeft int64
	MaxFreeCalls  int64
	CallGems      int64
	CanInvite     bool
}

func cloneEpisodeState(e *store.EpisodeState) *store.EpisodeState {
	if e == nil {
		return &store.EpisodeState{}
	}
	return &store.EpisodeState{
		Accepted: slices.Clone(e.Accepted),
		Progress: maps.Clone(e.Progress),
		Claimed:  slices.Clone(e.Claimed),
	}
}

func repeatQuestByID(reqs []RepeatQuestDef, id int64) (RepeatQuestDef, bool) {
	for _, r := range reqs {
		if r.ID == id {
			return r, true
		}
	}
	return RepeatQuestDef{}, false
}

func effectiveGoal(goal int64) int64 {
	if goal <= 0 {
		return 1
	}
	return goal
}

// ensureRequestsSeededLocked populates the initial batch of up to 3 Requests if not yet seeded today.
func ensureRequestsSeededLocked(rec *store.QuestDay, rules RequestRules, reqs []RepeatQuestDef) bool {
	if rec.ReqSeeded || rec.ReqClaimed >= rules.MaxDaily || len(reqs) == 0 {
		return false
	}
	rec.ReqSeeded = true
	for _, r := range reqs {
		if len(rec.ActiveReqs) >= questMaxActiveRequests {
			break
		}
		if !slices.Contains(rec.ActiveReqs, r.ID) {
			rec.ActiveReqs = append(rec.ActiveReqs, r.ID)
		}
	}
	return true
}

func markRequestProgressLocked(rec *store.QuestDay, key string) bool {
	rules := ActiveRequestRules()
	reqs := ActiveRewards().Requests
	changed := ensureRequestsSeededLocked(rec, rules, reqs)
	if rec.ReqClaimed >= rules.MaxDaily {
		return changed
	}
	for _, id := range rec.ActiveReqs {
		rdef, ok := repeatQuestByID(reqs, id)
		if !ok || rdef.Key != key {
			continue
		}
		goal := effectiveGoal(rdef.Goal)
		if rec.ReqProgress[id] < goal {
			if rec.ReqProgress == nil {
				rec.ReqProgress = map[int64]int64{}
			}
			rec.ReqProgress[id]++
			changed = true
		}
	}
	return changed
}

func currentEpisodeLocked(eps []EpisodeQuestDef, epState *store.EpisodeState) (int, EpisodeQuestDef, bool) {
	var claimed []int64
	if epState != nil {
		claimed = epState.Claimed
	}
	for i, ch := range eps {
		if !slices.Contains(claimed, ch.ID) {
			return i, ch, true
		}
	}
	return -1, EpisodeQuestDef{}, false
}

func markEpisodeProgressLocked(ep *store.EpisodeState, key string) (*store.EpisodeState, bool) {
	eps := ActiveRewards().Episodes
	_, ch, ok := currentEpisodeLocked(eps, ep)
	if !ok || ch.Key != key {
		return nil, false
	}
	goal := effectiveGoal(ch.Goal)
	var cur int64
	if ep != nil && ep.Progress != nil {
		cur = ep.Progress[ch.ID]
	}
	if cur >= goal {
		return nil, false
	}
	next := cloneEpisodeState(ep)
	if next.Progress == nil {
		next.Progress = map[int64]int64{}
	}
	next.Progress[ch.ID] = cur + 1
	return next, true
}

func availableRequests(reqs []RepeatQuestDef, active []int64) []RepeatQuestDef {
	var out []RepeatQuestDef
	for _, r := range reqs {
		if !slices.Contains(active, r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// nextCyclingRequest returns the first Request not in active, starting right after afterID and wrapping.
func nextCyclingRequest(reqs []RepeatQuestDef, active []int64, afterID int64) (int64, bool) {
	if len(reqs) == 0 {
		return 0, false
	}
	start := 0
	for i, r := range reqs {
		if r.ID == afterID {
			start = (i + 1) % len(reqs)
			break
		}
	}
	for off := range len(reqs) {
		cand := reqs[(start+off)%len(reqs)]
		if !slices.Contains(active, cand.ID) {
			return cand.ID, true
		}
	}
	return 0, false
}

// RequestList returns aid's active Requests view for now, seeding the initial batch of up to 3
// Requests once per game day if not yet seeded.
func RequestList(aid string, now time.Time) RequestListView {
	rules := ActiveRequestRules()
	reqs := ActiveRewards().Requests
	view := RequestListView{
		MaxDaily:      rules.MaxDaily,
		FreeCallsLeft: rules.FreeCalls,
		MaxFreeCalls:  rules.FreeCalls,
		CallGems:      rules.CallGems,
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return view
	}
	day := AttendDayKey(now)
	rec := cloneQuestDay(questRecord(acc, day))
	if ensureRequestsSeededLocked(&rec, rules, reqs) {
		prev := *acc
		acc.Quests = &rec
		if err := store.SaveLocked(nil, acc); err != nil {
			*acc = prev
			log.Printf("cherry: garden-quest seed save failed aid=%s err=%v", aid, err)
			return view
		}
	}
	for _, id := range rec.ActiveReqs {
		rdef, ok := repeatQuestByID(reqs, id)
		if !ok {
			continue
		}
		goal := effectiveGoal(rdef.Goal)
		cur := min(rec.ReqProgress[id], goal)
		status := tabQuestStatus(cur, goal, slices.Contains(rec.AcceptedReqs, id))
		view.Rows = append(view.Rows, QuestRow{
			ID:       rdef.ID,
			Name:     rdef.Name,
			Desc:     rdef.Desc,
			Icon:     resolveQuestIcon(rdef.Icon, rdef.Key, false),
			NPCIcon:  resolveQuestNPC(rdef.NPCIcon, rdef.ID),
			Gems:     rdef.Gems,
			Cash:     rdef.Cash,
			Status:   status,
			Current:  uint64(cur),
			Complete: uint64(goal),
		})
	}
	view.TodayDone = rec.ReqClaimed
	view.FreeCallsLeft = max(0, rules.FreeCalls-rec.ReqFreeCalls)
	avail := availableRequests(reqs, rec.ActiveReqs)
	view.CanInvite = rec.ReqClaimed < rules.MaxDaily && len(rec.ActiveReqs) < questMaxActiveRequests && len(avail) > 0
	return view
}

// AcceptQuest marks id accepted if it is an active Request or the current unlocked Episode.
// Returns 0 on success or 1 if unknown/unavailable.
func AcceptQuest(aid string, id int64, now time.Time) int {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return questErrRejected
	}
	day := AttendDayKey(now)
	rec := cloneQuestDay(questRecord(acc, day))
	prev := *acc
	rules := ActiveRequestRules()
	reqs := ActiveRewards().Requests
	seeded := ensureRequestsSeededLocked(&rec, rules, reqs)

	if slices.Contains(rec.ActiveReqs, id) {
		if !slices.Contains(rec.AcceptedReqs, id) {
			rec.AcceptedReqs = append(rec.AcceptedReqs, id)
			acc.Quests = &rec
			if err := store.SaveLocked(nil, acc); err != nil {
				*acc = prev
				return questErrRejected
			}
			return 0
		}
		if seeded {
			acc.Quests = &rec
			if err := store.SaveLocked(nil, acc); err != nil {
				*acc = prev
				return questErrRejected
			}
		}
		return 0
	}

	eps := ActiveRewards().Episodes
	_, ch, ok := currentEpisodeLocked(eps, acc.Episodes)
	if ok && ch.ID == id {
		epState := cloneEpisodeState(acc.Episodes)
		changed := seeded
		if seeded {
			acc.Quests = &rec
		}
		if !slices.Contains(epState.Accepted, id) {
			epState.Accepted = append(epState.Accepted, id)
			acc.Episodes = epState
			changed = true
		}
		if changed {
			if err := store.SaveLocked(nil, acc); err != nil {
				*acc = prev
				return questErrRejected
			}
		}
		return 0
	}

	return questErrRejected
}

// RejectQuest removes id from ActiveReqs (and clears its AcceptedReqs/ReqProgress) if it is an active
// Request. Returns 0 on success or 1 if not an active Request (Episodes cannot be declined).
func RejectQuest(aid string, id int64, now time.Time) int {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return questErrRejected
	}
	day := AttendDayKey(now)
	rec := cloneQuestDay(questRecord(acc, day))
	prev := *acc
	ensureRequestsSeededLocked(&rec, ActiveRequestRules(), ActiveRewards().Requests)
	if !slices.Contains(rec.ActiveReqs, id) {
		return questErrRejected
	}
	rec.ActiveReqs = slices.DeleteFunc(rec.ActiveReqs, func(v int64) bool { return v == id })
	rec.AcceptedReqs = slices.DeleteFunc(rec.AcceptedReqs, func(v int64) bool { return v == id })
	delete(rec.ReqProgress, id)
	acc.Quests = &rec
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		return questErrRejected
	}
	return 0
}

// claimTabQuestLocked handles ClaimQuestReward for active Requests and the current unlocked Episode.
// Caller holds AccountsMu.
func claimTabQuestLocked(acc *store.Account, prev store.Account, rec *store.QuestDay, day, id int64) (gems, cash int64, code int) {
	rules := ActiveRequestRules()
	reqs := ActiveRewards().Requests
	ensureRequestsSeededLocked(rec, rules, reqs)

	if slices.Contains(rec.ActiveReqs, id) {
		rdef, ok := repeatQuestByID(reqs, id)
		if !ok {
			return 0, 0, questErrRejected
		}
		goal := effectiveGoal(rdef.Goal)
		if rec.ReqProgress[id] < goal || rec.ReqClaimed >= rules.MaxDaily {
			return 0, 0, questErrRejected
		}
		reason := "quest:req:" + strconv.FormatInt(id, 10) + ":" + questDate(day)
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: rdef.Gems, Cash: rdef.Cash}, reason)
		if err != nil {
			*acc = prev
			return 0, 0, questErrRejected
		}
		rec.ReqClaimed++
		rec.ActiveReqs = slices.DeleteFunc(rec.ActiveReqs, func(v int64) bool { return v == id })
		rec.AcceptedReqs = slices.DeleteFunc(rec.AcceptedReqs, func(v int64) bool { return v == id })
		delete(rec.ReqProgress, id)
		if rec.ReqClaimed < rules.MaxDaily && len(rec.ActiveReqs) < questMaxActiveRequests {
			if nextID, ok := nextCyclingRequest(reqs, rec.ActiveReqs, id); ok {
				rec.ActiveReqs = append(rec.ActiveReqs, nextID)
			}
		}
		acc.Quests = rec
		if err := store.SaveLocked(lines, acc); err != nil {
			*acc = prev
			log.Printf("cherry: garden-quest req claim save failed aid=%s id=%d err=%v", acc.Aid, id, err)
			return 0, 0, questErrRejected
		}
		return rdef.Gems, rdef.Cash, 0
	}

	eps := ActiveRewards().Episodes
	_, ch, ok := currentEpisodeLocked(eps, acc.Episodes)
	if ok && ch.ID == id {
		epState := cloneEpisodeState(acc.Episodes)
		goal := effectiveGoal(ch.Goal)
		if epState.Progress[id] < goal {
			return 0, 0, questErrRejected
		}
		reason := "quest:ep:" + strconv.FormatInt(id, 10)
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: ch.Gems, Cash: ch.Cash}, reason)
		if err != nil {
			*acc = prev
			return 0, 0, questErrRejected
		}
		epState.Claimed = append(epState.Claimed, id)
		epState.Accepted = slices.DeleteFunc(epState.Accepted, func(v int64) bool { return v == id })
		delete(epState.Progress, id)
		acc.Episodes = epState
		if err := store.SaveLocked(lines, acc); err != nil {
			*acc = prev
			log.Printf("cherry: garden-quest ep claim save failed aid=%s id=%d err=%v", acc.Aid, id, err)
			return 0, 0, questErrRejected
		}
		return ch.Gems, ch.Cash, 0
	}

	return 0, 0, questErrRejected
}

// PickNPCRequests returns Requests not currently in ActiveReqs (empty if ReqClaimed >= MaxDaily).
// If refresh is true and RefreshGems > 0, it charges RefreshGems.
func PickNPCRequests(aid string, refresh bool, now time.Time) (rows []QuestRow, refreshGems int64, code int) {
	rules := ActiveRequestRules()
	reqs := ActiveRewards().Requests
	refreshGems = rules.RefreshGems

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil, refreshGems, questErrRejected
	}
	day := AttendDayKey(now)
	rec := cloneQuestDay(questRecord(acc, day))
	prev := *acc
	seeded := ensureRequestsSeededLocked(&rec, rules, reqs)

	var lines []store.LedgerLine
	spent := false
	if refresh && rules.RefreshGems > 0 && rec.ReqClaimed < rules.MaxDaily {
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: -rules.RefreshGems}, "quest:refresh")
		if err != nil {
			*acc = prev
			return nil, refreshGems, questErrRejected
		}
		spent = true
	}
	if seeded || spent {
		acc.Quests = &rec
		if err := store.SaveLocked(lines, acc); err != nil {
			*acc = prev
			return nil, refreshGems, questErrRejected
		}
	}
	if rec.ReqClaimed >= rules.MaxDaily {
		return nil, refreshGems, 0
	}
	for _, r := range availableRequests(reqs, rec.ActiveReqs) {
		rows = append(rows, QuestRow{
			ID:       r.ID,
			Name:     r.Name,
			Desc:     r.Desc,
			Icon:     resolveQuestIcon(r.Icon, r.Key, false),
			NPCIcon:  resolveQuestNPC(r.NPCIcon, r.ID),
			Gems:     r.Gems,
			Cash:     r.Cash,
			Status:   1,
			Current:  0,
			Complete: uint64(effectiveGoal(r.Goal)),
		})
	}
	return rows, refreshGems, 0
}

// CallNPCRequest invites Neighbor Request id into ActiveReqs, using a free call when !paid and
// free calls remain, or charging CallGems otherwise. Returns 0 on success, questErrCallInProgress
// (199428) if id is already active or 3 requests are already active, or 1 on other errors.
func CallNPCRequest(aid string, id int64, paid bool, now time.Time) int {
	rules := ActiveRequestRules()
	reqs := ActiveRewards().Requests
	if _, ok := repeatQuestByID(reqs, id); !ok {
		return questErrRejected
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return questErrRejected
	}
	day := AttendDayKey(now)
	rec := cloneQuestDay(questRecord(acc, day))
	prev := *acc
	ensureRequestsSeededLocked(&rec, rules, reqs)
	if rec.ReqClaimed >= rules.MaxDaily {
		return questErrRejected
	}
	if slices.Contains(rec.ActiveReqs, id) || len(rec.ActiveReqs) >= questMaxActiveRequests {
		return questErrCallInProgress
	}

	freeLeft := max(0, rules.FreeCalls-rec.ReqFreeCalls)
	var lines []store.LedgerLine
	if !paid && freeLeft > 0 {
		rec.ReqFreeCalls++
	} else if rules.CallGems > 0 {
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: -rules.CallGems}, "quest:call:"+strconv.FormatInt(id, 10))
		if err != nil {
			*acc = prev
			return questErrRejected
		}
	}
	rec.ActiveReqs = append(rec.ActiveReqs, id)
	acc.Quests = &rec
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		return questErrRejected
	}
	return 0
}

// EpisodeList returns the current unlocked Episode chapter and, if another chapter follows, the
// locked preview of the next chapter. Returns an empty slice once all chapters are claimed.
func EpisodeList(aid string) []EpisodeRow {
	eps := ActiveRewards().Episodes
	if len(eps) == 0 {
		return nil
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil
	}
	idx, ch, ok := currentEpisodeLocked(eps, acc.Episodes)
	if !ok {
		return nil
	}
	goal := effectiveGoal(ch.Goal)
	var cur int64
	if acc.Episodes != nil && acc.Episodes.Progress != nil {
		cur = min(acc.Episodes.Progress[ch.ID], goal)
	}
	accepted := acc.Episodes != nil && slices.Contains(acc.Episodes.Accepted, ch.ID)
	status := tabQuestStatus(cur, goal, accepted)
	out := []EpisodeRow{{
		QuestRow: QuestRow{
			ID:       ch.ID,
			Name:     ch.Name,
			Desc:     ch.Desc,
			Icon:     resolveQuestIcon(ch.Icon, ch.Key, false),
			NPCIcon:  resolveQuestNPC(ch.NPCIcon, ch.ID),
			Gems:     ch.Gems,
			Cash:     ch.Cash,
			Status:   status,
			Current:  uint64(cur),
			Complete: uint64(goal),
		},
	}}
	if idx+1 < len(eps) {
		next := eps[idx+1]
		out = append(out, EpisodeRow{
			QuestRow: QuestRow{
				ID:       next.ID,
				Name:     next.Name,
				Desc:     next.Desc,
				Icon:     resolveQuestIcon(next.Icon, next.Key, false),
				NPCIcon:  resolveQuestNPC(next.NPCIcon, next.ID),
				Gems:     next.Gems,
				Cash:     next.Cash,
				Status:   0,
				Current:  0,
				Complete: uint64(effectiveGoal(next.Goal)),
			},
			Locked:      true,
			PreReqTitle: "Clear " + ch.Name,
			PreReqIcon:  questArtFallbackIcon,
			PreReqCur:   0,
			PreReqMax:   1,
		})
	}
	return out
}

// QuestDetailByID looks up id across daily Tasks/event quests, Requests (active or available to
// invite), and Episodes (current unlocked or locked next chapter).
func QuestDetailByID(aid string, id int64, now time.Time) (EpisodeRow, bool) {
	if def, ok := QuestByID(id, now); ok {
		if def.Display {
			sent, _ := HeartsSentToday(aid, now)
			goal := HeartQuestCount()
			cur := min(sent, goal)
			status := uint64(1)
			if sent >= goal {
				status = 2
			}
			return EpisodeRow{
				QuestRow: QuestRow{
					ID:       def.ID,
					Name:     def.Name,
					Desc:     def.Desc,
					Icon:     QuestIcon(def),
					Gems:     def.Gems,
					Cash:     def.Cash,
					Status:   status,
					Current:  uint64(cur),
					Complete: uint64(goal),
				},
			}, true
		}
		rec := QuestProgress(aid, now)
		status, cur := uint64(1), uint64(0)
		if slices.Contains(rec.Done, def.ID) {
			status, cur = 3, 1
		}
		if slices.Contains(rec.Claimed, def.ID) {
			status, cur = 2, 1
		}
		return EpisodeRow{
			QuestRow: QuestRow{
				ID:       def.ID,
				Name:     def.Name,
				Desc:     def.Desc,
				Icon:     QuestIcon(def),
				Gems:     def.Gems,
				Cash:     def.Cash,
				Status:   status,
				Current:  cur,
				Complete: 1,
			},
		}, true
	}

	if rdef, ok := repeatQuestByID(ActiveRewards().Requests, id); ok {
		view := RequestList(aid, now)
		for _, row := range view.Rows {
			if row.ID == id {
				return EpisodeRow{QuestRow: row}, true
			}
		}
		return EpisodeRow{
			QuestRow: QuestRow{
				ID:       rdef.ID,
				Name:     rdef.Name,
				Desc:     rdef.Desc,
				Icon:     resolveQuestIcon(rdef.Icon, rdef.Key, false),
				NPCIcon:  resolveQuestNPC(rdef.NPCIcon, rdef.ID),
				Gems:     rdef.Gems,
				Cash:     rdef.Cash,
				Status:   1,
				Current:  0,
				Complete: uint64(effectiveGoal(rdef.Goal)),
			},
		}, true
	}

	for _, ep := range EpisodeList(aid) {
		if ep.ID == id {
			return ep, true
		}
	}
	return EpisodeRow{}, false
}
