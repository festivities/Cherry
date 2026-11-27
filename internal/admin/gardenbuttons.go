package admin

import (
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

const actionWebPage = "web"

type gardenButtonRow struct {
	Icon, Label, Action, Added, By string
}

type gardenActionOption struct {
	Value, Label string
}

type gardenButtonsPage struct {
	Buttons []gardenButtonRow
	Full    bool     // the panel keeps GardenButtonsMax already
	Icons   []string // the pictures to choose from; empty when the animation file cannot be read
	Actions []gardenActionOption
	Max     int
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
		page.Buttons = append(page.Buttons, gardenButtonRow{Icon: b.Icon, Label: b.Label, Action: action, Added: b.Added, By: auth.MaskIdentity(b.By)})
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
	if !economy.IsIconEffectSymbol(icon) {
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
