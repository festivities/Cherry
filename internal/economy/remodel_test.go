package economy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRemodelingRoomListEmptyForFloors: every floor tab (URMEXT, URM2F, URM3F) gets the same empty
// items array in the shape ParseRemodelingTable reads; non-GET is refused.
func TestRemodelingRoomListEmptyForFloors(t *testing.T) {
	for _, code := range []string{"URMEXT", "URM2F", "URM3F"} {
		rec := httptest.NewRecorder()
		handleRemodelingRoomList(rec, httptest.NewRequest(http.MethodGet, "/v4/shop/interior/remodelingroomlist/"+code+"?isFull=false", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"items":[]}}` {
			t.Fatalf("%s: status %d body %s", code, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	handleRemodelingRoomList(rec, httptest.NewRequest(http.MethodPost, "/v4/shop/interior/remodelingroomlist/URMEXT", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST status %d, want 404", rec.Code)
	}
}
