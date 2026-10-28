package economy

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
)

// Cherry Tip (home menu goSomewhere(cherrytip)): the tip list of the Cherry Tip popup. IDA 2026-10-28
// (menu-audit rows-d section 16): GET cherrytip/list/<InfoBundleVersion>/Android parses result as an array
// (NaDispatcherMoreView::ResCherryTipMenuList 0x1c10c80). Each element reads bannerId (asInt), title,
// description, image, eventPeriodTxt, linkType, link and dispStartYmdt (asString); the root reads timestamp.
// The picture goes through ChangeImageTexture 0x16a2520: a texture of that name when the client has one,
// otherwise NaDataManager::SetPNGImageWithURL with the string as the URL, so an image must be an absolute URL.

// The tips are Cherry-made, not original game data. data/cherrytips.json is the operator file (an operator
// file with the same name in %LOCALAPPDATA%\Cherry\config replaces it). Strict JSON: unknown keys are an error.
//
//	origin       must be "cherry"
//	note         says the tips are Cherry-made; text for operators, not sent to the client
//	tips         1 to 10 tips
//	  bannerId     a unique positive number
//	  title        the tip's heading
//	  description  the text under the heading
//	  image        "" (no picture) or an absolute https URL
type CherryTips struct {
	Origin string      `json:"origin"`
	Note   string      `json:"note"`
	Tips   []CherryTip `json:"tips"`
}

// CherryTip is one tip (see CherryTips).
type CherryTip struct {
	BannerID    int64  `json:"bannerId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

var cherryTipsFile = opdata.New("cherrytips.json", data.CherryTipsJSON, checkCherryTips, nil)

// ActiveCherryTips is the tip list in use.
func ActiveCherryTips() *CherryTips { return cherryTipsFile.Get() }

// checkCherryTips validates the structure: origin, note, 1 to 10 tips with unique positive ids, titles,
// descriptions, and images that are empty or https URLs.
func checkCherryTips(c *CherryTips) error {
	if c.Origin != "cherry" {
		return errors.New(`origin must be "cherry": the Cherry Tips are Cherry-made`)
	}
	if strings.TrimSpace(c.Note) == "" {
		return errors.New("note must say that the tips are Cherry-made")
	}
	if len(c.Tips) < 1 || len(c.Tips) > 10 {
		return errors.New("tips must hold 1 to 10 tips")
	}
	seen := map[int64]bool{}
	for i, t := range c.Tips {
		where := fmt.Sprintf("tips[%d] (bannerId %d)", i+1, t.BannerID)
		switch {
		case t.BannerID < 1 || seen[t.BannerID]:
			return fmt.Errorf("%s: bannerId must be a unique positive number", where)
		case strings.TrimSpace(t.Title) == "":
			return fmt.Errorf("%s: title must not be empty", where)
		case strings.TrimSpace(t.Description) == "":
			return fmt.Errorf("%s: description must not be empty", where)
		case t.Image != "" && !strings.HasPrefix(t.Image, "https://"):
			return fmt.Errorf("%s: image must be empty or an absolute https URL", where)
		}
		seen[t.BannerID] = true
	}
	return nil
}

// cherryTipRow is one element of the list. The popup reads the first four keys; the rest go out empty:
// no event period, no link (linkType "" and link ""), dispStartYmdt "0".
type cherryTipRow struct {
	BannerID       int64  `json:"bannerId"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Image          string `json:"image"`
	EventPeriodTxt string `json:"eventPeriodTxt"`
	LinkType       string `json:"linkType"`
	Link           string `json:"link"`
	DispStartYmdt  string `json:"dispStartYmdt"`
}

// cherryTipReply is the list reply: result is an array (the client walks it with size()), timestamp a string.
type cherryTipReply struct {
	Result    []cherryTipRow `json:"result"`
	Timestamp string         `json:"timestamp"`
}

// HandleCherryTipList serves GET cherrytip/list/<version>/<platform> (the version and platform do not
// change the list).
func HandleCherryTipList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rep := cherryTipReply{Result: []cherryTipRow{}, Timestamp: "0"}
	for _, t := range ActiveCherryTips().Tips {
		rep.Result = append(rep.Result, cherryTipRow{BannerID: t.BannerID, Title: t.Title, Description: t.Description, Image: t.Image, DispStartYmdt: "0"})
	}
	httpx.WriteObj(w, rep)
}
