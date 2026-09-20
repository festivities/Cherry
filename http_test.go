package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type setInitConfResult struct {
	SessionServerInfo string   `json:"sessionServerInfo"`
	StaticDomain      string   `json:"staticDomain"`
	NationCode        string   `json:"nationCode"`
	IsGdprNation      bool     `json:"isGdprNation"`
	SnsLoginUIList    []string `json:"snsLoginUIList"`
	SnsSignUpUIList   []string `json:"snsSignUpUIList"`
}

type setInitConfResponse struct {
	Result *setInitConfResult `json:"result"`
}

func serve(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestSetInitConf(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/setInitConf")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json; charset=utf-8")
	}

	var raw struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	for _, field := range []string{"sessionServerInfo", "staticDomain", "nationCode", "isGdprNation", "snsLoginUIList", "snsSignUpUIList"} {
		if _, ok := raw.Result[field]; !ok {
			t.Errorf("result field %q missing", field)
		}
	}
	if len(raw.Result) != 6 {
		t.Errorf("result has %d fields, want 6", len(raw.Result))
	}

	var body setInitConfResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if body.Result == nil {
		t.Fatal("result missing")
	}
	want := setInitConfResult{
		SessionServerInfo: "XPN:/p=XTCP;ip=session.play.naver.jp;port=10123",
		StaticDomain:      "https://play-static.line-scdn.net/",
		NationCode:        "JP",
		IsGdprNation:      false,
		SnsLoginUIList:    []string{"LD_GUEST"},
		SnsSignUpUIList:   []string{"LD_GUEST"},
	}
	if body.Result.SessionServerInfo != want.SessionServerInfo {
		t.Errorf("sessionServerInfo = %q, want %q", body.Result.SessionServerInfo, want.SessionServerInfo)
	}
	if body.Result.StaticDomain != want.StaticDomain {
		t.Errorf("staticDomain = %q, want %q", body.Result.StaticDomain, want.StaticDomain)
	}
	if body.Result.NationCode != want.NationCode {
		t.Errorf("nationCode = %q, want %q", body.Result.NationCode, want.NationCode)
	}
	if body.Result.IsGdprNation != want.IsGdprNation {
		t.Errorf("isGdprNation = %v, want %v", body.Result.IsGdprNation, want.IsGdprNation)
	}
	if len(body.Result.SnsLoginUIList) != 1 || body.Result.SnsLoginUIList[0] != "LD_GUEST" {
		t.Errorf("snsLoginUIList = %v, want [LD_GUEST]", body.Result.SnsLoginUIList)
	}
	if len(body.Result.SnsSignUpUIList) != 1 || body.Result.SnsSignUpUIList[0] != "LD_GUEST" {
		t.Errorf("snsSignUpUIList = %v, want [LD_GUEST]", body.Result.SnsSignUpUIList)
	}
}

func TestSetInitConfFixtureByteIdentical(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/setInitConf")
	if got := rec.Body.String(); got != setInitConfBody {
		t.Fatalf("body = %q, want %q", got, setInitConfBody)
	}
}

func TestUnknownRoute(t *testing.T) {
	for _, target := range []string{"/", "/v4/checkSession", "/v4/setInitConf/nested"} {
		rec := serve(t, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", target, rec.Code, http.StatusNotFound)
		}
		if got := rec.Body.String(); got != notFoundBody {
			t.Errorf("%s: body = %q, want %q", target, got, notFoundBody)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want %q", target, ct, "application/json; charset=utf-8")
		}
	}
}

func TestWrongMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
		rec := serve(t, method, "/v4/setInitConf")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", method, rec.Code, http.StatusNotFound)
		}
		if got := rec.Body.String(); got != notFoundBody {
			t.Errorf("%s: body = %q, want %q", method, got, notFoundBody)
		}
	}
}
