package economy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"cherry/data"
	"cherry/internal/opdata"
)

// Rewards is data/rewards.json: the Gems Cherry hands out (welcome gift, daily quests, hearts) and the
// mailbox rules.
//
//	welcome_gems      credited once per account by the welcome gift
//	quests[]          {id, key, name, desc, gems}: the daily quests (quests.go). id is the QuestId the
//	                  client sends back, keep it above the client's own codes. key is a progress key the
//	                  game code passes to MarkQuest (questTriggers lists them); keys stay unique. Exactly
//	                  one row is the hearts row: display true, key "", no gems (it pays hearts.reward_gems).
//	event_quests[]    {id, key, season, name, desc, gems}: daily quests listed and creditable only while
//	                  the season (a calendars.json name) is the calendar in force (season.go, /admin/season
//	                  included). Ids are unique across both lists. A key may repeat across seasons, but not
//	                  within one season or against quests[].
//	hearts            base: hearts a sender may send per game day (the daily allowance); quest: hearts sent in
//	                  one game day that pay the reward (0 = base, else 1 to base); reward_gems and reward_cash:
//	                  credited once a day with the quest-th heart; from_limit: sender aids kept per receiver per day;
//	                  cooldown_minutes: minutes before the same sender may heart the same target again (nil = 240, 0 = off)
//	mail              sender: the name on staff mail; default_days: expiry when a staff grant gives none;
//	                  max_amount: largest Gem, Cash or ticket amount per mail row; max_rows: mailbox size
//	birthday          claim_days: days from the avatar's birthday on which its reward can be claimed;
//	                  gacha_discount_percent: Gem gacha discount the claim gives; discount_days: how long it lasts
type Rewards struct {
	opdata.Provenance
	WelcomeGems  int64             `json:"welcome_gems"`
	Quests       []QuestDef        `json:"quests"`
	EventQuests  []EventQuest      `json:"event_quests"`
	Requests     []RepeatQuestDef  `json:"requests,omitempty"`
	RequestRules *RequestRules     `json:"request_rules,omitempty"`
	Episodes     []EpisodeQuestDef `json:"episodes,omitempty"`
	Hearts       HeartRules        `json:"hearts"`
	Mail         MailRules         `json:"mail"`
	Birthday     BirthdayRules     `json:"birthday"`
}

// RepeatQuestDef is one Garden Requests tab quest (rewards.json requests).
type RepeatQuestDef struct {
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	Icon    string `json:"icon,omitempty"`
	NPCIcon string `json:"npc_icon,omitempty"`
	Gems    int64  `json:"gems"`
	Cash    int64  `json:"cash,omitempty"`
	Goal    int64  `json:"goal,omitempty"`
}

// RequestRules are the daily limit and Neighbor Invite pricing for Garden Requests (rewards.json request_rules).
type RequestRules struct {
	MaxDaily    int64 `json:"max_daily"`
	FreeCalls   int64 `json:"free_calls"`
	CallGems    int64 `json:"call_gems"`
	RefreshGems int64 `json:"refresh_gems"`
}

// EpisodeQuestDef is one Garden Episode tab chapter (rewards.json episodes).
type EpisodeQuestDef struct {
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	Icon    string `json:"icon,omitempty"`
	NPCIcon string `json:"npc_icon,omitempty"`
	Gems    int64  `json:"gems"`
	Cash    int64  `json:"cash,omitempty"`
	Goal    int64  `json:"goal,omitempty"`
}

// BirthdayRules are the avatar birthday reward numbers (rewards.json birthday; birthday.go).
type BirthdayRules struct {
	ClaimDays            int   `json:"claim_days"`
	GachaDiscountPercent int64 `json:"gacha_discount_percent"`
	DiscountDays         int   `json:"discount_days"`
}

// EventQuest is one event quest (rewards.json event_quests). It has no display flag: only the daily
// hearts row is display-only.
type EventQuest struct {
	ID     int64  `json:"id"`
	Key    string `json:"key"`
	Season string `json:"season"`
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Icon   string `json:"icon,omitempty"`
	Gems   int64  `json:"gems"`
}

const (
	defaultHeartCooldownMinutes = 240     // 4 hours between hearts from the same sender to the same target
	maxHeartCooldownMinutes     = 24 * 60 // at most 24 hours
)

// HeartRules are the hearts-quest numbers (rewards.json hearts). checkRewards sets Quest to Base when the file
// leaves it out, so callers read the effective value. CooldownMinutes nil means defaultHeartCooldownMinutes; 0 turns
// the per-target cooldown off.
type HeartRules struct {
	Base            int64 `json:"base"`
	Quest           int64 `json:"quest"`
	RewardGems      int64 `json:"reward_gems"`
	RewardCash      int64 `json:"reward_cash"`
	FromLimit       int   `json:"from_limit"`
	CooldownMinutes *int  `json:"cooldown_minutes,omitempty"`
}

// MailRules are the mailbox numbers (rewards.json mail).
type MailRules struct {
	Sender      string `json:"sender"`
	DefaultDays int    `json:"default_days"`
	MaxAmount   int64  `json:"max_amount"`
	MaxRows     int    `json:"max_rows"`
}

var (
	rewardsFile = opdata.New("rewards.json", data.RewardsJSON, checkRewards, nil)
)

// ActiveRewards is the rewards file in use.
func ActiveRewards() *Rewards { return rewardsFile.Get() }

// ActiveRequestRules returns the active RequestRules, falling back to defaults when omitted.
func ActiveRequestRules() RequestRules {
	if rr := ActiveRewards().RequestRules; rr != nil {
		return *rr
	}
	return RequestRules{MaxDaily: 10, FreeCalls: 3, CallGems: 10, RefreshGems: 10}
}

// HeartQuest is the hearts row of the quest list (checkRewards guarantees one).
func HeartQuest() QuestDef {
	for _, q := range ActiveRewards().Quests {
		if q.Display {
			return q
		}
	}
	return QuestDef{}
}

// CherryQuestFirst and CherryQuestLast bound the quest ids Cherry makes: the daily quests (9001-9005), the
// event quests of each season (9101-9303), Requests (9401-9499) and Episodes (9501-9599). The client compiles
// no quest goal or reward (phaseD quests.md), and no archive holds a quest id.
const (
	CherryQuestFirst int64 = 9001
	CherryQuestLast  int64 = 9599
)

func checkRewards(r *Rewards) error {
	if err := r.CheckOrigin("cherry"); err != nil {
		return err
	}
	if r.Hearts.Quest == 0 { // an override file from before hearts.quest keeps its meaning: quest = base
		r.Hearts.Quest = r.Hearts.Base
	}
	switch {
	case r.WelcomeGems < 0:
		return errors.New("welcome_gems must not be negative")
	case r.Hearts.Base < 1:
		return errors.New("hearts.base must be at least 1")
	case r.Hearts.Quest < 1 || r.Hearts.Quest > r.Hearts.Base:
		return errors.New("hearts.quest must be from 1 to hearts.base (or left out, which means hearts.base)")
	case r.Hearts.RewardGems < 0:
		return errors.New("hearts.reward_gems must not be negative")
	case r.Hearts.RewardCash < 0:
		return errors.New("hearts.reward_cash must not be negative")
	case r.Hearts.FromLimit < 1:
		return errors.New("hearts.from_limit must be at least 1")
	case r.Hearts.CooldownMinutes != nil && (*r.Hearts.CooldownMinutes < 0 || *r.Hearts.CooldownMinutes > maxHeartCooldownMinutes):
		return fmt.Errorf("hearts.cooldown_minutes must be from 0 to %d", maxHeartCooldownMinutes)
	case r.Mail.Sender == "":
		return errors.New("mail.sender must not be empty")
	case r.Mail.DefaultDays < 1 || r.Mail.DefaultDays > 3650:
		return errors.New("mail.default_days must be 1 to 3650")
	case r.Mail.MaxAmount < 1:
		return errors.New("mail.max_amount must be at least 1")
	case r.Mail.MaxRows < 1:
		return errors.New("mail.max_rows must be at least 1")
	case r.Birthday.ClaimDays < 1 || r.Birthday.ClaimDays > 30:
		return errors.New("birthday.claim_days must be 1 to 30")
	case r.Birthday.GachaDiscountPercent < 1 || r.Birthday.GachaDiscountPercent > 90:
		return errors.New("birthday.gacha_discount_percent must be 1 to 90")
	case r.Birthday.DiscountDays < 1 || r.Birthday.DiscountDays > 365:
		return errors.New("birthday.discount_days must be 1 to 365")
	}
	ids := map[int64]bool{}
	keys := map[string]bool{}
	hearts := 0
	for i := range r.Quests {
		q := &r.Quests[i]
		if !inCherryBlock(q.ID, CherryQuestFirst, CherryQuestLast) || ids[q.ID] {
			return fmt.Errorf("quest %d: id must be unique and inside the Cherry block %d-%d", q.ID, CherryQuestFirst, CherryQuestLast)
		}
		ids[q.ID] = true
		if q.Name == "" {
			return fmt.Errorf("quest %d: name must not be empty", q.ID)
		}
		if q.Display {
			hearts++
			switch {
			case q.Key != "":
				return fmt.Errorf("quest %d: the hearts row has no key", q.ID)
			case q.Gems != 0:
				return fmt.Errorf("quest %d: the hearts row pays hearts.reward_gems, remove gems", q.ID)
			case q.Cash != 0:
				return fmt.Errorf("quest %d: the hearts row pays hearts.reward_cash, remove cash", q.ID)
			}
			q.Gems = r.Hearts.RewardGems
			q.Cash = r.Hearts.RewardCash
			continue
		}
		if q.Key == "" || keys[q.Key] {
			return fmt.Errorf("quest %d: key %q must be unique and not empty", q.ID, q.Key)
		}
		if !slices.Contains(questTriggers, q.Key) {
			return fmt.Errorf("quest %d: key %q is not a known trigger (%s)", q.ID, q.Key, strings.Join(questTriggers, ", "))
		}
		keys[q.Key] = true
		if q.Gems < 0 {
			return fmt.Errorf("quest %d: gems must not be negative", q.ID)
		}
	}
	if hearts != 1 {
		return fmt.Errorf("quests need exactly one hearts row (display true), not %d", hearts)
	}
	seasonNames := map[string]bool{}
	for _, s := range seasons() {
		seasonNames[s.Name] = true
	}
	eventKeys := map[string]map[string]bool{} // season -> keys of its event quests
	for _, e := range r.EventQuests {
		switch {
		case !inCherryBlock(e.ID, CherryQuestFirst, CherryQuestLast) || ids[e.ID]:
			return fmt.Errorf("event quest %d: id must be unique and inside the Cherry block %d-%d", e.ID, CherryQuestFirst, CherryQuestLast)
		case e.Name == "":
			return fmt.Errorf("event quest %d: name must not be empty", e.ID)
		case !seasonNames[e.Season]:
			return fmt.Errorf("event quest %d: season %q is not a season in calendars.json", e.ID, e.Season)
		case e.Key == "" || keys[e.Key] || eventKeys[e.Season][e.Key]:
			return fmt.Errorf("event quest %d: key %q must be unique within its season and not a daily quest key", e.ID, e.Key)
		case !slices.Contains(questTriggers, e.Key):
			return fmt.Errorf("event quest %d: key %q is not a known trigger (%s)", e.ID, e.Key, strings.Join(questTriggers, ", "))
		case e.Gems < 0:
			return fmt.Errorf("event quest %d: gems must not be negative", e.ID)
		}
		ids[e.ID] = true
		if eventKeys[e.Season] == nil {
			eventKeys[e.Season] = map[string]bool{}
		}
		eventKeys[e.Season][e.Key] = true
	}
	if r.RequestRules != nil {
		switch {
		case r.RequestRules.MaxDaily < 1 || r.RequestRules.MaxDaily > 100:
			return errors.New("request_rules.max_daily must be 1 to 100")
		case r.RequestRules.FreeCalls < 0:
			return errors.New("request_rules.free_calls must not be negative")
		case r.RequestRules.CallGems < 0:
			return errors.New("request_rules.call_gems must not be negative")
		case r.RequestRules.RefreshGems < 0:
			return errors.New("request_rules.refresh_gems must not be negative")
		}
	}
	for i := range r.Requests {
		q := &r.Requests[i]
		if err := checkTabQuest(ids, "request", q.ID, q.Key, q.Name, q.Gems, q.Cash, &q.Goal); err != nil {
			return err
		}
	}
	for i := range r.Episodes {
		q := &r.Episodes[i]
		if err := checkTabQuest(ids, "episode", q.ID, q.Key, q.Name, q.Gems, q.Cash, &q.Goal); err != nil {
			return err
		}
	}
	return nil
}

func checkTabQuest(ids map[int64]bool, kind string, id int64, key, name string, gems, cash int64, goal *int64) error {
	switch {
	case !inCherryBlock(id, CherryQuestFirst, CherryQuestLast) || ids[id]:
		return fmt.Errorf("%s %d: id must be unique and inside the Cherry block %d-%d", kind, id, CherryQuestFirst, CherryQuestLast)
	case name == "":
		return fmt.Errorf("%s %d: name must not be empty", kind, id)
	case !slices.Contains(questTriggers, key):
		return fmt.Errorf("%s %d: key %q is not a known trigger (%s)", kind, id, key, strings.Join(questTriggers, ", "))
	case gems < 0:
		return fmt.Errorf("%s %d: gems must not be negative", kind, id)
	case cash < 0:
		return fmt.Errorf("%s %d: cash must not be negative", kind, id)
	case *goal < 0:
		return fmt.Errorf("%s %d: goal must not be negative", kind, id)
	}
	if *goal == 0 {
		*goal = 1
	}
	ids[id] = true
	return nil
}
