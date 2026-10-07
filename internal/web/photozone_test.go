package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cherry/internal/economy"
)

func TestPhotoZoneShopInfo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/photozone/shop/info/", economy.HandlePhotoZoneShopInfo)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/v4/photozone/shop/info/comic_shop/en")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"categoryId", "shopId", "shopType", "shopFlag", "payType", "shopName", "endDate"} {
		var s string
		if json.Unmarshal(out.Result[k], &s) != nil {
			t.Errorf("%s not a string", k)
		}
	}
	for _, k := range []string{"subjectType", "originPrice", "discountAmount", "totalPrice"} {
		var n int
		if json.Unmarshal(out.Result[k], &n) != nil {
			t.Errorf("%s not an int", k)
		}
	}
	var flag bool
	if json.Unmarshal(out.Result["shopFlagYn"], &flag) != nil || flag {
		t.Error("shopFlagYn must be false bool")
	}
	var frames []map[string]any
	if json.Unmarshal(out.Result["frameList"], &frames) != nil || len(frames) != len(economy.PhotoZoneFrames) {
		t.Fatalf("frameList %v", frames)
	}
	for _, f := range frames {
		for _, k := range []string{"frameId", "displayEndDate", "displayImagePath", "displayImagefiles"} {
			if _, ok := f[k].(string); !ok {
				t.Errorf("frame %v: %s not a string", f["frameId"], k)
			}
		}
		if _, ok := f["flagDisplayYn"].(bool); !ok {
			t.Error("flagDisplayYn not bool")
		}
	}
	var actions []struct {
		ID int `json:"actionId"`
	}
	if json.Unmarshal(out.Result["actionList"], &actions) != nil || len(actions) == 0 {
		t.Error("actionList")
	}
	var pets []string
	if json.Unmarshal(out.Result["petActionList"], &pets) != nil || pets == nil {
		t.Error("petActionList must be an array")
	}
	for _, p := range []string{"/v4/photozone/shop/info/other/en", "/v4/photozone/shop/info/comic_shop/"} {
		if get(p).Code != 404 {
			t.Errorf("%s should 404", p)
		}
	}
}

func TestBadgeReset(t *testing.T) {
	for _, c := range []struct {
		method, path string
		code         int
	}{{"POST", "/v4/r/badge/reset/CSET", 200}, {"GET", "/v4/r/badge/reset/CSET", 404}, {"POST", "/v4/r/badge/reset/", 404}} {
		rec := decorReq(t, "", c.method, c.path, "")
		if rec.Code != c.code || (c.code == 200 && rec.Body.String() != `{"result":{}}`) {
			t.Fatalf("%s %s = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}
