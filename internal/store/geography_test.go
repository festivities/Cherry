package store

import (
	"fmt"
	"testing"
	"time"
)

func TestCountryInfoNormalization(t *testing.T) {
	cases := []struct {
		raw        string
		wantCode   string
		wantName   string
		wantRegion string
	}{
		{"", "", "Unknown", RegionUnknown},
		{"   ", "", "Unknown", RegionUnknown},
		{"id", "ID", "Indonesia", RegionSoutheastAsia},
		{" ID ", "ID", "Indonesia", RegionSoutheastAsia},
		{"th", "TH", "Thailand", RegionSoutheastAsia},
		{"jp", "JP", "Japan", RegionEastAsia},
		{"kr", "KR", "South Korea", RegionEastAsia},
		{"us", "US", "United States", RegionNorthAmerica},
		{"gb", "GB", "United Kingdom", RegionEurope},
		{"au", "AU", "Australia", RegionOceania},
		{"br", "BR", "Brazil", RegionSouthAmerica},
		{"in", "IN", "India", RegionSouthWestAsia},
		{"za", "ZA", "South Africa", RegionAfrica},
		{"sd", "SD", "Sudan", RegionAfrica},
		{"zz", "ZZ", "ZZ", RegionOther},
		{"xy", "XY", "XY", RegionOther},
	}
	for _, tc := range cases {
		code, name, region := CountryInfo(tc.raw)
		if code != tc.wantCode || name != tc.wantName || region != tc.wantRegion {
			t.Errorf("CountryInfo(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.raw, code, name, region, tc.wantCode, tc.wantName, tc.wantRegion)
		}
	}
}

func TestGeographySummary(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	recentMs := now.Add(-2 * 24 * time.Hour).UnixMilli()
	boundaryMs := now.Add(-7 * 24 * time.Hour).UnixMilli()
	oldMs := now.Add(-8 * 24 * time.Hour).UnixMilli()

	seed := map[string]*Account{
		"tok-p1": {Aid: "", Country: "id", Gender: "FEMALE"},
		"tok-p2": {Aid: "0", Country: "us", Gender: "MALE"},
		"tok-1":  {Aid: "1001", Country: "id", Gender: "female", AvatarCreated: recentMs},
		"tok-2":  {Aid: "1002", Country: "ID", Gender: "MALE", AvatarCreated: boundaryMs},
		"tok-3":  {Aid: "1003", Country: " id ", Gender: "ANIMAL", AvatarCreated: oldMs},
		"tok-4":  {Aid: "1004", Country: "th", Gender: "FEMALE", AvatarCreated: recentMs},
		"tok-5":  {Aid: "1005", Country: "jp", Gender: "FEMALE", AvatarCreated: 0},
		"tok-6":  {Aid: "1006", Country: "zz", Gender: "", AvatarCreated: recentMs},
		"tok-7":  {Aid: "1007", Country: "", Gender: "MALE", AvatarCreated: oldMs},
		"tok-8":  {Aid: "1008", Country: "   ", Gender: "FEMALE", AvatarCreated: recentMs},
	}

	restore, err := SwapAccountsForTest(seed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)

	online := map[string]bool{
		"1001": true, // ID
		"1003": true, // ID
		"1007": true, // Unknown
		"9999": true, // no account
		"1004": false,
	}

	rep := GeographySummary(online, now)

	if rep.TotalAvatars != 8 {
		t.Fatalf("TotalAvatars = %d, want 8", rep.TotalAvatars)
	}
	if rep.PendingCreation != 2 {
		t.Errorf("PendingCreation = %d, want 2", rep.PendingCreation)
	}
	if rep.CountriesCount != 4 {
		t.Errorf("CountriesCount = %d, want 4 (ID, TH, JP, ZZ)", rep.CountriesCount)
	}
	if rep.TopCountry.Code != "ID" || rep.TopCountry.Name != "Indonesia" || rep.TopCountry.Total != 3 {
		t.Errorf("TopCountry = %+v, want ID (3)", rep.TopCountry)
	}
	if rep.UnknownCount != 2 {
		t.Errorf("UnknownCount = %d, want 2", rep.UnknownCount)
	}
	if rep.OnlineTotal != 4 || rep.OnlineMapped != 2 {
		t.Errorf("OnlineTotal=%d OnlineMapped=%d, want 4 and 2", rep.OnlineTotal, rep.OnlineMapped)
	}
	if rep.New7dTotal != 5 {
		t.Errorf("New7dTotal = %d, want 5", rep.New7dTotal)
	}

	// Countries sorted by total desc, then code asc:
	// ID (3), "" (2), JP (1), TH (1), ZZ (1)
	wantOrder := []string{"ID", "", "JP", "TH", "ZZ"}
	if len(rep.Countries) != len(wantOrder) {
		t.Fatalf("len(Countries) = %d, want %d", len(rep.Countries), len(wantOrder))
	}
	for i, code := range wantOrder {
		if rep.Countries[i].Code != code {
			t.Errorf("Countries[%d].Code = %q, want %q", i, rep.Countries[i].Code, code)
		}
		if b := rep.Countries[i].Bar; b < 0 || b > 100 || b%5 != 0 {
			t.Errorf("Countries[%d].Bar = %d, want 0..100 step 5", i, b)
		}
	}

	idRow := rep.Countries[0]
	if idRow.Online != 2 || idRow.New7d != 2 || idRow.Female != 1 || idRow.Male != 1 || idRow.Animal != 1 {
		t.Errorf("ID row = %+v", idRow)
	}

	// Regions: Southeast Asia (4), Unknown (2), East Asia (1), Other (1)
	if len(rep.Regions) != 4 || rep.Regions[0].Name != RegionSoutheastAsia || rep.Regions[0].Total != 4 || rep.Regions[0].Online != 2 || rep.Regions[0].New7d != 3 {
		t.Errorf("Regions = %+v", rep.Regions)
	}

	// OnlineCountries: ID (2), "" (1)
	if len(rep.OnlineCountries) != 2 || rep.OnlineCountries[0].Code != "ID" || rep.OnlineCountries[0].Online != 2 || rep.OnlineCountries[1].Code != "" {
		t.Errorf("OnlineCountries = %+v", rep.OnlineCountries)
	}

	// AvatarTypes: Female (4), Male (2), Animal (1), Unset (1)
	wantTypes := map[string]int{"Female": 4, "Male": 2, "Animal": 1, "Unset": 1}
	for _, at := range rep.AvatarTypes {
		if at.Count != wantTypes[at.Label] {
			t.Errorf("AvatarTypes[%s] = %d, want %d", at.Label, at.Count, wantTypes[at.Label])
		}
	}
}

func TestGeographyNewCountriesTop15Limit(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	recentMs := now.Add(-time.Hour).UnixMilli()
	codes := []string{"ID", "TH", "MY", "SG", "VN", "PH", "JP", "KR", "CN", "TW", "HK", "US", "CA", "GB", "AU", "DE", "FR", "BR"}
	seed := make(map[string]*Account, len(codes))
	for i, code := range codes {
		seed[fmt.Sprintf("tok-%d", i)] = &Account{
			Aid:           fmt.Sprintf("%d", 2000+i),
			Country:       code,
			Gender:        "FEMALE",
			AvatarCreated: recentMs,
		}
	}
	restore, err := SwapAccountsForTest(seed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)

	rep := GeographySummary(nil, now)
	if len(rep.NewCountries) != 15 {
		t.Fatalf("len(NewCountries) = %d, want 15", len(rep.NewCountries))
	}
	// All have New7d == 1 and Total == 1, so sorted by Code asc: AU first.
	if rep.NewCountries[0].Code != "AU" {
		t.Errorf("NewCountries[0].Code = %q, want AU", rep.NewCountries[0].Code)
	}
}
