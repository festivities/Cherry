package admin

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"cherry/internal/auth"
	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/web"
)

// The Garden buttons page (owner only): the event buttons the game draws in the Garden's right-hand column
// (home/integrated/info home_icon; spec garden-buttons-20261128). The owner picks a picture from the client's icon_effect
// animation, a label for the staff and the screen the button opens: a home menu row or a web page (openWithBrowser).
// The left column below the player picture holds only the client's built-in buttons, which no server value changes.
// Custom pictures (Cherry-made, uploaded here) are kept apart from the originals: their own section, their own store
// (store/gardenicons.go) and their own delivery (economy/gardenicons.go).

const (
	actionWebPage     = "web"
	gardenIconBodyCap = 1<<20 + 64<<10 // one PNG of at most 1 MiB plus the form fields
)

type gardenButtonRow struct {
	Icon, Preview, Label, Action, Added, By string
}

type gardenCustomRow struct {
	ID, Key, Name, Size, Added, By string
	Live, Used                     bool
}

type gardenActionOption struct {
	Value, Label string
}

type gardenButtonsPage struct {
	Buttons []gardenButtonRow
	Full    bool     // the panel keeps GardenButtonsMax already
	Icons   []string // the original pictures to choose from; empty when the animation file cannot be read
	Actions []gardenActionOption
	Max     int

	Custom     []gardenCustomRow // the custom pictures, in upload order
	CustomLive []gardenCustomRow // those a button can use now
	CustomOff  string            // why custom pictures cannot reach the clients; "" when they can
	CustomFull bool
	CustomMax  int
}

// gardenIconPreview is the panel URL of a button picture: an uploaded one or an original still.
func gardenIconPreview(icon string) string {
	if id, ok := strings.CutPrefix(icon, economy.GardenIconKey("")); ok {
		return "/garden-buttons/custom/" + id
	}
	return "/garden-buttons/icon/" + icon
}

func (s *Server) gardenOwner(w http.ResponseWriter, sess session) bool {
	if !s.isOwner(sess) {
		s.fail(w, http.StatusForbidden, "Only the owner can change the Garden buttons.")
		return false
	}
	return true
}

// handleGardenButtons: GET /garden-buttons (owner only).
func (s *Server) handleGardenButtons(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.gardenOwner(w, sess) {
		return
	}
	list, err := store.GardenButtonList()
	if err != nil {
		s.storeError(w, err)
		return
	}
	labels := map[string]string{economy.LinkBrowser: "Web page"}
	page := &gardenButtonsPage{Full: len(list) >= store.GardenButtonsMax, Max: store.GardenButtonsMax}
	for _, m := range web.MenuLinks() {
		labels[m.Link] = m.Label
		page.Actions = append(page.Actions, gardenActionOption{Value: m.Link, Label: m.Label})
	}
	page.Actions = append(page.Actions, gardenActionOption{Value: actionWebPage, Label: "Open a web page (URL below)"})
	for _, b := range list {
		action := labels[b.Link]
		if action == "" {
			action = b.Link
		}
		if b.Link == economy.LinkBrowser {
			action += ": " + b.Detail
		}
		page.Buttons = append(page.Buttons, gardenButtonRow{Icon: b.Icon, Preview: gardenIconPreview(b.Icon), Label: b.Label, Action: action, Added: b.Added, By: auth.MaskIdentity(b.By)})
	}
	if economy.GardenIconsOff() != "" {
		_ = economy.RefreshGardenIcons() // a startup failure (the bucket briefly away) is retried here
	}
	page.CustomOff, page.CustomMax = economy.GardenIconsOff(), store.GardenIconsMax
	custom, err := store.GardenIconList()
	if err != nil {
		s.storeError(w, err)
		return
	}
	page.CustomFull = len(custom) >= store.GardenIconsMax
	for _, ic := range custom {
		key := economy.GardenIconKey(ic.ID)
		row := gardenCustomRow{ID: ic.ID, Key: key, Name: ic.Name, Size: fmt.Sprintf("%d×%d px, %.1f KiB", ic.Width, ic.Height, float64(ic.Bytes)/1024),
			Added: ic.Added, By: auth.MaskIdentity(ic.By), Live: economy.GardenIconLive(key)}
		for _, b := range list {
			row.Used = row.Used || b.Icon == key
		}
		page.Custom = append(page.Custom, row)
		if row.Live {
			page.CustomLive = append(page.CustomLive, row)
		}
	}
	if icons, err := economy.IconEffectSymbols(); err != nil {
		s.logger.Printf("cherry: garden buttons: icon list: %v", err)
	} else {
		page.Icons = icons
	}
	v := s.base(r, sess, "Garden buttons")
	v.GardenButtons = page
	s.render(w, http.StatusOK, "gardenbuttons", v)
}

// handleGardenIcon: GET /garden-buttons/icon/{name} (owner only): the preview still of one picture.
func (s *Server) handleGardenIcon(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.isOwner(sess) {
		http.NotFound(w, r)
		return
	}
	b, ok := economy.IconEffectPreview(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(b)
}

// handleGardenButtonAdd: POST /garden-buttons/add (privileged, owner). The form: icon, label, action (a home menu link
// type or "web") and url (for "web").
func (s *Server) handleGardenButtonAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.gardenOwner(w, sess) {
		return
	}
	icon := r.PostFormValue("icon")
	if !economy.IsIconEffectSymbol(icon) && !economy.GardenIconLive(icon) {
		s.fail(w, http.StatusBadRequest, "Choose one of the pictures shown.")
		return
	}
	b := store.GardenButton{Icon: icon, Label: r.PostFormValue("label")}
	switch action := r.PostFormValue("action"); action {
	case actionWebPage:
		u := strings.TrimSpace(r.PostFormValue("url"))
		if err := economy.CheckBrowserURL(u); err != nil {
			s.fail(w, http.StatusBadRequest, "The web page address "+err.Error()+".")
			return
		}
		b.Link, b.Detail = economy.LinkBrowser, u
	default:
		for _, m := range web.MenuLinks() {
			if m.Link == action {
				b.Link, b.Detail = m.Link, m.Detail
			}
		}
		if b.Link == "" {
			s.fail(w, http.StatusBadRequest, "Choose what the button opens.")
			return
		}
	}
	if err := store.AddGardenButton(b, sess.identity, s.now()); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/garden-buttons?notice=garden-button-added")
}

// handleGardenButtonRemove: POST /garden-buttons/remove (privileged, owner). The form: icon.
func (s *Server) handleGardenButtonRemove(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.gardenOwner(w, sess) {
		return
	}
	if err := store.RemoveGardenButton(r.PostFormValue("icon"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/garden-buttons?notice=garden-button-removed")
}

// handleGardenCustomIcon: GET /garden-buttons/custom/{id} (owner only): an uploaded picture.
func (s *Server) handleGardenCustomIcon(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.isOwner(sess) {
		http.NotFound(w, r)
		return
	}
	b, err := store.GardenIconPNG(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(b)
}

// handleGardenIconAdd: POST /garden-buttons/pictures/add (privileged, owner; multipart). The form: name and png (a PNG
// file). The picture is normalised (economy.NormalizeGardenIcon), stored, and the clients' lists are rebuilt.
func (s *Server) handleGardenIconAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.gardenOwner(w, sess) {
		return
	}
	if economy.GardenIconsOff() != "" {
		_ = economy.RefreshGardenIcons()
	}
	if off := economy.GardenIconsOff(); off != "" {
		s.fail(w, http.StatusConflict, "Custom pictures are off: "+off+".")
		return
	}
	f, _, err := r.FormFile("png")
	if err != nil {
		s.fail(w, http.StatusBadRequest, "Choose a PNG file to upload.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	f.Close()
	if err != nil {
		s.fail(w, http.StatusBadRequest, "The file could not be read.")
		return
	}
	b, id, width, height, err := economy.NormalizeGardenIcon(raw)
	if err != nil {
		s.fail(w, http.StatusBadRequest, capitalFirst(err.Error())+".")
		return
	}
	if err := store.AddGardenIcon(store.GardenIcon{ID: id, Name: r.PostFormValue("name"), Width: width, Height: height}, b, sess.identity, s.now()); err != nil {
		s.storeError(w, err)
		return
	}
	s.refreshGardenIcons(w, "/garden-buttons?notice=garden-picture-added")
}

// handleGardenIconRemove: POST /garden-buttons/pictures/remove (privileged, owner). The form: id. A picture a button
// uses is refused.
func (s *Server) handleGardenIconRemove(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.gardenOwner(w, sess) {
		return
	}
	id := r.PostFormValue("id")
	if err := store.RemoveGardenIcon(id, economy.GardenIconKey(id), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	s.refreshGardenIcons(w, "/garden-buttons?notice=garden-picture-removed")
}

// refreshGardenIcons rebuilds the clients' lists after a change and redirects to next; a failed rebuild is shown (the
// change is stored; the page says custom pictures are off and why).
func (s *Server) refreshGardenIcons(w http.ResponseWriter, next string) {
	if err := economy.RefreshGardenIcons(); err != nil {
		s.logger.Printf("cherry: custom Garden pictures off: %v", err)
		s.fail(w, http.StatusServiceUnavailable, "The change is saved, but the pictures cannot reach the players now: "+err.Error()+".")
		return
	}
	redirect(w, http.StatusSeeOther, next)
}

func capitalFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
