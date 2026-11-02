package economy

import (
	"errors"
	"fmt"
	"log"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Riding Pet Race, solo (Economy Phase DE). A race needs an owned Riding Pet (the URIDE pets of the
// Minipet Shop). An account has max_tickets races a game day (06:00 JST, like the boss tries) and a race
// takes one at its start. A race that reaches the finish pays once: Gems through the ledger (capped per
// game day), racing store points, exp (the level) and MMR (the grade). A race the time runs out on pays
// nothing. Wire notes: %TEMP%\opencode\cherry\phaseDE-20261022\racing-spec.md.

// Racing is data/racing.json. Every value is Cherry-made, except that the track coordinates are unverified.
//
//	title                  the game's title (lobby and game list)
//	max_tickets            races a game day
//	roster_delay_ms        the waiting-room roster is pushed this long after the world loads; at least 1000
//	                       (a push during the scene load crashes the client)
//	countdown_sec          the start countdown the client shows, and the wait before the race starts (1 to 10)
//	max_race_sec           the race time the client shows; the race ends unranked when it runs out (10 to 600)
//	finish_countdown_sec   seconds from arrival to the result (1 to 60)
//	pos_key_count          position keys sent at the start (at least 256)
//	pet_stat               the Riding Pet's power, speed, stamina and item_slot (0 to 1000 each)
//	track                  start and finish areas in world tiles; the hero has arrived when its tile x reaches
//	                       (finish.begin_x + finish.end_x) / 2. Unverified: the petrace track is not read yet.
//	exp_per_level          exp for one level: level = 1 + exp / exp_per_level (at least 1)
//	grades                 MMR grades {name, mmr_min}, ascending, the first mmr_min 0
//	reward                 gems per arrival, gems_daily_cap Gems a game day from arrivals, store_points,
//	                       exp and mmr per arrival (all 0 or more)
type Racing struct {
	opdata.Provenance
	Title              string        `json:"title"`
	MaxTickets         int64         `json:"max_tickets"`
	RosterDelayMs      int64         `json:"roster_delay_ms"`
	CountdownSec       int64         `json:"countdown_sec"`
	MaxRaceSec         int64         `json:"max_race_sec"`
	FinishCountdownSec int64         `json:"finish_countdown_sec"`
	PosKeyCount        int           `json:"pos_key_count"`
	PetStat            RacingPetStat `json:"pet_stat"`
	Track              RacingTrack   `json:"track"`
	ExpPerLevel        int64         `json:"exp_per_level"`
	Grades             []RacingGrade `json:"grades"`
	Reward             RacingReward  `json:"reward"`
}

// RacingPetStat is the stat block of the Riding Pet in a race.
type RacingPetStat struct {
	Power    int64 `json:"power"`
	Speed    int64 `json:"speed"`
	Stamina  int64 `json:"stamina"`
	ItemSlot int64 `json:"item_slot"`
}

// RacingTrack is the start and the finish area, in world tiles.
type RacingTrack struct {
	Start  RacingArea `json:"start"`
	Finish RacingArea `json:"finish"`
}

// RacingArea is one MapArea: a begin and an end tile on each axis.
type RacingArea struct {
	BeginX int64 `json:"begin_x"`
	BeginY int64 `json:"begin_y"`
	EndX   int64 `json:"end_x"`
	EndY   int64 `json:"end_y"`
}

// RacingGrade is one MMR grade; a player's grade is the last one whose mmr_min is at or below their MMR.
type RacingGrade struct {
	Name   string `json:"name"`
	MmrMin int64  `json:"mmr_min"`
}

// RacingReward is what one arrival pays.
type RacingReward struct {
	Gems         int64 `json:"gems"`
	GemsDailyCap int64 `json:"gems_daily_cap"`
	StorePoints  int64 `json:"store_points"`
	Exp          int64 `json:"exp"`
	Mmr          int64 `json:"mmr"`
}

var racingFile = opdata.New("racing.json", data.RacingJSON, checkRacing, nil)

// ActiveRacing is the racing file in use.
func ActiveRacing() *Racing { return racingFile.Get() }

func checkRacing(r *Racing) error {
	if err := r.CheckOrigin("cherry"); err != nil {
		return err
	}
	area := func(name string, a RacingArea) error {
		if a.BeginX < 0 || a.BeginY < 0 || a.EndX < a.BeginX || a.EndY < a.BeginY {
			return fmt.Errorf("track %s: begin must not be past end, and no coordinate negative", name)
		}
		return nil
	}
	switch {
	case r.Title == "":
		return errors.New("title must not be empty")
	case r.MaxTickets < 1:
		return errors.New("max_tickets must be at least 1")
	case r.RosterDelayMs < 1000:
		return errors.New("roster_delay_ms must be at least 1000 (a push during the scene load crashes the client)")
	case r.CountdownSec < 1 || r.CountdownSec > 10:
		return errors.New("countdown_sec must be 1 to 10")
	case r.MaxRaceSec < 10 || r.MaxRaceSec > 600:
		return errors.New("max_race_sec must be 10 to 600")
	case r.FinishCountdownSec < 1 || r.FinishCountdownSec > 60:
		return errors.New("finish_countdown_sec must be 1 to 60")
	case r.PosKeyCount < 256 || r.PosKeyCount > 100000:
		return errors.New("pos_key_count must be 256 to 100000")
	case r.ExpPerLevel < 1:
		return errors.New("exp_per_level must be at least 1")
	}
	if s := r.PetStat; min(s.Power, s.Speed, s.Stamina, s.ItemSlot) < 0 || max(s.Power, s.Speed, s.Stamina, s.ItemSlot) > 1000 {
		return errors.New("pet_stat values must be 0 to 1000")
	}
	if err := area("start", r.Track.Start); err != nil {
		return err
	}
	if err := area("finish", r.Track.Finish); err != nil {
		return err
	}
	if len(r.Grades) == 0 || r.Grades[0].MmrMin != 0 {
		return errors.New("grades must list at least one grade, and the first mmr_min must be 0")
	}
	for i, g := range r.Grades {
		if g.Name == "" || (i > 0 && g.MmrMin <= r.Grades[i-1].MmrMin) {
			return fmt.Errorf("grades[%d]: name must be set and mmr_min must rise from the grade before", i+1)
		}
	}
	rw := r.Reward
	if min(rw.Gems, rw.GemsDailyCap, rw.StorePoints, rw.Exp, rw.Mmr) < 0 {
		return errors.New("reward amounts must not be negative")
	}
	return nil
}

// RaceStatus is one account's racing state at a moment: the tickets left today and the seconds until the
// next game day, the level and its exp, the MMR and grade, and the store points.
type RaceStatus struct {
	Tickets     int64
	MaxTickets  int64
	ResetSec    int64
	Level       int64
	LevelExp    int64
	LevelExpMax int64
	Mmr         int64
	Grade       int64
	GradeName   string
	Points      int64
}

// RaceOutcome is one settled race: the state before and after it, and the Gems it paid.
type RaceOutcome struct {
	Before, After RaceStatus
	Gems          int64
}

// raceDay is acc's record as of day: a record from another day keeps the career totals and starts the
// day's tickets and Gems from zero.
func raceDay(acc *store.Account, day int64) store.RacingRecord {
	var rec store.RacingRecord
	if acc.Racing != nil {
		rec = *acc.Racing
	}
	if rec.Day != day {
		rec.Day, rec.Used, rec.GemsDay = day, 0, 0
	}
	return rec
}

// raceStatus is acc's racing state at now.
func raceStatus(acc *store.Account, now time.Time) RaceStatus {
	r := ActiveRacing()
	day := AttendDayKey(now)
	rec := raceDay(acc, day)
	st := RaceStatus{
		Tickets:     max(r.MaxTickets-rec.Used, 0),
		MaxTickets:  r.MaxTickets,
		ResetSec:    int64(keyTime(day+1).Sub(now) / time.Second),
		Level:       1 + rec.Exp/r.ExpPerLevel,
		LevelExp:    rec.Exp % r.ExpPerLevel,
		LevelExpMax: r.ExpPerLevel,
		Mmr:         rec.Mmr,
		Points:      rec.Points,
	}
	for i, g := range r.Grades {
		if rec.Mmr >= g.MmrMin {
			st.Grade, st.GradeName = int64(i), g.Name
		}
	}
	return st
}

// RaceStatusOf is aid's racing state at now (an unknown account has the empty state).
func RaceStatusOf(aid string, now time.Time) RaceStatus {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		acc = &store.Account{}
	}
	return raceStatus(acc, now)
}

// RidePets lists aid's owned Riding Pets: the pets the catalog files under URIDE (a pet with an
// animation), in the account's order. A pet without art is not in the catalog and is not listed.
func RidePets(aid string) []store.PetItem {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil
	}
	cat := loadCatalog()
	var out []store.PetItem
	for _, p := range acc.Pets {
		if it := cat.byCode[p.Cd]; it != nil && it.Catg == "URIDE" {
			out = append(out, p)
		}
	}
	return out
}

// RaceStart spends one of aid's tickets for the game day of now. left is the tickets left after the
// call; ok is false when none was left or the save failed (nothing changed).
func RaceStart(aid string, now time.Time) (left int64, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	limit := ActiveRacing().MaxTickets
	rec := raceDay(acc, AttendDayKey(now))
	if rec.Used >= limit {
		return 0, false
	}
	before := rec.Used
	rec.Used++
	prev := *acc
	acc.Racing = &rec
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square racing ticket save failed aid=%s err=%v", aid, err)
		return limit - before, false
	}
	return limit - rec.Used, true
}

// RaceFinish settles aid's race at now. An arrival pays the reward once: Gems up to the game day's cap
// through the ledger, plus the store points, exp and MMR, all saved together. Any other end (arrived is
// false: the time ran out) pays nothing and only reports the state. ok is false when the account is
// unknown, or the save failed and nothing was paid.
func RaceFinish(aid string, arrived bool, now time.Time) (out RaceOutcome, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return RaceOutcome{}, false
	}
	before := raceStatus(acc, now)
	if !arrived {
		return RaceOutcome{Before: before, After: before}, true
	}
	r := ActiveRacing()
	day := AttendDayKey(now)
	rec := raceDay(acc, day)
	gems := min(r.Reward.Gems, max(r.Reward.GemsDailyCap-rec.GemsDay, 0))
	rec.GemsDay += gems
	rec.Exp += r.Reward.Exp
	rec.Mmr += r.Reward.Mmr
	rec.Points += r.Reward.StorePoints
	prev := *acc
	var lines []store.LedgerLine
	if gems > 0 {
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: gems}, "racing:"+questDate(day))
		if err != nil {
			*acc = prev
			log.Printf("cherry: square racing reward failed aid=%s err=%v", aid, err)
			return RaceOutcome{}, false
		}
	}
	acc.Racing = &rec
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square racing reward save failed aid=%s err=%v", aid, err)
		return RaceOutcome{}, false
	}
	store.FlushLedgerLocked(lines)
	return RaceOutcome{Before: before, After: raceStatus(acc, now), Gems: gems}, true
}
