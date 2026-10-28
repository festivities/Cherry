package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBannedKeywordShape(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/banned/keyword")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	// SetInhibitionWordList needs root.result to be an array of strings; null or an object is not accepted.
	var body struct {
		Result []string `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not {result: [strings]}: %v (%s)", err, rec.Body.String())
	}
	if body.Result == nil || len(body.Result) != 0 {
		t.Fatalf("result = %#v, want an empty array", body.Result)
	}
	if rec := serve(t, http.MethodPost, "/v4/banned/keyword"); rec.Code != http.StatusNotFound {
		t.Errorf("POST = %d, want 404", rec.Code)
	}
}
