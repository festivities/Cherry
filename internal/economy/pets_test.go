package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/opdata"
	"cherry/internal/room"
)

func TestActivePetsLoaded(t *testing.T) {
	cat := ActivePets()
	if got := len(cat.Pets); got != 310 {
		t.Fatalf("len(ActivePets().Pets) = %d, want 310", got)
	}
	cases := []struct {
		code string
		want PetSpec
	}{
		{"PUPE0000G", PetSpec{Name: "Mixed Breed", Price: 800, Currency: "GEM", Cuddles: 100}},
		{"PUPE0000J", PetSpec{Name: "Dachshund", Price: 12, Currency: "CASH", Cuddles: 100}},
		{"PUPE0006E", PetSpec{Name: "Carrie", Price: 2000, Currency: "GEM", Cuddles: 100, Stats: &RacingPetStat{Power: 100, Speed: 110, Stamina: 105, ItemSlot: 1}}},
		{"PUPE00074", PetSpec{Name: "Beam", Price: 35, Currency: "CASH", Cuddles: 100, Stats: &RacingPetStat{Power: 98, Speed: 130, Stamina: 101, ItemSlot: 1}}},
	}
	for _, tc := range cases {
		got, ok := PetSpecOf(tc.code)
		if !ok {
			t.Fatalf("PetSpecOf(%s) missing", tc.code)
		}
		if got.Name != tc.want.Name || got.Price != tc.want.Price || got.Currency != tc.want.Currency || got.Cuddles != tc.want.Cuddles {
			t.Errorf("%s = %+v, want %+v", tc.code, got, tc.want)
		}
		if (got.Stats == nil) != (tc.want.Stats == nil) || (got.Stats != nil && *got.Stats != *tc.want.Stats) {
			t.Errorf("%s stats = %+v, want %+v", tc.code, got.Stats, tc.want.Stats)
		}
	}
}

func TestCheckPetsValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bad origin",
			raw:  `{"origin":"cherry","note":"x","pets":{"PUPE0000G":{"name":"Mixed Breed"}}}`,
			want: `origin must be "mixed"`,
		},
		{
			name: "short code",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0000":{"name":"Mixed Breed"}}}`,
			want: `must be a 9-char PUPE code`,
		},
		{
			name: "wrong prefix",
			raw:  `{"origin":"mixed","note":"x","pets":{"CUPE0000G":{"name":"Mixed Breed"}}}`,
			want: `must be a 9-char PUPE code`,
		},
		{
			name: "zero skin id",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE00000":{"name":"Mixed Breed"}}}`,
			want: `above 326400000`,
		},
		{
			name: "empty name",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0000G":{"name":""}}}`,
			want: `name must not be empty`,
		},
		{
			name: "negative price",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0000G":{"name":"Mixed Breed","price":-1,"currency":"GEM"}}}`,
			want: `price must not be negative`,
		},
		{
			name: "bad currency",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0000G":{"name":"Mixed Breed","price":100,"currency":"GOLD"}}}`,
			want: `must be GEM or CASH`,
		},
		{
			name: "currency without price",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0000G":{"name":"Mixed Breed","currency":"GEM"}}}`,
			want: `must be empty when price is 0`,
		},
		{
			name: "negative cuddles",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0000G":{"name":"Mixed Breed","cuddles":-5}}}`,
			want: `cuddles must not be negative`,
		},
		{
			name: "negative stat",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0006E":{"name":"Carrie","stats":{"power":-1,"speed":100,"stamina":100,"item_slot":1}}}}`,
			want: `stats must be 0 to 1000`,
		},
		{
			name: "stat above 1000",
			raw:  `{"origin":"mixed","note":"x","pets":{"PUPE0006E":{"name":"Carrie","stats":{"power":100,"speed":1001,"stamina":100,"item_slot":1}}}}`,
			want: `stats must be 0 to 1000`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := opdata.Decode([]byte(tc.raw), checkPets)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Decode err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPetCatalogPricingNamesAndStats(t *testing.T) {
	shopFixture(t, map[string][]string{
		"pet/326400016": {"skeleton.xml", "dp.png"},              // PUPE0000G: 800 GEM
		"pet/326400019": {"skeleton.xml", "dp.png"},              // PUPE0000J: 12 CASH
		"pet/326400029": {"skeleton.xml", "dp.png"},              // PUPE0000T: unpriced -> 1200 GEM fallback
		"pet/326400230": {"skeleton.xml", "r.aniproj", "dp.png"}, // PUPE0006E: 2000 GEM ride
		"pet/326400256": {"skeleton.xml", "r.aniproj", "dp.png"}, // PUPE00074: 35 CASH ride
		"pet/326400259": {"skeleton.xml", "r.aniproj", "dp.png"}, // PUPE00077: unpriced animated -> 25 CASH fallback
	})

	priceCases := []struct {
		code  string
		price int64
		cash  bool
		grade string
		name  string
	}{
		{"PUPE0000G", 800, false, "N", "Mixed Breed"},
		{"PUPE0000J", 12, true, "N", "Dachshund"},
		{"PUPE0000T", 1200, false, "N", "Bichon"},
		{"PUPE0006E", 2000, false, "R", "Carrie"},
		{"PUPE00074", 35, true, "R", "Beam"},
		{"PUPE00077", 25, true, "R", "Pio"},
		{"PUPE99999", 1200, false, "N", "Pet 99999"},
	}
	for _, tc := range priceCases {
		price, cash, grade, ok := ItemPrice(tc.code)
		if !ok || price != tc.price || cash != tc.cash || grade != tc.grade {
			t.Errorf("ItemPrice(%s) = %d %v %s %v, want %d %v %s true", tc.code, price, cash, grade, ok, tc.price, tc.cash, tc.grade)
		}
		if got := ItemName(tc.code); got != tc.name {
			t.Errorf("ItemName(%s) = %q, want %q", tc.code, got, tc.name)
		}
	}

	if got, want := PetRaceStat("PUPE0006E"), (RacingPetStat{Power: 100, Speed: 110, Stamina: 105, ItemSlot: 1}); got != want {
		t.Errorf("PetRaceStat(PUPE0006E) = %+v, want %+v", got, want)
	}
	if got, want := PetRaceStat("PUPE00074"), (RacingPetStat{Power: 98, Speed: 130, Stamina: 101, ItemSlot: 1}); got != want {
		t.Errorf("PetRaceStat(PUPE00074) = %+v, want %+v", got, want)
	}
	if got, want := PetRaceStat("PUPE00077"), ActiveRacing().PetStat; got != want {
		t.Errorf("PetRaceStat(PUPE00077) = %+v, want fallback %+v", got, want)
	}

	species := PetSpecies()
	namesByCode := map[string]string{}
	for _, sp := range species {
		namesByCode[sp.Code] = sp.Name
	}
	if namesByCode["PUPE0000G"] != "Mixed Breed" || namesByCode["PUPE0006E"] != "Carrie" {
		t.Errorf("PetSpecies names = %v", namesByCode)
	}

	rec := httptest.NewRecorder()
	handleShopList(kindPet)(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/petitemlist/UPETINT", nil))
	var listResp struct {
		Result struct {
			Items []struct {
				ItemCode string `json:"itemCode"`
				Name     string `json:"name"`
				Speed    int64  `json:"speed"`
				Power    int64  `json:"power"`
				Stamina  int64  `json:"stamina"`
				ItemSlot int64  `json:"itemSlot"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	byCode := map[string]struct {
		Name                        string
		Speed, Power, Stamina, Slot int64
	}{}
	for _, it := range listResp.Result.Items {
		byCode[it.ItemCode] = struct {
			Name                        string
			Speed, Power, Stamina, Slot int64
		}{it.Name, it.Speed, it.Power, it.Stamina, it.ItemSlot}
	}
	if got := byCode["PUPE0006E"]; got.Name != "Carrie" || got.Power != 100 || got.Speed != 110 || got.Stamina != 105 || got.Slot != 1 {
		t.Errorf("shop row PUPE0006E = %+v", got)
	}
	if got, def := byCode["PUPE00077"], ActiveRacing().PetStat; got.Name != "Pio" || got.Power != def.Power || got.Speed != def.Speed || got.Stamina != def.Stamina || got.Slot != def.ItemSlot {
		t.Errorf("shop row PUPE00077 = %+v, want Pio + fallback %+v", got, def)
	}
	if got := byCode["PUPE0000G"]; got.Name != "Mixed Breed" || got.Power != 0 || got.Speed != 0 || got.Stamina != 0 || got.Slot != 0 {
		t.Errorf("shop row PUPE0000G = %+v, want Mixed Breed + 0 stats", got)
	}

	// Buy assigns PetDisplayName to the owned PetItem.
	acc, _ := phaseEAccount(t, 5000, nil)
	if rec := postAs(handleShopBuy(kindPet), `{"items":[{"itemCode":"PUPE0000G","price":800}]}`); rec.Code != http.StatusOK {
		t.Fatalf("buy PUPE0000G = %d %s", rec.Code, rec.Body.String())
	}
	if len(acc.Pets) != 1 || acc.Pets[0].Name != "Mixed Breed" {
		t.Errorf("purchased pet = %+v, want Name Mixed Breed", acc.Pets)
	}
	// Verify room.PetSpecies type compiles with Name.
	_ = room.PetSpecies{Code: "PUPE0000G", Name: "Mixed Breed"}
}
