package economy

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
)

// Shapes of the Gacha Shop page replies (spec gacha-pages sections 1, 3 and 4).
var (
	gachaCategoryShape = mergeShapes(
		shapeOf("int", "id"),
		shapeOf("string", "name image flag link nMarkTimestamp linkType desc"),
		shapeOf("bool", "nMark delimiter"),
		shapeOf("array", "newGachaNames repGachaList"))
	gachaRepGachaShape = shapeOf("string", "gachaName bgColor imgUrl")
	gachaKeywordShape  = mergeShapes(
		shapeOf("int", "keywordId keywordSeq"),
		shapeOf("string", "keywordGrp keywordNm"),
		shapeOf("bool", "specialYn likeYn"))
	gachaLikeItemShape = mergeShapes(
		shapeOf("int", "gachaId itemId"),
		shapeOf("string", "grade itemCode bgColor itemSpecialEffectsType representImagePath"))
)

// gachaListOf serves a list route and returns its result object, checked against the list shape.
func gachaListOf(t *testing.T, target string) map[string]any {
	t.Helper()
	rec := gachaCall(t, http.MethodGet, target, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", target, rec.Code, rec.Body)
	}
	res := gachaResultMap(t, rec.Body.Bytes())
	gachaShape(t, target, res, gachaListShape)
	return res
}

// gachaFirstRow returns the first row of a list result (the test has checked there is one).
func gachaFirstRow(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	items, _ := res["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no rows in %v", res)
	}
	return items[0].(map[string]any)
}

// TestGachaSublistCategory: home/sublist/gacha is an array of the one category row, with one tile whose picture is the card.
func TestGachaSublistCategory(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 0, nil)
	rec := gachaCall(t, http.MethodGet, "/v4/home/sublist/gacha/10.1.0.0/Android.nhn?parentIconId=&isMain=true", "", "")
	var env struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); rec.Code != http.StatusOK || err != nil || len(env.Result) != 1 {
		t.Fatalf("sublist = %d %s (%v), want an array of one row", rec.Code, rec.Body, err)
	}
	row, g := env.Result[0], ActiveGacha()
	gachaShape(t, "category row", row, gachaCategoryShape)
	if row["id"] != float64(g.Category.ID) || row["name"] != g.Category.Name || row["linkType"] != "goSomewhere(gachaCatg)" ||
		row["link"] != strconv.FormatInt(g.Category.ID, 10) {
		t.Errorf("category row = %v", row)
	}
	reps, _ := row["repGachaList"].([]any)
	if len(reps) != 1 {
		t.Fatalf("repGachaList = %v, want one tile", reps)
	}
	rep := reps[0].(map[string]any)
	gachaShape(t, "repGachaList[0]", rep, gachaRepGachaShape)
	if rep["gachaName"] != g.Name || rep["imgUrl"] != "cherry/gacha/"+gachaCardName("CUON004TV") {
		t.Errorf("tile = %v, want the gacha name and the card of its first RARE item", rep)
	}
}

// TestGachaCategoryAndKeywordIDs: catg/all/<id> and keyword/<n> give one row for the Cherry category and keyword and the
// empty list for every other id; the keyword list is an array of the one keyword row; catg/all/-1 stays [].
func TestGachaCategoryAndKeywordIDs(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 0, nil)
	g := ActiveGacha()
	res := gachaListOf(t, "/v4/shop/gachaList2/catg/all/"+strconv.FormatInt(g.Category.ID, 10))
	if len(res["items"].([]any)) != 1 || res["totalCount"] != "1" || res["categroyNm"] != g.Category.Name ||
		gachaFirstRow(t, res)["itemCode"] != "CUON004TV" {
		t.Errorf("category detail = %v, want one row", res)
	}
	res = gachaListOf(t, "/v4/shop/gachaList2/keyword/"+strconv.FormatInt(g.Keyword.ID, 10))
	if len(res["items"].([]any)) != 1 || res["totalCount"] != "1" || res["keywordNm"] != g.Keyword.Name {
		t.Errorf("keyword results = %v, want one row", res)
	}
	for _, id := range []string{"0", "2", "999"} {
		if n := len(gachaListOf(t, "/v4/shop/gachaList2/catg/all/"+id)["items"].([]any)); n != 0 {
			t.Errorf("catg/all/%s has %d rows, want 0", id, n)
		}
	}
	for _, id := range []string{"0", "1", "999"} {
		if n := len(gachaListOf(t, "/v4/shop/gachaList2/keyword/"+id)["items"].([]any)); n != 0 {
			t.Errorf("keyword/%s has %d rows, want 0", id, n)
		}
	}
	if rec := gachaCall(t, http.MethodGet, "/v4/shop/gachaList2/catg/all/-1", "", ""); rec.Body.String() != `{"result":[]}` {
		t.Errorf("catg/all/-1 = %s, want []", rec.Body)
	}
	rec := gachaCall(t, http.MethodGet, "/v4/shop/gachaList2/keyword", "", "")
	var env struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Result) != 1 {
		t.Fatalf("keyword list = %s (%v), want an array of one row", rec.Body, err)
	}
	gachaShape(t, "keyword row", env.Result[0], gachaKeywordShape)
	if env.Result[0]["keywordId"] != float64(g.Keyword.ID) || env.Result[0]["keywordNm"] != g.Keyword.Name || env.Result[0]["keywordGrp"] != "Etc" {
		t.Errorf("keyword row = %v", env.Result[0])
	}
}

// TestGachaLikesRareOnly: likes/popular/item is one group, named for the category, of the gacha's RARE items only.
func TestGachaLikesRareOnly(t *testing.T) {
	p := gachaFixture(t)
	gachaAccount(t, 0, nil)
	rec := gachaCall(t, http.MethodGet, "/v4/likes/popular/item", "", "")
	var env struct {
		Result []struct {
			CategoryName string           `json:"categoryName"`
			Items        []map[string]any `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); rec.Code != http.StatusOK || err != nil || len(env.Result) != 1 {
		t.Fatalf("likes = %d %s (%v), want one group", rec.Code, rec.Body, err)
	}
	if env.Result[0].CategoryName != ActiveGacha().Category.Name {
		t.Errorf("categoryName = %q", env.Result[0].CategoryName)
	}
	var codes []string
	for _, it := range env.Result[0].Items {
		gachaShape(t, "like item", it, gachaLikeItemShape)
		if it["grade"] != "R" || it["gachaId"] != float64(ActiveGacha().ID) || it["representImagePath"] != "" {
			t.Errorf("like item = %v", it)
		}
		codes = append(codes, it["itemCode"].(string))
	}
	if !slices.Equal(codes, gachaCodes(p.rare)) {
		t.Errorf("like codes %v, want the RARE tier %v", codes, gachaCodes(p.rare))
	}
}

// TestGachaLikesCapAndPool: the group is capped at gachaLikeMax, a pool with no RARE item gives [], and an animal's
// pool drops the human-only RARE item (the hair CUHA00001) that a human's keeps.
func TestGachaLikesCapAndPool(t *testing.T) {
	p := gachaFixture(t)
	var many gachaPool
	for i := range gachaLikeMax + 5 {
		many.rare = append(many.rare, gachaEntry{code: "RUZZ" + strconv.Itoa(i), rare: true})
	}
	if got := gachaLikesFor(many); len(got) != 1 || len(got[0].Items) != gachaLikeMax {
		t.Errorf("capped group = %+v, want one group of %d", got, gachaLikeMax)
	}
	if got := gachaLikesFor(gachaPool{normal: p.normal}); got == nil || len(got) != 0 {
		t.Errorf("no RARE item gave %v, want an empty (non-nil) result", got)
	}
	human := p
	human.rare = append(slices.Clone(p.rare), gachaEntry{code: "CUHA00001", rare: true})
	if got := gachaLikesFor(human); len(got) != 1 || !slices.Contains(gachaLikeCodes(got), "CUHA00001") {
		t.Fatalf("human group = %+v, want CUHA00001 listed", got)
	}
	for _, code := range gachaLikeCodes(gachaLikesFor(human.animalPool())) {
		if itemcode.HumanOnly(code) {
			t.Errorf("animal group lists human-only %s", code)
		}
	}
}

func gachaLikeCodes(groups []gachaLikeGroup) []string {
	var out []string
	for _, g := range groups {
		for _, it := range g.Items {
			out = append(out, it.ItemCode)
		}
	}
	return out
}

// TestGachaDetailMovedKeys: previewImageUrl, previewGardenImageUrl, newUpgradeItems and similarGachas sit in items[0]
// and not at the root; newUpgradeItems and similarGachas are empty arrays (no upgrade or similar rows are invented).
func TestGachaDetailMovedKeys(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 0, nil)
	rec := gachaCall(t, http.MethodGet, "/v4/shop/gacha/detail/target/1001", "", "")
	res := gachaResultMap(t, rec.Body.Bytes())
	for _, k := range []string{"previewImageUrl", "previewGardenImageUrl", "newUpgradeItems", "similarGachas"} {
		if _, ok := res[k]; ok {
			t.Errorf("root has %q, want it in items[0]", k)
		}
	}
	item := gachaFirstRow(t, res)
	gachaShape(t, "detail item", item, gachaDetailItemShape)
	for _, k := range []string{"newUpgradeItems", "similarGachas"} {
		if arr, ok := item[k].([]any); !ok || len(arr) != 0 {
			t.Errorf("items[0].%s = %v, want []", k, item[k])
		}
	}
	if !strings.Contains(rec.Body.String(), `"newUpgradeItems":[]`) {
		t.Errorf("newUpgradeItems is not the empty array in %s", rec.Body)
	}
}

// TestGachaCardPicture: the card is the first RARE item's art on a plain panel. The rows and the category tile carry its
// relative URL; GET serves a PNG of gachaCardSize with the art in the middle. Other names and methods are 404.
func TestGachaCardPicture(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 0, nil)
	url := "cherry/gacha/" + gachaCardName("CUON004TV")
	if got := gachaFirstRow(t, gachaListOf(t, "/v4/shop/gachaList2/top/all?deviceType=Android"))["gachaMainBannerImageUrl"]; got != url {
		t.Errorf("top row picture = %v, want %s", got, url)
	}
	rec := gachaCall(t, http.MethodGet, "/"+url, "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("card = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("card is not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != gachaCardSize || b.Dy() != gachaCardSize {
		t.Errorf("card is %dx%d, want %dx%d", b.Dx(), b.Dy(), gachaCardSize, gachaCardSize)
	}
	if r, g, _, _ := img.At(gachaCardSize/2, gachaCardSize/2).RGBA(); r < 0xc000 || g > 0x4000 {
		t.Errorf("centre pixel is not the item's red art")
	}
	if r, _, _, _ := img.At(2, 2).RGBA(); r>>8 > 0x80 {
		t.Errorf("corner pixel is not the plain panel")
	}
	for _, bad := range []struct {
		method, path string
	}{
		{http.MethodGet, "/cherry/gacha/CUON004TV-00000000.png"},        // wrong checksum
		{http.MethodGet, "/cherry/gacha/CUON004TV.png"},                 // no checksum
		{http.MethodGet, "/cherry/gacha/" + gachaCardName("CUHE00001")}, // a face: not in the gacha pool
		{http.MethodPost, "/" + url},
	} {
		if rec := gachaCall(t, bad.method, bad.path, "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", bad.method, bad.path, rec.Code)
		}
	}
}

// TestGachaCardWithoutStore: with no asset store the card URL is "" in every row and tile (an empty URL is skipped by the
// client's download, a picture Cherry cannot draw is never named).
func TestGachaCardWithoutStore(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 0, nil)
	old := assets.Archive
	assets.Archive = nil
	t.Cleanup(func() { assets.Archive = old })
	if got := gachaCardURL("CUON004TV"); got != "" {
		t.Errorf("gachaCardURL without the store = %q, want \"\"", got)
	}
	if got := gachaFirstRow(t, gachaListOf(t, "/v4/shop/gachaList2/top/all?deviceType=Android"))["gachaMainBannerImageUrl"]; got != "" {
		t.Errorf("top row picture without the store = %v", got)
	}
	rec := gachaCall(t, http.MethodGet, "/v4/home/sublist/gacha/10.1.0.0/Android.nhn?parentIconId=&isMain=true", "", "")
	if !strings.Contains(rec.Body.String(), `"imgUrl":""`) {
		t.Errorf("category tile without the store = %s", rec.Body)
	}
}

// TestGachaCheckNames: checkGacha accepts the shipped file and refuses an empty or blank category or keyword name and an
// id below 1.
func TestGachaCheckNames(t *testing.T) {
	base := *ActiveGacha()
	if err := checkGacha(&base); err != nil {
		t.Fatalf("shipped gacha refused: %v", err)
	}
	for label, mut := range map[string]func(*Gacha){
		"empty category name": func(g *Gacha) { g.Category.Name = "" },
		"blank category name": func(g *Gacha) { g.Category.Name = "  " },
		"category id 0":       func(g *Gacha) { g.Category.ID = 0 },
		"empty keyword name":  func(g *Gacha) { g.Keyword.Name = "" },
		"keyword id 0":        func(g *Gacha) { g.Keyword.ID = 0 },
	} {
		g := base
		mut(&g)
		if err := checkGacha(&g); err == nil {
			t.Errorf("%s: accepted", label)
		}
	}
}
