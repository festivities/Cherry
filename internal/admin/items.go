package admin

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/internal/auth"
	"cherry/internal/economy"
	"cherry/internal/store"
)

// The Item names page (/items): visual browser and inline renamer for every cosmetic item, face part, furniture piece,
// pet, Garden landscape and remodeling layout in Cherry's catalog. Accessible to Helpers, Moderators and the Owner.
// Every save and reset commits to item_names + audit in one transaction and takes effect live in-game without a restart.
// Items already promoted into data/item_names.json show as Built-in and count toward the named total; Export downloads
// the merged data/item_names.json format ready to commit directly into the repository.

const (
	itemsPerPage  = 48
	itemsQueryMax = 100
)

type itemGroupTab struct {
	Key     string
	Label   string
	Href    string
	Count   int
	Named   int
	Current bool
}

type itemCatOption struct {
	Key   string
	Label string
	Count int
}

type itemRow struct {
	Code          string
	ID            int64
	Group         string
	CategoryLabel string
	Species       string
	Name          string
	DefaultName   string // what Reset restores to (built-in data/item_names.json name if present, else synthetic fallback)
	Custom        bool   // live DB override differing from built-in default
	Builtin       bool   // present in committed data/item_names.json
	Animated      bool
	Converted     bool
	Updated       string
	UpdatedBy     string
	PreviewHref   string
}

type itemsPage struct {
	Total       int
	NamedTotal  int
	CustomTotal int // live DB overrides not yet identical to data/item_names.json
	NamedPct    string
	NamedBar    int // 0..100 in steps of 5 (matches .w-N CSS classes)
	Group       string
	Groups      []itemGroupTab
	Category    string
	Categories  []itemCatOption
	Status      string // "" (all), "unnamed", "named", "custom"
	Sort        string // "" (code), "recent"
	Query       string
	Filtered    int
	Page        int
	TotalPages  int
	Start       int
	End         int
	PrevHref    string
	NextHref    string
	ClearHref   string
	ReturnURL   string
	CanExport   bool
	ShowActor   bool
	Rows        []itemRow
}

type itemsFilter struct {
	Group    string
	Category string
	Status   string
	Sort     string
	Query    string
	Page     int
}

func validItemGroup(g string) bool {
	if g == "" {
		return true
	}
	for _, eg := range economy.ItemGroups {
		if eg.Key == g {
			return true
		}
	}
	return false
}

func itemsFilterOf(q url.Values) (itemsFilter, string) {
	f := itemsFilter{
		Group:    strings.TrimSpace(q.Get("group")),
		Category: strings.TrimSpace(q.Get("cat")),
		Status:   strings.TrimSpace(q.Get("status")),
		Sort:     strings.TrimSpace(q.Get("sort")),
		Query:    strings.TrimSpace(q.Get("q")),
		Page:     1,
	}
	if !validItemGroup(f.Group) {
		return f, "Unknown item group."
	}
	if f.Status != "" && f.Status != "unnamed" && f.Status != "named" && f.Status != "custom" {
		return f, "The status filter must be all, unnamed, named or custom."
	}
	if f.Sort != "" && f.Sort != "recent" {
		return f, "The sort must be code or recent."
	}
	if utf8.RuneCountInString(f.Query) > itemsQueryMax {
		return f, "The search is at most 100 characters."
	}
	if len(f.Category) > 20 || !onlyChars(f.Category, "_-") {
		return f, "The category filter is not valid."
	}
	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100000 {
			return f, "The page number is not valid."
		}
		f.Page = n
	}
	return f, ""
}

func itemsHref(f itemsFilter) string {
	q := url.Values{}
	if f.Group != "" {
		q.Set("group", f.Group)
	}
	if f.Category != "" {
		q.Set("cat", f.Category)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.Sort != "" {
		q.Set("sort", f.Sort)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Page > 1 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	if len(q) == 0 {
		return "/items"
	}
	return "/items?" + q.Encode()
}

func percentBar(part, total int) (pct string, bar int) {
	if total <= 0 {
		return "0%", 0
	}
	p := float64(part) * 100 / float64(total)
	if p > 0 && p < 0.1 {
		pct = "<0.1%"
	} else {
		pct = fmt.Sprintf("%.1f%%", p)
	}
	bar = int((p+2.5)/5) * 5
	if bar > 100 {
		bar = 100
	}
	if part > 0 && bar == 0 {
		bar = 5
	}
	return pct, bar
}

// resolveItemState classifies one catalog item against live DB overrides and built-in data/item_names.json defaults.
func resolveItemState(it economy.CatalogItem, overrides map[string]store.ItemNameRecord, builtins map[string]string) (display, defaultName string, custom, builtin bool, rec store.ItemNameRecord) {
	builtinName, hasBuiltin := builtins[it.Code]
	dbRec, hasDB := overrides[it.Code]

	defaultName = it.DefaultName
	if hasBuiltin {
		defaultName = builtinName
	}
	builtin = hasBuiltin
	custom = hasDB && (!hasBuiltin || dbRec.Name != builtinName)
	if custom {
		return dbRec.Name, defaultName, true, builtin, dbRec
	}
	return defaultName, defaultName, false, builtin, store.ItemNameRecord{}
}

// handleItems: GET /items. Lists items with previews and direct editable name inputs.
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request, sess session) {
	f, problem := itemsFilterOf(r.URL.Query())
	if problem != "" {
		s.fail(w, http.StatusBadRequest, problem)
		return
	}

	catalog := economy.CatalogItems()
	overrides := store.AllItemNameRecords()
	builtins := store.AllBuiltinItemNames()
	role := s.roleOf(sess.identity)
	showActor := role >= roleModerator

	// Tally group counts and overall named progress in one scan.
	groupCounts := map[string]int{}
	groupNamed := map[string]int{}
	namedTotal := 0
	customTotal := 0
	catCounts := map[string]int{}
	catLabels := map[string]string{}

	for _, it := range catalog {
		groupCounts[it.Group]++
		_, _, isCustom, isBuiltin, _ := resolveItemState(it, overrides, builtins)
		if isCustom {
			customTotal++
		}
		if isCustom || isBuiltin {
			groupNamed[it.Group]++
			namedTotal++
		}
		if f.Group == "" || it.Group == f.Group {
			catCounts[it.Category]++
			if _, ok := catLabels[it.Category]; !ok {
				catLabels[it.Category] = it.CategoryLabel
			}
		}
	}

	// Build group tabs: All + each declared group.
	tabs := make([]itemGroupTab, 0, len(economy.ItemGroups)+1)
	allTab := itemsFilter{Status: f.Status, Sort: f.Sort, Query: f.Query}
	tabs = append(tabs, itemGroupTab{
		Key: "", Label: "All", Href: itemsHref(allTab),
		Count: len(catalog), Named: namedTotal, Current: f.Group == "",
	})
	for _, g := range economy.ItemGroups {
		tf := itemsFilter{Group: g.Key, Status: f.Status, Sort: f.Sort, Query: f.Query}
		tabs = append(tabs, itemGroupTab{
			Key: g.Key, Label: g.Label, Href: itemsHref(tf),
			Count: groupCounts[g.Key], Named: groupNamed[g.Key], Current: f.Group == g.Key,
		})
	}

	// Build sorted subcategory dropdown options for the active group.
	cats := make([]itemCatOption, 0, len(catCounts))
	for k, cnt := range catCounts {
		cats = append(cats, itemCatOption{Key: k, Label: catLabels[k], Count: cnt})
	}
	slices.SortFunc(cats, func(a, b itemCatOption) int {
		if c := cmp.Compare(a.Label, b.Label); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})

	// Filter catalog items.
	needle := strings.ToLower(f.Query)
	type matchedItem struct {
		it          economy.CatalogItem
		rec         store.ItemNameRecord
		custom      bool
		builtin     bool
		display     string
		defaultName string
	}
	matched := make([]matchedItem, 0, min(len(catalog), 256))
	for _, it := range catalog {
		if f.Group != "" && it.Group != f.Group {
			continue
		}
		if f.Category != "" && it.Category != f.Category {
			continue
		}
		display, defName, isCustom, isBuiltin, rec := resolveItemState(it, overrides, builtins)
		isNamed := isCustom || isBuiltin
		switch f.Status {
		case "unnamed":
			if isNamed {
				continue
			}
		case "named":
			if !isNamed {
				continue
			}
		case "custom":
			if !isCustom {
				continue
			}
		}
		if needle != "" {
			if !strings.Contains(strings.ToLower(it.Code), needle) &&
				!strings.Contains(strconv.FormatInt(it.ID, 10), needle) &&
				!strings.Contains(strings.ToLower(display), needle) &&
				!strings.Contains(strings.ToLower(defName), needle) &&
				!strings.Contains(strings.ToLower(it.DefaultName), needle) &&
				!strings.Contains(strings.ToLower(it.CategoryLabel), needle) {
				continue
			}
		}
		matched = append(matched, matchedItem{
			it: it, rec: rec, custom: isCustom, builtin: isBuiltin,
			display: display, defaultName: defName,
		})
	}

	if f.Sort == "recent" {
		slices.SortFunc(matched, func(a, b matchedItem) int {
			if a.custom != b.custom {
				if a.custom {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(b.rec.Updated, a.rec.Updated); c != 0 {
				return c
			}
			return cmp.Compare(a.it.Code, b.it.Code)
		})
	}

	totalFiltered := len(matched)
	totalPages := max(1, (totalFiltered+itemsPerPage-1)/itemsPerPage)
	pageNum := min(f.Page, totalPages)
	startIdx := (pageNum - 1) * itemsPerPage
	endIdx := min(startIdx+itemsPerPage, totalFiltered)

	rows := make([]itemRow, 0, endIdx-startIdx)
	for _, m := range matched[startIdx:endIdx] {
		updated := ""
		updatedBy := ""
		if m.custom {
			updated = rfcDate(m.rec.Updated)
			if showActor {
				updatedBy = auth.MaskIdentity(m.rec.UpdatedBy)
			}
		}
		rows = append(rows, itemRow{
			Code:          m.it.Code,
			ID:            m.it.ID,
			Group:         m.it.Group,
			CategoryLabel: m.it.CategoryLabel,
			Species:       m.it.Species,
			Name:          m.display,
			DefaultName:   m.defaultName,
			Custom:        m.custom,
			Builtin:       m.builtin,
			Animated:      m.it.Animated,
			Converted:     m.it.Converted,
			Updated:       updated,
			UpdatedBy:     updatedBy,
			PreviewHref:   "/items/" + m.it.Code + "/preview",
		})
	}

	curFilter := f
	curFilter.Page = pageNum
	var prevHref, nextHref string
	if pageNum > 1 {
		pf := curFilter
		pf.Page = pageNum - 1
		prevHref = itemsHref(pf)
	}
	if pageNum < totalPages {
		nf := curFilter
		nf.Page = pageNum + 1
		nextHref = itemsHref(nf)
	}

	startShown, endShown := 0, 0
	if totalFiltered > 0 {
		startShown, endShown = startIdx+1, endIdx
	}
	pct, bar := percentBar(namedTotal, len(catalog))

	v := s.base(r, sess, "Item names")
	v.Scripts = []string{"items.js"}
	v.Items = itemsPage{
		Total:       len(catalog),
		NamedTotal:  namedTotal,
		CustomTotal: customTotal,
		NamedPct:    pct,
		NamedBar:    bar,
		Group:       f.Group,
		Groups:      tabs,
		Category:    f.Category,
		Categories:  cats,
		Status:      f.Status,
		Sort:        f.Sort,
		Query:       f.Query,
		Filtered:    totalFiltered,
		Page:        pageNum,
		TotalPages:  totalPages,
		Start:       startShown,
		End:         endShown,
		PrevHref:    prevHref,
		NextHref:    nextHref,
		ClearHref:   itemsHref(itemsFilter{Group: f.Group}),
		ReturnURL:   itemsHref(curFilter),
		CanExport:   role >= roleModerator,
		ShowActor:   showActor,
		Rows:        rows,
	}
	s.render(w, http.StatusOK, "items", v)
}

// handleItemPreview: GET /items/{code}/preview. Serves the item's PNG preview directly from Cherry's asset store /
// thumbnail generator under the staff origin (CSP-safe, never redirects off-origin).
func (s *Server) handleItemPreview(w http.ResponseWriter, r *http.Request, _ session) {
	code := r.PathValue("code")
	if !economy.ServeItemPreview(w, code) {
		http.NotFound(w, r)
	}
}

func safeItemsReturn(raw, notice string) string {
	loc := "/items"
	if strings.HasPrefix(raw, "/items") && validNext(raw) {
		loc = raw
	}
	u, err := url.Parse(loc)
	if err != nil {
		return "/items?notice=" + url.QueryEscape(notice)
	}
	q := u.Query()
	q.Set("notice", notice)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) itemJSONError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The database is not available."})
	case errors.Is(err, store.ErrBadInput):
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		s.logger.Printf("cherry: staff item name failed: %v", err)
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong."})
	}
}

type itemNameResponse struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DefaultName  string `json:"default_name"`
	Custom       bool   `json:"custom"`
	Builtin      bool   `json:"builtin"`
	Updated      string `json:"updated"`
	UpdatedBy    string `json:"updated_by,omitempty"`
	NamedTotal   int    `json:"named_total"`
	CustomTotal  int    `json:"custom_total"`
	CatalogTotal int    `json:"catalog_total"`
}

func countNamedInCatalog() (named, custom, total int) {
	catalog := economy.CatalogItems()
	overrides := store.AllItemNameRecords()
	builtins := store.AllBuiltinItemNames()
	for _, it := range catalog {
		_, _, isCustom, isBuiltin, _ := resolveItemState(it, overrides, builtins)
		if isCustom {
			custom++
		}
		if isCustom || isBuiltin {
			named++
		}
	}
	return named, custom, len(catalog)
}

// handleItemNameSave: POST /items/{code}/name. Saves a custom name for code, writes an audit row, and updates the live
// in-memory map immediately. Returns JSON when the caller asks for application/json (items.js), else redirects back.
func (s *Server) handleItemNameSave(w http.ResponseWriter, r *http.Request, sess session) {
	code := strings.TrimSpace(r.PathValue("code"))
	name := r.PostFormValue("name")
	rec, _, err := store.SetItemName(code, name, sess.identity, s.now())
	if err != nil {
		if wantsJSON(r) {
			s.itemJSONError(w, err)
			return
		}
		s.storeError(w, err)
		return
	}
	if wantsJSON(r) {
		named, customCount, total := countNamedInCatalog()
		_, hasBuiltin := store.BuiltinItemName(rec.Code)
		_, hasDB := store.ItemNameRecordOf(rec.Code)
		by := ""
		updated := ""
		if hasDB {
			updated = rfcDate(rec.Updated)
			if s.roleOf(sess.identity) >= roleModerator {
				by = auth.MaskIdentity(rec.UpdatedBy)
			}
		}
		respondJSON(w, http.StatusOK, itemNameResponse{
			Code:         rec.Code,
			Name:         rec.Name,
			DefaultName:  economy.DefaultItemName(rec.Code),
			Custom:       hasDB,
			Builtin:      hasBuiltin,
			Updated:      updated,
			UpdatedBy:    by,
			NamedTotal:   named,
			CustomTotal:  customCount,
			CatalogTotal: total,
		})
		return
	}
	redirect(w, http.StatusSeeOther, safeItemsReturn(r.PostFormValue("return"), "item-name-saved"))
}

// handleItemNameReset: POST /items/{code}/reset. Removes any live DB override so code returns to its built-in default
// (data/item_names.json) or synthetic fallback.
func (s *Server) handleItemNameReset(w http.ResponseWriter, r *http.Request, sess session) {
	code := strings.TrimSpace(r.PathValue("code"))
	if _, _, err := store.ResetItemName(code, sess.identity); err != nil {
		if wantsJSON(r) {
			s.itemJSONError(w, err)
			return
		}
		s.storeError(w, err)
		return
	}
	if wantsJSON(r) {
		named, customCount, total := countNamedInCatalog()
		_, hasBuiltin := store.BuiltinItemName(code)
		def := economy.DefaultItemName(code)
		respondJSON(w, http.StatusOK, itemNameResponse{
			Code:         code,
			Name:         def,
			DefaultName:  def,
			Custom:       false,
			Builtin:      hasBuiltin,
			NamedTotal:   named,
			CustomTotal:  customCount,
			CatalogTotal: total,
		})
		return
	}
	redirect(w, http.StatusSeeOther, safeItemsReturn(r.PostFormValue("return"), "item-name-reset"))
}

// handleItemsExport: GET /items/export.json (Moderator and Owner). Downloads the merged data/item_names.json file
// (existing built-ins + live DB overrides, sorted alphabetically) ready to drop into data/item_names.json and commit.
func (s *Server) handleItemsExport(w http.ResponseWriter, _ *http.Request, _ session) {
	payload, err := store.ExportItemNamesJSON()
	if err != nil {
		s.fail(w, http.StatusInternalServerError, "Could not encode item names.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="item_names.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}
