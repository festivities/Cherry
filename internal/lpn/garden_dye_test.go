package lpn

import (
	"testing"

	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestGardenAvatarItemCarriesDye: a dyed garment's AvatarInfo item carries dye_type 2 and its two colour layers
// (GAMEBASE::ItemInfo fields 3 and 4), an undyed one only its item id.
func TestGardenAvatarItemCarriesDye(t *testing.T) {
	dress, ok := itemcode.Code(225001650)
	if !ok {
		t.Fatal("no code for the dress")
	}
	testutil.InstallAccounts(t, map[string]*store.Account{"gdye-9602": {AccessToken: "gdye-9602", SessionKey: "sk", Aid: "9602",
		Name: "Dyed", Gender: "FEMALE", ItemCodes: []string{dress, "CUTO0011X"},
		DyeColors: map[string][]store.DyeColor{dress: {{ColorCode: "#E53935"}, {ColorCode: "#43A047", Transparency: 20}}}}})
	var items [][]byte
	if !pbScan(gardenAvatarInfo(9602), func(field, _ uint64, d []byte) {
		if field == 13 {
			items = append(items, d)
		}
	}) || len(items) < 2 { // the appearance adds the base items of the empty slots
		t.Fatalf("avatar items = %d, want the dress and the base items", len(items))
	}
	for _, item := range items {
		var id, dyeType uint64
		var layers []struct {
			code string
			tr   uint64
		}
		pbScan(item, func(field, v uint64, d []byte) {
			switch field {
			case 1:
				if d == nil {
					id = v
				}
			case 3:
				dyeType = v
			case 4:
				var layer struct {
					code string
					tr   uint64
				}
				pbScan(d, func(f, v uint64, d []byte) {
					if f == 1 {
						layer.code = string(d)
					} else if f == 2 {
						layer.tr = v
					}
				})
				layers = append(layers, layer)
			}
		})
		if id == 225001650 {
			if dyeType != 2 || len(layers) != 2 || layers[0].code != "#E53935" || layers[1].code != "#43A047" || layers[1].tr != 20 {
				t.Fatalf("dyed item: dye_type %d layers %+v", dyeType, layers)
			}
		} else if dyeType != 0 || len(layers) != 0 {
			t.Fatalf("undyed item %d carries dye fields: dye_type %d layers %+v", id, dyeType, layers)
		}
	}
}
