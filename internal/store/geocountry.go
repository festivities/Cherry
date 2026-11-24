package store

import (
	"database/sql"
	"sync"
	"time"
)

// Countries looked up from the network of an avatar whose client sent none (schema v12 geo_countries; the lookup is
// cmd/cherry geocountry.go). Only the staff pages read them (Geography, Online now, the player page): Account.Country, which
// the game itself shows, is never changed. The table is read into memory at start; geoMu comes after AccountsMu.
var (
	geoMu        sync.Mutex
	geoCountries = map[string]string{} // avatar id -> ISO 3166-1 alpha-2 code
)

// loadGeoCountries reads the looked-up countries of d into memory (OpenStore).
func loadGeoCountries(d *DB) error {
	got := map[string]string{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT aid, country FROM geo_countries`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var aid, code string
			if err := rows.Scan(&aid, &code); err != nil {
				return err
			}
			got[aid] = code
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	geoMu.Lock()
	geoCountries = got
	geoMu.Unlock()
	return nil
}

// CountryUnknownAids returns those of aids whose account has no country code and no looked-up country yet.
func CountryUnknownAids(aids []string) []string {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	geoMu.Lock()
	defer geoMu.Unlock()
	var out []string
	for _, aid := range aids {
		acc := AccountByAidLocked(aid)
		if acc == nil {
			continue
		}
		if code, _, _ := CountryInfo(acc.Country); code != "" {
			continue
		}
		if geoCountries[aid] == "" {
			out = append(out, aid)
		}
	}
	return out
}

// SetGeoCountries keeps looked-up countries (avatar id -> ISO code), in the database when one is attached and in memory.
func SetGeoCountries(found map[string]string, now time.Time) error {
	if len(found) == 0 {
		return nil
	}
	if d := attachedDB(); d != nil {
		stamp := now.UTC().Format(time.RFC3339)
		if err := d.Update(func(tx *sql.Tx) error {
			for aid, code := range found {
				if _, err := tx.Exec(`INSERT OR REPLACE INTO geo_countries (aid, country, updated) VALUES (?, ?, ?)`, aid, code, stamp); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	geoMu.Lock()
	for aid, code := range found {
		geoCountries[aid] = code
	}
	geoMu.Unlock()
	return nil
}

// SetGeoCountriesForTest replaces the looked-up countries in memory (nil = none). Tests only.
func SetGeoCountriesForTest(m map[string]string) {
	geoMu.Lock()
	defer geoMu.Unlock()
	geoCountries = map[string]string{}
	for aid, code := range m {
		geoCountries[aid] = code
	}
}

// countryOfLocked is the country the staff pages show for acc: the one its client sent, else the looked-up one (byIP).
// Caller holds AccountsMu.
func countryOfLocked(acc *Account) (raw string, byIP bool) {
	if code, _, _ := CountryInfo(acc.Country); code != "" {
		return acc.Country, false
	}
	geoMu.Lock()
	defer geoMu.Unlock()
	if code := geoCountries[acc.Aid]; code != "" {
		return code, true
	}
	return acc.Country, false
}
