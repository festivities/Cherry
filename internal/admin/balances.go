package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Every player's balances (owner only): one change sets or adds to chosen currencies on every account. The form is a review and a
// send, as mail is: the review (privileged) checks the form, shows the mode, each balance and the accounts that would change, and
// carries a fresh send id; the apply (privileged) repeats the checks and makes the change once (economy.StaffBalancesAll). Both
// refuse anyone but the owner. The change itself, its ledger lines and its audit row are in economy/staffbalances.go.

const (
	balancesModeSet    = "set"
	balancesModeAdd    = "add"
	balancesModeTake   = "take"
	balancesNoticeDone = "balances-changed" // ?notice= code: the count is in ?n=, and the flash names it
)

// balanceField is one currency of the form: the name the form, the change and the audit row use, and its label.
type balanceField struct{ Name, Label string }

// balanceFields are the currencies in the order of the form.
var balanceFields = []balanceField{
	{"gems", "Gems"},
	{"cash", "Cash"},
	{"faceTickets", "Face Shop tickets"},
	{"smiles", "Smiles"},
	{"bossCoins", "Boss Coins"},
	{"ridePoints", "Ride points"},
	{"extraHearts", "Extra hearts (to send)"},
}

// balancesForm is the form's values, as typed or as the review carried them: the mode, each balance's text (a blank one is
// untouched) and the reason.
type balancesForm struct {
	Mode   string
	Values map[string]string
	Reason string
}

// balancesPage is what the balances pages show: the form, and on the review the count of accounts and the send id.
type balancesPage struct {
	Form     balancesForm
	Fields   []balanceField
	Review   bool
	Accounts int      // every account the change reaches
	Changing int      // the accounts whose balance would change
	SendID   string   // the review's send id
	Lines    []string // the review's change to each chosen balance, in words
}

// balancesOwner answers the refusal for anyone but the owner and reports whether the request may go on.
func (s *Server) balancesOwner(w http.ResponseWriter, sess session) bool {
	if !s.isOwner(sess) {
		s.logger.Printf("cherry: balances change refused for a non-owner")
		s.fail(w, http.StatusForbidden, "Only the owner can change every player's balances.")
		return false
	}
	return true
}

// balancesFormOf reads the form posted by the page or the review. A blank balance is left out of the map.
func balancesFormOf(r *http.Request) balancesForm {
	f := balancesForm{Mode: r.PostFormValue("mode"), Values: map[string]string{}, Reason: r.PostFormValue("reason")}
	for _, fd := range balanceFields {
		if v := strings.TrimSpace(r.PostFormValue(fd.Name)); v != "" {
			f.Values[fd.Name] = v
		}
	}
	return f
}

// balancesOf turns the form into the change the economy checks: set, take or add, and each filled balance a whole number. A refusal
// is a store.ErrBadInput.
func balancesOf(f balancesForm, actor string) (economy.StaffBalances, error) {
	b := economy.StaffBalances{Actor: actor, Reason: f.Reason, Values: map[string]int64{}}
	switch f.Mode {
	case balancesModeSet:
		b.Set = true
	case balancesModeTake:
		b.Take = true
	case balancesModeAdd:
	default:
		return b, fmt.Errorf("%w: the change is set, take or add", store.ErrBadInput)
	}
	for _, fd := range balanceFields {
		text, ok := f.Values[fd.Name]
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return b, fmt.Errorf("%w: the %s amount is a whole number", store.ErrBadInput, fd.Label)
		}
		b.Values[fd.Name] = n
	}
	return b, nil
}

// balanceLines says what the change does to each chosen balance, in words, for the review.
func balanceLines(b economy.StaffBalances) []string {
	var out []string
	for _, fd := range balanceFields {
		v, ok := b.Values[fd.Name]
		if !ok {
			continue
		}
		switch {
		case b.Set:
			out = append(out, fmt.Sprintf("Every player's %s becomes %d.", fd.Label, v))
		case b.Take:
			out = append(out, fmt.Sprintf("Every player loses %d %s (a balance stops at 0).", v, fd.Label))
		default:
			out = append(out, fmt.Sprintf("Every player gets %d %s (a balance stops at %d).", v, fd.Label, store.MaxBalance))
		}
	}
	return out
}

// handleBalances: GET /balances, the form. The owner's only.
func (s *Server) handleBalances(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.balancesOwner(w, sess) {
		return
	}
	v := s.base(r, sess, "Every player's balances")
	if q := r.URL.Query(); q.Get("notice") == balancesNoticeDone {
		if n, err := strconv.Atoi(q.Get("n")); err == nil && n >= 0 {
			v.Notice = fmt.Sprintf("Balances changed for %d players.", n)
		}
	}
	v.Balances = balancesPage{Form: balancesForm{Mode: balancesModeAdd, Values: map[string]string{}}, Fields: balanceFields}
	s.render(w, http.StatusOK, "balances", v)
}

// handleBalancesReview: POST /balances/review (privileged). It checks the form the way the change will and shows the change, the
// accounts it reaches and a fresh send id. Nothing is changed.
func (s *Server) handleBalancesReview(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.balancesOwner(w, sess) {
		return
	}
	f := balancesFormOf(r)
	b, err := balancesOf(f, sess.identity)
	if err != nil {
		s.storeError(w, err)
		return
	}
	accounts, changing, err := economy.PreviewStaffBalances(b)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Review balances")
	v.Crumbs = []link{{Href: "/balances", Label: "Balances"}, {Label: "Review"}}
	v.Balances = balancesPage{Form: f, Fields: balanceFields, Review: true, Accounts: accounts, Changing: changing,
		SendID: randomToken(16), Lines: balanceLines(b)}
	s.render(w, http.StatusOK, "balancesreview", v)
}

// handleBalancesApply: POST /balances/apply (privileged). The form is the review's, with the send id and the count the reviewer saw.
// A send id already kept changes nothing and says so.
func (s *Server) handleBalancesApply(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.balancesOwner(w, sess) {
		return
	}
	sendID := r.PostFormValue("send")
	if !validSendID(sendID) {
		s.fail(w, http.StatusBadRequest, "The send id is not valid. Review the change again.")
		return
	}
	expect, err := strconv.Atoi(r.PostFormValue("expect"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "The account count is not valid. Review the change again.")
		return
	}
	f := balancesFormOf(r)
	b, err := balancesOf(f, sess.identity)
	if err != nil {
		s.storeError(w, err)
		return
	}
	b.SendID, b.Expect = sendID, expect
	changed, applied, err := economy.StaffBalancesAll(b, economy.NowFunc())
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !applied {
		redirect(w, http.StatusSeeOther, "/balances?notice=balances-already-applied")
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/balances?notice=%s&n=%d", balancesNoticeDone, changed))
}
