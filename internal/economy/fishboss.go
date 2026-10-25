package economy

import (
	"errors"
	"fmt"
	"image/color"
	"log"
	"maps"
	"strconv"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Square boss fishing, solo (Economy Phase DE). An account spends one of max_tries per game day
// (06:00 JST) to start a battle. The battle is a chart of notes: each correct touch of a new, later
// note takes damage_per_hit off the boss, and a battle the boss does not survive is a catch. Every
// finished battle pays the participation reward, plus the catch reward when caught; Gems go through
// the ledger, Boss Coins sit on the account. Reset refills the tries for Gems when the data offers
// it. Wire notes: %TEMP%\opencode\cherry\phaseDE-20261022\boss-fishing-spec.md (sections 2, 5, 8, 11).

// Boss fishing is data/fishboss.json. Every value is Cherry-made (no ground truth exists).
//
//	max_tries        boss tries per account per game day
//	reset_gems       Gems that refill the tries once none are left; 0 = no reset is offered
//	damage_per_hit   HP one correct note takes off the boss
//	note_arrive_ms   a note's travel time to the hit line, at least 1000 (the client reads whole
//	                 seconds); it also spaces the notes, so 1000 sends 30 notes in about 3 s
//	max_playtime_ms  battle length; the client ends the battle itself when the time runs out
//	combo_approx_ms  combo timing the client displays (0 is fine)
//	play_note        one digit per note, in order (the note type); every digit needs a note_detail row;
//	                 0 is an invisible decoy that cannot be hit, d > 1 is a taller note that skips the
//	                 next d-1 digits
//	note_detail[]   {note_type, touch_max}: note_type is a digit 0 to 9, touch_max how many taps one note
//	                 of that type takes (at least 1); every accepted tap deals damage_per_hit
//	boss             {id, name, max_hp, tint, explain}: id is the FishId the client shows (it must not be
//	                 a fish id: the boss picture shares the fish picture route); tint is the picture colour
//	reward           gems_participation, gems_catch (Gems) and coins_participation, coins_catch (Boss Coins)
//
// The chart must be able to deal max_hp: len(play_note) * damage_per_hit >= max_hp.
type FishBoss struct {
	MaxTries      int64      `json:"max_tries"`
	ResetGems     int64      `json:"reset_gems"`
	DamagePerHit  int64      `json:"damage_per_hit"`
	NoteArriveMs  int64      `json:"note_arrive_ms"`
	MaxPlaytimeMs int64      `json:"max_playtime_ms"`
	ComboApproxMs int64      `json:"combo_approx_ms"`
	PlayNote      string     `json:"play_note"`
	NoteDetail    []BossNote `json:"note_detail"`
	Boss          BossDef    `json:"boss"`
	Reward        BossReward `json:"reward"`
}

// BossNote is one NoteDetailInfo row (rc71 GameData6).
type BossNote struct {
	NoteType int64 `json:"note_type"`
	TouchMax int64 `json:"touch_max"`
}

// BossDef is the boss. Name and explain are Cherry-made.
type BossDef struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	MaxHp   int64  `json:"max_hp"`
	Tint    string `json:"tint"`
	Explain string `json:"explain"`

	tint color.NRGBA
}

// TouchMax is the hit budget of a note digit (its note_detail touch_max; 0 for an unlisted digit).
func (f *FishBoss) TouchMax(digit byte) int64 {
	for _, n := range f.NoteDetail {
		if n.NoteType == int64(digit-'0') {
			return n.TouchMax
		}
	}
	return 0
}

// BossReward is what one finished battle pays.
type BossReward struct {
	GemsParticipation  int64 `json:"gems_participation"`
	GemsCatch          int64 `json:"gems_catch"`
	CoinsParticipation int64 `json:"coins_participation"`
	CoinsCatch         int64 `json:"coins_catch"`
}

var fishbossFile = opdata.New("fishboss.json", data.FishbossJSON, checkFishBoss, verifyFishBoss)

// ActiveFishBoss is the boss file in use.
func ActiveFishBoss() *FishBoss { return fishbossFile.Get() }

func checkFishBoss(f *FishBoss) error {
	if f.MaxTries < 1 {
		return errors.New("max_tries must be at least 1")
	}
	if f.ResetGems < 0 {
		return errors.New("reset_gems must not be negative (0 = no reset)")
	}
	if f.DamagePerHit < 1 {
		return errors.New("damage_per_hit must be at least 1")
	}
	if f.NoteArriveMs < 1000 {
		return errors.New("note_arrive_ms must be at least 1000 (the client reads whole seconds)")
	}
	if f.MaxPlaytimeMs < 1 || f.ComboApproxMs < 0 {
		return errors.New("max_playtime_ms must be positive and combo_approx_ms not negative")
	}
	if f.PlayNote == "" {
		return errors.New("play_note must not be empty")
	}
	types := map[int64]bool{}
	for _, n := range f.NoteDetail {
		if n.NoteType < 0 || n.NoteType > 9 || types[n.NoteType] || n.TouchMax < 1 {
			return fmt.Errorf("note_detail type %d: type must be a digit 0 to 9 listed once, touch_max at least 1", n.NoteType)
		}
		types[n.NoteType] = true
	}
	for i, c := range f.PlayNote {
		if c < '0' || c > '9' {
			return fmt.Errorf("play_note[%d] %q is not a digit", i, c)
		}
		if !types[int64(c-'0')] {
			return fmt.Errorf("play_note[%d] digit %c has no note_detail entry", i, c)
		}
	}
	if f.Boss.ID < 1 || f.Boss.Name == "" {
		return errors.New("boss id must be positive and the name set")
	}
	if f.Boss.MaxHp < 1 {
		return errors.New("boss max_hp must be positive")
	}
	c, err := parseHexColor(f.Boss.Tint)
	if err != nil {
		return fmt.Errorf("boss: %w", err)
	}
	f.Boss.tint = c
	if int64(len(f.PlayNote))*f.DamagePerHit < f.Boss.MaxHp {
		return fmt.Errorf("boss max_hp %d cannot be dealt by %d notes at %d per hit", f.Boss.MaxHp, len(f.PlayNote), f.DamagePerHit)
	}
	if f.Reward.GemsParticipation < 0 || f.Reward.GemsCatch < 0 || f.Reward.CoinsParticipation < 0 || f.Reward.CoinsCatch < 0 {
		return errors.New("reward amounts must not be negative")
	}
	return nil
}

// verifyFishBoss runs at startup, after the fish file: the boss picture is served by the fish
// picture route, so the boss id must not be a fish id.
func verifyFishBoss(f *FishBoss) error {
	for _, fd := range ActiveFishing().Fish {
		if fd.ID == f.Boss.ID {
			return fmt.Errorf("boss id %d is also a fish id", fd.ID)
		}
	}
	return nil
}

// fishBossTint is the picture colour of the boss with id, when id is the boss.
func fishBossTint(id int64) (color.NRGBA, bool) {
	if b := ActiveFishBoss(); b.Boss.ID == id {
		return b.Boss.tint, true
	}
	return color.NRGBA{}, false
}

// bossUsed is the tries aid spent on day; another day's record counts as none spent.
func bossUsed(acc *store.Account, day int64) int64 {
	if acc.BossTries == nil || acc.BossTries.Day != day {
		return 0
	}
	return acc.BossTries.Used
}

// bossLeft is the tries left after used on a day with limit tries.
func bossLeft(limit, used int64) int64 { return max(limit-used, 0) }

// BossTryStart spends one of aid's boss tries for the game day of now. left is the tries still
// left after the call; ok is false when none was left or the save failed (nothing changed).
func BossTryStart(aid string, now time.Time) (int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	limit := ActiveFishBoss().MaxTries
	day := AttendDayKey(now)
	used := bossUsed(acc, day)
	if used >= limit {
		return 0, false
	}
	prev := *acc
	acc.BossTries = &store.BossDay{Day: day, Used: used + 1}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square boss try save failed aid=%s err=%v", aid, err)
		return bossLeft(limit, used), false
	}
	return bossLeft(limit, used+1), true
}

// BossReset refills aid's boss tries for reset_gems Gems on the game day of now. It is refused
// (ok false, nothing changed) when the data offers no reset, tries are still left, the Gems are
// short or the save failed. left is the tries left after the call.
func BossReset(aid string, now time.Time) (int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	boss := ActiveFishBoss()
	day := AttendDayKey(now)
	left := bossLeft(boss.MaxTries, bossUsed(acc, day))
	if boss.ResetGems == 0 || left > 0 {
		return left, false
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -boss.ResetGems}, "fishboss:reset:"+questDate(day))
	if err != nil {
		*acc = prev
		return left, false
	}
	acc.BossTries = &store.BossDay{Day: day}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square boss reset save failed aid=%s err=%v", aid, err)
		return left, false
	}
	store.FlushLedgerLocked(lines)
	return boss.MaxTries, true
}

// BossFinish pays one finished battle for aid: the participation reward, plus the catch reward when
// caught. Gems go through the ledger; Boss Coins go on the account, and a catch is recorded in
// BossCaught. ok is false when the account is unknown or the save failed, and then nothing is paid.
// balance is the account's Boss Coins after the call.
func BossFinish(aid string, caught bool, now time.Time) (gems, coins, balance int64, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, 0, 0, false
	}
	boss := ActiveFishBoss()
	gems, coins = boss.Reward.GemsParticipation, boss.Reward.CoinsParticipation
	if caught {
		gems += boss.Reward.GemsCatch
		coins += boss.Reward.CoinsCatch
	}
	prev := *acc
	var lines []store.LedgerLine
	if gems > 0 {
		var err error
		reason := "fishboss:" + strconv.FormatInt(boss.Boss.ID, 10) + ":" + questDate(AttendDayKey(now))
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: gems}, reason)
		if err != nil {
			*acc = prev
			log.Printf("cherry: square boss reward failed aid=%s err=%v", aid, err)
			return 0, 0, prev.BossCoins, false
		}
	}
	acc.BossCoins = prev.BossCoins + coins
	if caught {
		counts := maps.Clone(acc.BossCaught) // a copy, so the rollback in *acc = prev restores the old record
		if counts == nil {
			counts = map[int64]int64{}
		}
		counts[boss.Boss.ID]++
		acc.BossCaught = counts
	}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square boss finish save failed aid=%s err=%v", aid, err)
		return 0, 0, prev.BossCoins, false
	}
	store.FlushLedgerLocked(lines)
	return gems, coins, acc.BossCoins, true
}
