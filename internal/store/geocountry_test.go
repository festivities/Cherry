package store

import (
	"testing"
	"time"
)

// TestGeographyUsesLookedUpCountries: an avatar whose client sent no country counts under the country looked up from its
// network (ByIPCount), one with neither stays Unset, and the client's own country always wins.
func TestGeographyUsesLookedUpCountries(t *testing.T) {
	restore, err := SwapAccountsForTest(map[string]*Account{
		"t1": {AccessToken: "t1", Aid: "801", Name: "A"},
		"t2": {AccessToken: "t2", Aid: "802", Name: "B"},
		"t3": {AccessToken: "t3", Aid: "803", Name: "C", Country: "kr"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	SetGeoCountriesForTest(map[string]string{"801": "JP", "803": "US"})
	t.Cleanup(func() { SetGeoCountriesForTest(nil) })

	rep := GeographySummary(map[string]bool{}, time.Now())
	if rep.UnknownCount != 1 || rep.ByIPCount != 1 {
		t.Fatalf("unknown %d, by IP %d, want 1 and 1", rep.UnknownCount, rep.ByIPCount)
	}
	got := map[string]int{}
	for _, c := range rep.Countries {
		got[c.Code] = c.Total
	}
	if got["JP"] != 1 || got["KR"] != 1 || got["US"] != 0 {
		t.Fatalf("countries %v, want JP 1 (looked up), KR 1 (sent, not the looked-up US)", got)
	}
}
