package economy

import (
	"slices"
	"strings"
	"testing"

	"cherry/internal/store"
)

// TestItemGrantHumanOnlyPaysSellBackForAnimal: an item an ANIMAL cannot wear is not granted. Its sell-back Gems come back
// in the grant's own ledger line, which names the code; a human gets the hair, and an animal still gets a top.
func TestItemGrantHumanOnlyPaysSellBackForAnimal(t *testing.T) {
	shopFixture(t, map[string][]string{"dress/225000001": {"dp.png"}, "custom/224400001": {"dp.png"}})
	for _, c := range []struct {
		name, gender, code string
		granted            bool
	}{
		{"hair, human", "FEMALE", "CUHA00001", true},
		{"hair, animal", "ANIMAL", "CUHA00001", false},
		{"top, animal", "ANIMAL", "CUTO00001", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			acc := &store.Account{Aid: "9951", Name: "Grant", Gender: c.gender}
			lines, err := grantItemLocked(acc, c.code, "mail")
			if err != nil {
				t.Fatalf("grant: %v", err)
			}
			if got := slices.Contains(store.AccountOwnedCodes(acc), c.code); got != c.granted {
				t.Fatalf("owned = %v, want %v (owned %v)", got, c.granted, store.AccountOwnedCodes(acc))
			}
			if c.granted {
				if len(lines) != 0 {
					t.Fatalf("granted item paid lines %+v, want none", lines)
				}
				return
			}
			want := sellBackGems(c.code)
			if want <= 0 || len(lines) != 1 || lines[0].Currency != "gems" || lines[0].Delta != want || !strings.Contains(lines[0].Reason, c.code) || acc.Gems != want {
				t.Fatalf("sell-back lines %+v, gems %d: want one gems line of %d naming %s", lines, acc.Gems, want, c.code)
			}
		})
	}
}

// TestAttendRowsAnimalSellsBackHumanOnly: a calendar item day an ANIMAL cannot wear reads as its Gems plus the item's
// sell-back, as an owned item does, and a human still sees the item itself.
func TestAttendRowsAnimalSellsBackHumanOnly(t *testing.T) {
	shopFixture(t, map[string][]string{"custom/224400001": {"dp.png"}})
	sched := calSchedule{Cycle: [7]calDay{{Type: itemRewardType, Gems: 150, Item: "CUHA00001"}}}
	for _, c := range []struct {
		gender, itemCd string
		gem            int64
	}{
		{"FEMALE", "CUHA00001", 150},
		{"ANIMAL", "", 150 + sellBackGems("CUHA00001")},
	} {
		rows := attendRows(&store.Account{Aid: "9952", Gender: c.gender}, sched, 1, true)
		if row := rows[0]; row["itemCd"] != c.itemCd || row["gem"] != c.gem {
			t.Errorf("%s item day = itemCd %v gem %v, want itemCd %q gem %d", c.gender, row["itemCd"], row["gem"], c.itemCd, c.gem)
		}
	}
}
