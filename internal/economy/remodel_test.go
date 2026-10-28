package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/itemcode"
)

// TestRemodelingRoomListRows: the owned floors answer their row in the shape ParseRemodelingTable reads (owned and
// in use, zero price, GEM); the 3F tab answers an empty items array; non-GET is refused.
func TestRemodelingRoomListRows(t *testing.T) {
	want := map[string]RemodelRow{"URMEXT": {Code: "RUTI0005D", RoomSize: 1}, "URM2F": {Code: "RUWA0006D", RoomSize: 2}}
	for _, tab := range []string{"URMEXT", "URM2F", "URM3F"} {
		rec := httptest.NewRecorder()
		handleRemodelingRoomList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/remodelingroomlist/"+tab+"?isFull=false", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tab, rec.Code)
		}
		var env struct {
			Result struct {
				Items []map[string]any `json:"items"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: reply %s: %v", tab, rec.Body, err)
		}
		w, owned := want[tab]
		if !owned {
			if rec.Body.String() != `{"result":{"items":[]}}` {
				t.Fatalf("%s: body %s, want an empty list", tab, rec.Body)
			}
			continue
		}
		if len(env.Result.Items) != 1 {
			t.Fatalf("%s: %d rows, want 1", tab, len(env.Result.Items))
		}
		row := env.Result.Items[0]
		id, ok := itemcode.ID(w.Code)
		if back, _ := itemcode.Code(id); !ok || back != w.Code {
			t.Fatalf("%s: code %s does not round-trip", tab, w.Code)
		}
		if row["itemCode"] != w.Code || row["itemID"] != float64(id) || row["roomSize"] != float64(w.RoomSize) ||
			row["existInven"] != true || row["inUseYn"] != true || row["price"] != float64(0) || row["paymentType"] != "GEM" {
			t.Fatalf("%s: row %v", tab, row)
		}
	}
	rec := httptest.NewRecorder()
	handleRemodelingRoomList(rec, httptest.NewRequest(http.MethodPost, "/v4/shop/interior/remodelingroomlist/URMEXT", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST status %d, want 404", rec.Code)
	}
}

// TestRemodelDataChecks: the operator file is Cherry-made, names canonical room codes, one row per tab and 1 to 3 floors.
func TestRemodelDataChecks(t *testing.T) {
	ok := RemodelRow{Tab: "URM2F", Code: "RUWA0006D", Name: "Cherry 2F room", RoomSize: 2}
	good := func(rows ...RemodelRow) *Remodel { return &Remodel{Origin: "cherry", Note: "Cherry-made.", Rows: rows} }
	if err := checkRemodel(good(ok)); err != nil {
		t.Fatalf("valid file refused: %v", err)
	}
	bad := map[string]*Remodel{
		"origin":   {Origin: "", Note: "Cherry-made.", Rows: []RemodelRow{ok}},
		"note":     {Origin: "cherry", Rows: []RemodelRow{ok}},
		"tab":      good(RemodelRow{Tab: "URM9F", Code: "RUWA0006D", Name: "x", RoomSize: 2}),
		"twice":    good(ok, ok),
		"code":     good(RemodelRow{Tab: "URM2F", Code: "CUTO0006W", Name: "x", RoomSize: 2}),
		"name":     good(RemodelRow{Tab: "URM2F", Code: "RUWA0006D", Name: " ", RoomSize: 2}),
		"roomSize": good(RemodelRow{Tab: "URM2F", Code: "RUWA0006D", Name: "x", RoomSize: 0}),
	}
	for name, m := range bad {
		if err := checkRemodel(m); err == nil {
			t.Fatalf("%s: refused nothing", name)
		}
	}
}
