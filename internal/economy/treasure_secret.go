package economy

import (
	"time"

	"cherry/internal/store"
)

// Treasure World secret quests (cell 5; cs 26 enable, cs 25 claim; the Gacha pulls count): a landing on a secret quest square
// starts its quest. The quest is enabled by cs 26 on the square (the client then opens the Gacha Shop from the quest's link).
// Each Gacha pull of the account while the quest is enabled counts toward its goal (special.goal_pulls); the goal reached makes
// it Done, and cs 25 pays special.reward_tickets while the token stands on the square. The goal and the reward are Cherry-made.

// treasureSecretLand starts the quest of ring square pos at a landing. The quest of pos stays as it is; a quest of another
// square that is not done yet is replaced; a done quest stays until it is claimed (cs 25).
func treasureSecretLand(r *store.TreasureRecord, d *TreasureData, pos int) {
	if r.Secret != nil && (r.Secret.Square == pos || r.Secret.Done) {
		return
	}
	r.Secret = &store.TreasureSecret{Square: pos, Goal: d.Special.GoalPulls}
}

// treasureOnSecret reports whether the token stands on the square of the account's secret quest.
func treasureOnSecret(r store.TreasureRecord, d *TreasureData) bool {
	return r.Secret != nil && r.Secret.Square == r.Position && d.Board.Ring[r.Position] == TreasureCellSpecial
}

// TreasureSecretEnable enables the secret quest of the square the token stands on (cs 26). Refused unless the token is on the
// quest's square.
func TreasureSecretEnable(aid string, now time.Time) (TreasureView, error) {
	view, err := treasureTx(aid, now, treasureGatePlay, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if !treasureOnSecret(*r, d) {
			return nil, ErrTreasureRefused
		}
		r.Secret.Enabled = true
		return nil, nil
	})
	return view, err
}

// TreasureSecretClaim pays the reward of the finished secret quest the token stands on (cs 25): the tickets, one secret task of
// progress, and the quest ends. It returns the tickets paid. Refused unless the quest is done and the token is on its square.
func TreasureSecretClaim(aid string, now time.Time) (int64, TreasureView, error) {
	var paid int64
	view, err := treasureTx(aid, now, treasureGatePlay, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if !treasureOnSecret(*r, d) || !r.Secret.Done {
			return nil, ErrTreasureRefused
		}
		paid = d.Special.RewardTickets
		r.Tickets += paid
		treasureProgress(r, d, "secret", 1)
		r.Secret = nil
		return nil, nil
	})
	return paid, view, err
}

// TreasureGachaPulled counts n Gacha pulls of aid toward its enabled, unfinished secret quest. The Gacha pull calls it while it
// holds store.AccountsMu, so this takes only the board's lock (TreasureMu), never AccountsMu.
func TreasureGachaPulled(aid string, n int64) {
	if rec, ok := store.TreasureRecordOf(aid); !ok || rec.Secret == nil || !rec.Secret.Enabled || rec.Secret.Done {
		return
	}
	_, _ = store.UpdateTreasure(aid, func(r *store.TreasureRecord) {
		if r.Secret == nil || !r.Secret.Enabled || r.Secret.Done {
			return
		}
		s := *r.Secret
		s.Count = min(s.Count+n, s.Goal)
		s.Done = s.Count >= s.Goal
		r.Secret = &s
	})
}
