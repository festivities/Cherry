package economy

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"cherry/data"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Garden Atelier (craft, Economy Phase DE): material designs turn gathered materials into goods for
// the Bag, clothes designs turn goods into catalog clothing for the Closet. Stamina regenerates over
// time, blueprints cost Smiles. Every invented value (themes, goods, recipes, stamina, prices) lives
// in the recipe file below; the wire layouts are in lpn/garden_craft.go and the spec
// %TEMP%\opencode\cherry\phaseDE-20261022\gather-craft-spec.md section 12.
//
// Recipe file (JSON, lower_snake keys; unknown keys are an error). The default is data/atelier.json,
// embedded in the binary. An operator file at %LOCALAPPDATA%\Cherry\config\atelier.json REPLACES it
// entirely; it is read once at startup and the choice is logged.
//
//	stamina   {max, regen_seconds}: stamina cap; one point back every regen_seconds (full = no timer)
//	themes[]  {id, kind "material"|"clothes", name, information, banner, icon}: banner and icon are
//	          relative client paths (craft01/...). A material theme holds material designs, a clothes
//	          theme holds clothes designs. Every theme needs at least one design.
//	goods[]   {id, name}: crafted Bag items (49xxxx). A material design makes one.
//	designs[] {id, theme, product_material | product_clothes + product_name, inputs[{item, count}],
//	          stamina, blueprint_smiles}: inputs are gathering drops or goods; blueprint_smiles 0 =
//	          unlocked from the start (every material design, usually).
//
// Cherry-made values: every name, text, id, price and count in the default file.

// Result codes of an Atelier request (NaCraftItemDataManager::GetErrorMsg, spec 12.1). 0 is success.
const (
	AtelierOK          = 0
	AtelierLockDesign  = 200193 // CRAFT_LOCK_DESIGN: a clothes design whose blueprint is not bought
	AtelierAlreadyOpen = 200194 // ALREADY_UNLOCK_DESIGN
	AtelierNotSmiles   = 200195 // OPEN_DESIGN_NOT_ENOUGH_P_POINT (also a Smile spend that fails)
	AtelierStamina     = 200197 // MAKE_PRODUCT_STAMINA_NOT_ENOUGH
	AtelierItems       = 200199 // MAKE_PRODUCT_ITEM_NOT_ENOUGH
	AtelierPowder      = 200201 // RATE_POWDER_CANT_USE: Lucky Powder is not in v1
	AtelierChoice      = 200204 // MAKE_PRODUCT_CHOICE_FAILED: a product code the design does not make
	AtelierRetry       = 200206 // RETRY_COUNT_OVER: retries are not in v1
	AtelierInvalid     = 200208 // MAKE_PRODUCT_INVALID: unknown design, owned clothing, wrong kind
	AtelierSave        = 200209 // MAKE_PRODUCT_LOGIC_ERROR: the account save failed, nothing changed
	AtelierRewardNone  = 200213 // THEME_REWARD_NOT_ENABLE_TAKE: theme rewards are not in v1
	AtelierShopNone    = 200214 // SHOP_NO_SHOP_DATA: the craft shop is not in v1
)

// AtelierMaxBatch caps the count of one material make (the client's make count).
const AtelierMaxBatch = 99

// Theme kinds in the recipe file.
const (
	AtelierKindMaterial = "material"
	AtelierKindClothes  = "clothes"
)

// Atelier is the parsed recipe file.
type Atelier struct {
	opdata.Provenance
	Stamina AtelierStaminaRule `json:"stamina"`
	Themes  []AtelierTheme     `json:"themes"`
	Goods   []AtelierGood      `json:"goods"`
	Designs []AtelierDesign    `json:"designs"`
}

type AtelierStaminaRule struct {
	Max          int64 `json:"max"`
	RegenSeconds int64 `json:"regen_seconds"`
}

type AtelierTheme struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Information string `json:"information"`
	Banner      string `json:"banner"`
	Icon        string `json:"icon"`
}

type AtelierGood struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type AtelierDesign struct {
	ID              int64          `json:"id"`
	Theme           int64          `json:"theme"`
	ProductMaterial int64          `json:"product_material,omitempty"`
	ProductClothes  string         `json:"product_clothes,omitempty"`
	ProductName     string         `json:"product_name,omitempty"`
	Inputs          []AtelierInput `json:"inputs"`
	Stamina         int64          `json:"stamina"`
	Price           int64          `json:"blueprint_smiles"`
}

type AtelierInput struct {
	Item  int64 `json:"item"`
	Count int64 `json:"count"`
}

// Material reports whether the design makes a Bag material (else it makes clothing).
func (d AtelierDesign) Material() bool { return d.ProductMaterial != 0 }

// Theme returns the theme with id.
func (a *Atelier) Theme(id int64) (AtelierTheme, bool) {
	return findFirst(a.Themes, func(t AtelierTheme) bool { return t.ID == id })
}

// Design returns the design with id.
func (a *Atelier) Design(id int64) (AtelierDesign, bool) {
	return findFirst(a.Designs, func(d AtelierDesign) bool { return d.ID == id })
}

// Good returns the crafted good with id.
func (a *Atelier) Good(id int64) (AtelierGood, bool) {
	return findFirst(a.Goods, func(g AtelierGood) bool { return g.ID == id })
}

// DesignsOf returns the designs of theme, in file order.
func (a *Atelier) DesignsOf(theme int64) []AtelierDesign {
	var out []AtelierDesign
	for _, d := range a.Designs {
		if d.Theme == theme {
			out = append(out, d)
		}
	}
	return out
}

// FirstMaterialTheme is the theme the Atelier opens on (0 when there is none).
func (a *Atelier) FirstMaterialTheme() int64 {
	for _, t := range a.Themes {
		if t.Kind == AtelierKindMaterial {
			return t.ID
		}
	}
	return 0
}

// ProductName is the name of what design makes.
func (a *Atelier) ProductName(d AtelierDesign) string {
	if d.Material() {
		if g, ok := a.Good(d.ProductMaterial); ok {
			return g.Name
		}
		return ""
	}
	return d.ProductName
}

// ClothesName is the name of the clothes design that makes code ("" when none does).
func (a *Atelier) ClothesName(code string) string {
	d, _ := findFirst(a.Designs, func(d AtelierDesign) bool { return d.ProductClothes == code })
	return d.ProductName
}

func findFirst[T any](xs []T, match func(T) bool) (T, bool) {
	for _, x := range xs {
		if match(x) {
			return x, true
		}
	}
	var zero T
	return zero, false
}

// atelierFile is the recipe file. Its startup check adds the item archive (checkAtelierProducts).
var atelierFile = opdata.New("atelier.json", data.AtelierJSON, (*Atelier).check, checkAtelierProducts)

// ActiveAtelier is the recipe set in use: the startup load, or the embedded default (tests).
func ActiveAtelier() *Atelier { return atelierFile.Get() }

// parseAtelier decodes and validates a recipe file (no archive checks; see checkAtelierProducts).
func parseAtelier(raw []byte) (*Atelier, error) {
	return opdata.Decode(raw, (*Atelier).check)
}

// CherryDesignFirst and CherryDesignLast bound the Atelier design ids (atelier.json). The themes 1012 and 1013 are
// original (their banners are in cache/craft01), but no archive holds a recipe or design: the designs are
// Cherry-made, numbered theme*100+n, and the block is reserved for them.
const (
	CherryDesignFirst int64 = 101201
	CherryDesignLast  int64 = 101399
)

func (a *Atelier) check() error {
	if err := a.CheckOrigin("mixed"); err != nil {
		return err
	}
	if a.Stamina.Max < 1 {
		return errors.New("stamina.max must be at least 1")
	}
	if a.Stamina.RegenSeconds < 1 {
		return errors.New("stamina.regen_seconds must be at least 1")
	}
	themeKind := map[int64]string{}
	material := false
	for _, t := range a.Themes {
		if _, dup := themeKind[t.ID]; dup {
			return fmt.Errorf("theme %d: duplicate id", t.ID)
		}
		switch t.Kind {
		case AtelierKindMaterial:
			material = true
		case AtelierKindClothes:
		default:
			return fmt.Errorf("theme %d: kind must be %q or %q, not %q", t.ID, AtelierKindMaterial, AtelierKindClothes, t.Kind)
		}
		if t.Name == "" || t.Banner == "" || t.Icon == "" {
			return fmt.Errorf("theme %d: name, banner and icon must not be empty", t.ID)
		}
		themeKind[t.ID] = t.Kind
	}
	if !material {
		return errors.New("no material theme: the Atelier opens on one")
	}
	goods := map[int64]bool{}
	for _, g := range a.Goods {
		if g.ID < 1 || goods[g.ID] {
			return fmt.Errorf("good %d: id must be unique and positive", g.ID)
		}
		if _, gathered := gatherDropByID(g.ID); gathered {
			return fmt.Errorf("good %d: id is a gathering drop", g.ID)
		}
		if g.Name == "" {
			return fmt.Errorf("good %d: name must not be empty", g.ID)
		}
		goods[g.ID] = true
	}
	designs := map[int64]bool{}
	hasDesign := map[int64]bool{}
	products := map[string]int64{}
	for _, d := range a.Designs {
		if !inCherryBlock(d.ID, CherryDesignFirst, CherryDesignLast) || designs[d.ID] {
			return fmt.Errorf("design %d: id must be unique and inside the Cherry block %d-%d", d.ID, CherryDesignFirst, CherryDesignLast)
		}
		kind, ok := themeKind[d.Theme]
		if !ok {
			return fmt.Errorf("design %d: theme %d is not in themes", d.ID, d.Theme)
		}
		if d.Material() == (d.ProductClothes != "") {
			return fmt.Errorf("design %d: set exactly one of product_material and product_clothes", d.ID)
		}
		if d.Material() {
			if kind != AtelierKindMaterial {
				return fmt.Errorf("design %d: a material product needs a material theme (theme %d is %s)", d.ID, d.Theme, kind)
			}
			if !goods[d.ProductMaterial] {
				return fmt.Errorf("design %d: product_material %d is not in goods", d.ID, d.ProductMaterial)
			}
		} else {
			if kind != AtelierKindClothes {
				return fmt.Errorf("design %d: a clothing product needs a clothes theme (theme %d is %s)", d.ID, d.Theme, kind)
			}
			if d.ProductName == "" {
				return fmt.Errorf("design %d: product_name must not be empty for clothing", d.ID)
			}
			if prev, dup := products[d.ProductClothes]; dup {
				return fmt.Errorf("design %d: product %s is also made by design %d", d.ID, d.ProductClothes, prev)
			}
			products[d.ProductClothes] = d.ID
		}
		if len(d.Inputs) == 0 {
			return fmt.Errorf("design %d: needs at least one input", d.ID)
		}
		seen := map[int64]bool{}
		for _, in := range d.Inputs {
			if in.Count < 1 {
				return fmt.Errorf("design %d: input %d count %d must be at least 1", d.ID, in.Item, in.Count)
			}
			if seen[in.Item] {
				return fmt.Errorf("design %d: input %d is listed twice", d.ID, in.Item)
			}
			seen[in.Item] = true
			_, gathered := gatherDropByID(in.Item)
			if !gathered && !goods[in.Item] {
				return fmt.Errorf("design %d: input %d is neither a gathering drop nor a good in goods", d.ID, in.Item)
			}
		}
		if d.Stamina < 0 {
			return fmt.Errorf("design %d: stamina must not be negative", d.ID)
		}
		if d.Price < 0 {
			return fmt.Errorf("design %d: blueprint_smiles must not be negative", d.ID)
		}
		designs[d.ID] = true
		hasDesign[d.Theme] = true
	}
	for _, t := range a.Themes {
		if !hasDesign[t.ID] {
			return fmt.Errorf("theme %d has no designs (the client faults on an empty theme)", t.ID)
		}
	}
	return nil
}

// checkAtelierProducts refuses a clothing product the item archive cannot serve (not clothing, not
// priced, or without art, the same rule as the shops and grants).
func checkAtelierProducts(a *Atelier) error {
	for _, d := range a.Designs {
		if d.Material() {
			continue
		}
		if _, kind := classify(d.ProductClothes); kind != kindFashion {
			return fmt.Errorf("design %d: product %s is not clothing", d.ID, d.ProductClothes)
		}
		if !itemAvailable(d.ProductClothes) {
			return fmt.Errorf("design %d: product %s has no art in the item archive (%s)", d.ID, d.ProductClothes, thumbs.DpItemRoot)
		}
	}
	return nil
}

// ClothingItem is the catalog id and the relative picture path of a clothing code; ok is false when
// the code is not in the catalog.
func ClothingItem(code string) (id int64, image string, ok bool) {
	it := loadCatalog().byCode[code]
	if it == nil {
		return 0, "", false
	}
	kind, sid, _ := itemThumbRef(code)
	return it.ID, "arts_item_" + kind + "_" + sid + "/dp.png", true
}

// AtelierClothesChoice checks a clothing make's product code and Lucky Powder (not in v1). It
// returns a result code, AtelierOK when the choice is one the design makes.
func AtelierClothesChoice(design int64, code string, powder int64) int64 {
	d, ok := ActiveAtelier().Design(design)
	switch {
	case !ok || d.Material():
		return AtelierInvalid
	case powder != 0:
		return AtelierPowder
	case code != "" && code != d.ProductClothes:
		return AtelierChoice
	}
	return AtelierOK
}

// AtelierSnap is one account's Atelier state at a moment: stamina and its timer, the Smile balance,
// the Bag counts and the bought blueprints.
type AtelierSnap struct {
	Stamina, StaminaMax, RemainSec, ChargeSec int64
	Smiles                                    int64
	Counts                                    map[int64]int64
	Blueprints                                []int64
	Owned                                     map[string]bool // clothing products the account already owns
}

// Unlocked reports whether the account may open design without a blueprint purchase: free designs,
// bought ones, and clothing the account already owns (no blueprint is sold for it; the make is
// refused anyway, so Smiles are never spent on it).
func (s AtelierSnap) Unlocked(d AtelierDesign) bool {
	return d.Price == 0 || slices.Contains(s.Blueprints, d.ID) || s.Owned[d.ProductClothes]
}

// Enough reports whether the Bag covers count batches of inputs.
func (s AtelierSnap) Enough(inputs []AtelierInput, count int64) bool {
	for _, in := range inputs {
		if s.Counts[in.Item] < in.Count*count {
			return false
		}
	}
	return true
}

// AtelierStatus is aid's Atelier state at now (full stamina for an unknown account).
func AtelierStatus(aid string, now time.Time) AtelierSnap {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return atelierSnapLocked(store.AccountByAidLocked(aid), ActiveAtelier(), now)
}

// atelierSnapLocked is the state of acc (nil: a full stamina bar and nothing else). Caller holds accountsMu.
func atelierSnapLocked(acc *store.Account, a *Atelier, now time.Time) AtelierSnap {
	cur, at := atelierStamina(acc, a, now)
	s := AtelierSnap{Stamina: cur, StaminaMax: a.Stamina.Max, ChargeSec: a.Stamina.RegenSeconds, Counts: map[int64]int64{}}
	if cur < a.Stamina.Max {
		s.RemainSec = max(at+a.Stamina.RegenSeconds*1000-now.UnixMilli(), 0) / 1000
	}
	if acc == nil {
		return s
	}
	s.Smiles = acc.Smiles
	s.Owned = map[string]bool{}
	for _, d := range a.Designs {
		if !d.Material() && itemOwnedLocked(acc, d.ProductClothes) {
			s.Owned[d.ProductClothes] = true
		}
	}
	for _, st := range acc.GardenBag {
		s.Counts[st.Item] = st.Count
	}
	if acc.Atelier != nil {
		s.Blueprints = acc.Atelier.Blueprints
	}
	return s
}

// atelierStamina is acc's stamina at now and the timer base to store with it. Full stamina (or a
// record that has none) keeps no timer, so the timer starts at now when a make takes it below full;
// a partial point keeps its progress.
func atelierStamina(acc *store.Account, a *Atelier, now time.Time) (cur, at int64) {
	nowMs := now.UnixMilli()
	limit, period := a.Stamina.Max, a.Stamina.RegenSeconds*1000
	if acc == nil || acc.Atelier == nil || acc.Atelier.Stamina >= limit {
		return limit, nowMs
	}
	st := acc.Atelier
	n := max(nowMs-st.StaminaAt, 0) / period
	cur = min(limit, st.Stamina+n)
	if cur == limit {
		return limit, nowMs
	}
	return cur, st.StaminaAt + n*period
}

// atelierRecord is the Atelier record with stamina cur (timer base at) that keeps acc's blueprints
// and adds bought. Call it before acc.Atelier is replaced.
func atelierRecord(acc *store.Account, cur, at int64, bought ...int64) *store.AtelierState {
	var bp []int64
	if acc.Atelier != nil {
		bp = slices.Clone(acc.Atelier.Blueprints)
	}
	return &store.AtelierState{Stamina: cur, StaminaAt: at, Blueprints: append(bp, bought...)}
}

// AtelierOpenDesign buys the blueprint of design with Smiles (ch 32). A free or already bought
// design answers AtelierAlreadyOpen; too few Smiles answers AtelierNotSmiles. The result snapshot
// is the state after the call.
func AtelierOpenDesign(aid string, design int64, now time.Time) (int64, AtelierSnap) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	a := ActiveAtelier()
	acc := store.AccountByAidLocked(aid)
	d, ok := a.Design(design)
	if acc == nil || !ok {
		return AtelierInvalid, atelierSnapLocked(acc, a, now)
	}
	snap := atelierSnapLocked(acc, a, now)
	if snap.Unlocked(d) {
		return AtelierAlreadyOpen, snap
	}
	if acc.Smiles < d.Price {
		return AtelierNotSmiles, snap
	}
	prev := *acc
	cur, at := atelierStamina(acc, a, now)
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Smiles: -d.Price}, "atelier:blueprint:"+strconv.FormatInt(d.ID, 10))
	if err != nil {
		*acc = prev
		return AtelierNotSmiles, snap
	}
	acc.Atelier = atelierRecord(acc, cur, at, d.ID)
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return AtelierSave, snap
	}
	store.FlushLedgerLocked(lines)
	return AtelierOK, atelierSnapLocked(acc, a, now)
}

// AtelierMakeMaterial makes count batches of a material design (ch 33): the inputs leave the Bag,
// the goods join it and the stamina is spent, all or nothing.
func AtelierMakeMaterial(aid string, design, count int64, now time.Time) (int64, AtelierSnap) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	a := ActiveAtelier()
	acc := store.AccountByAidLocked(aid)
	d, ok := a.Design(design)
	if acc == nil || !ok || !d.Material() || count < 1 || count > AtelierMaxBatch {
		return AtelierInvalid, atelierSnapLocked(acc, a, now)
	}
	snap := atelierSnapLocked(acc, a, now)
	switch {
	case !snap.Unlocked(d):
		return AtelierLockDesign, snap
	case snap.Stamina < d.Stamina*count:
		return AtelierStamina, snap
	case !snap.Enough(d.Inputs, count):
		return AtelierItems, snap
	}
	prev := *acc
	bag := acc.GardenBag
	for _, in := range d.Inputs {
		bag = gardenBagChange(bag, in.Item, -in.Count*count, now)
	}
	bag = gardenBagChange(bag, d.ProductMaterial, count, now)
	cur, at := atelierStamina(acc, a, now)
	acc.GardenBag = bag
	acc.Atelier = atelierRecord(acc, cur-d.Stamina*count, at)
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return AtelierSave, snap
	}
	return AtelierOK, atelierSnapLocked(acc, a, now)
}

// AtelierMakeClothes makes one clothing item of a clothes design (ch 35) into the Closet, for the
// stamina and inputs. Clothing the account already owns, or hair and a human face for an ANIMAL, is
// refused: no re-craft, no Gem payout, and no inputs spent.
func AtelierMakeClothes(aid string, design int64, now time.Time) (int64, AtelierSnap) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	a := ActiveAtelier()
	acc := store.AccountByAidLocked(aid)
	d, ok := a.Design(design)
	if acc == nil || !ok || d.Material() {
		return AtelierInvalid, atelierSnapLocked(acc, a, now)
	}
	snap := atelierSnapLocked(acc, a, now)
	switch {
	case !snap.Unlocked(d):
		return AtelierLockDesign, snap
	case itemOwnedLocked(acc, d.ProductClothes) || acc.Gender == "ANIMAL" && itemcode.HumanOnly(d.ProductClothes):
		return AtelierInvalid, snap
	case snap.Stamina < d.Stamina:
		return AtelierStamina, snap
	case !snap.Enough(d.Inputs, 1):
		return AtelierItems, snap
	}
	prev := *acc
	bag := acc.GardenBag
	for _, in := range d.Inputs {
		bag = gardenBagChange(bag, in.Item, -in.Count, now)
	}
	cur, at := atelierStamina(acc, a, now)
	acc.GardenBag = bag
	acc.Atelier = atelierRecord(acc, cur-d.Stamina, at)
	if _, err := itemGrantLocked(acc, d.ProductClothes); err != nil {
		*acc = prev
		return AtelierInvalid, snap
	}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		return AtelierSave, snap
	}
	return AtelierOK, atelierSnapLocked(acc, a, now)
}
