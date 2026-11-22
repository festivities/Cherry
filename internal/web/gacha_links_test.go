package web

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/economy"
)

// TestGachaHomeRowLinks: the gacha home rows carry the link values of spec gacha-pages section 6 (gachaDetail the gacha id,
// the two category rows the category id); every other row sends an empty link.
func TestGachaHomeRowLinks(t *testing.T) {
	var body struct {
		Result struct {
			HomeIconList []struct {
				LinkType string `json:"linkType"`
				Link     string `json:"link"`
			} `json:"homeIconList"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(buildHomeListExtBody()), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"goSomewhere(gachaDetail)":    strconv.FormatInt(economy.ActiveGacha().ID, 10),
		"goSomewhere(gachaCatg)":      strconv.FormatInt(economy.ActiveGacha().Category.ID, 10),
		"goSomewhere(gachaCatgNotap)": strconv.FormatInt(economy.ActiveGacha().Category.ID, 10),
	}
	seen := map[string]bool{}
	for _, row := range body.Result.HomeIconList {
		if !strings.Contains(row.LinkType, "gacha") {
			if row.Link != "" {
				t.Errorf("%s has link %q, want none", row.LinkType, row.Link)
			}
			continue
		}
		seen[row.LinkType] = true
		if got, ok := want[row.LinkType]; ok && row.Link != got {
			t.Errorf("%s link = %q, want %q", row.LinkType, row.Link, got)
		} else if !ok && row.Link != "" {
			t.Errorf("%s link = %q, want none", row.LinkType, row.Link)
		}
	}
	for link := range want {
		if !seen[link] {
			t.Errorf("home menu has no %s row", link)
		}
	}
}
