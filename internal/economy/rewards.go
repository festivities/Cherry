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
//	hearts            base: hearts per game day that count toward the quest; reward_gems: Gems credited
//	                  once a day with the base-th heart; from_limit: sender aids kept per receiver per day
//	mail              sender: the name on staff mail; default_days: expiry when a staff grant gives none;
//	                  max_amount: largest Gem, Cash or ticket amount per mail row; max_rows: mailbox size
type Rewards struct {
	WelcomeGems int64        `json:"welcome_gems"`
	Quests      []QuestDef   `json:"quests"`
	EventQuests []EventQuest `json:"event_quests"`
	Hearts      HeartRules   `json:"hearts"`
	Mail        MailRules    `json:"mail"`
}

// EventQuest is one event quest (rewards.json event_quests). It has no display flag: only the daily
// hearts row is display-only.
type EventQuest struct {
	ID     int64  `json:"id"`
	Key    string `json:"key"`
	Season string `json:"season"`
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Gems   int64  `json:"gems"`
}

// HeartRules are the hearts-quest numbers (rewards.json hearts).
type HeartRules struct {
	Base       int64 `json:"base"`
	RewardGems int64 `json:"reward_gems"`
	FromLimit  int   `json:"from_limit"`
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

// HeartQuest is the hearts row of the quest list (checkRewards guarantees one).
func HeartQuest() QuestDef {
	for _, q := range ActiveRewards().Quests {
		if q.Display {
			return q
		}
	}
	return QuestDef{}
}

func checkRewards(r *Rewards) error {
	switch {
	case r.WelcomeGems < 0:
		return errors.New("welcome_gems must not be negative")
	case r.Hearts.Base < 1:
		return errors.New("hearts.base must be at least 1")
	case r.Hearts.RewardGems < 0:
		return errors.New("hearts.reward_gems must not be negative")
	case r.Hearts.FromLimit < 1:
		return errors.New("hearts.from_limit must be at least 1")
	case r.Mail.Sender == "":
		return errors.New("mail.sender must not be empty")
	case r.Mail.DefaultDays < 1 || r.Mail.DefaultDays > 3650:
		return errors.New("mail.default_days must be 1 to 3650")
	case r.Mail.MaxAmount < 1:
		return errors.New("mail.max_amount must be at least 1")
	case r.Mail.MaxRows < 1:
		return errors.New("mail.max_rows must be at least 1")
	}
	ids := map[int64]bool{}
	keys := map[string]bool{}
	hearts := 0
	for i := range r.Quests {
		q := &r.Quests[i]
		if q.ID < 1 || ids[q.ID] {
			return fmt.Errorf("quest %d: id must be unique and positive", q.ID)
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
			}
			q.Gems = r.Hearts.RewardGems
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
		case e.ID < 1 || ids[e.ID]:
			return fmt.Errorf("event quest %d: id must be unique and positive", e.ID)
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
	return nil
}
