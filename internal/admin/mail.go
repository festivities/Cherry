package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"cherry/internal/auth"
	"cherry/internal/economy"
	"cherry/internal/store"
)

// Staff mail (PLAN Roadmap 8, staff panel phase 5): compose, review, send and the sent mail list. A send is two posts: the
// review (privileged) checks the form and shows the recipients, and the send (privileged) repeats the check and sends once. The
// review's form carries a fresh send id; the send keeps it, so a second post of the same send (a double click, a resend) is
// answered "already sent" and sends nothing. Mail to every account is the owner's only: the review and the send both refuse
// it for anyone else. The sends themselves are economy.SendStaffMail.

const (
	mailListRows   = 50  // sends on a page of the sent mail list
	mailTextCut    = 80  // characters of a send's text in the list
	sendIDMin      = 16  // characters of a send id (the form's value)
	sendIDMax      = 64  // characters of a send id
	mailFormMax    = 200 // characters of a typed player, item or days value
	mailAudPlayer  = "player"
	mailAudAll     = "all"
	mailKindItem   = store.MailTypeItem
	mailNoticeSent = "mail-sent"
)

// mailKind is one attachment choice of the compose form.
type mailKind struct{ Value, Label string }

// mailKinds are the attachments the postbox carries: one of them per mail.
var mailKinds = []mailKind{
	{store.MailTypeGems, "Gems"},
	{store.MailTypeCash, "Cash"},
	{store.MailTypeTickets, "Face Shop tickets"},
	{mailKindItem, "One item (by code)"},
}

// mailForm is the compose form's values, as typed or as the review carried them. PlayerID is the player's row id once the review
// has resolved the typed player.
type mailForm struct {
	Audience, Player, Kind, Amount, Item, Message, Days, Reason string
	PlayerID                                                    int64
}

// mailRow is one send of the sent mail list.
type mailRow struct {
	ID                 int64
	When, By           string
	Target, TargetHref string
	Recipients         int64
	Skipped            int64
	Attachment         string
	Expires            int64
	Text, Reason       string
}

// mailPage is what the mail pages show: the form (and on the review, the recipients and the send id), the owner's flag and the list.
type mailPage struct {
	Form       mailForm
	Owner      bool
	Kinds      []mailKind
	Review     bool
	Recipients int
	Target     string // the review's recipients in words: "Player #12 (name)" or "Every account"
	SendID     string
	Attachment string // the review's attachment in words
	Expires    int    // the review's expiry in days
	Sends      []mailRow
	OlderHref  string
	NewestHref string
}

// mailFormOf reads the form posted by the compose or the review page. Browsers submit a textarea's line breaks as CRLF; the
// message goes to the game with LF, so the player's mail and the 500-byte limit do not carry the browser's CRs.
func mailFormOf(r *http.Request) mailForm {
	message := strings.ReplaceAll(r.PostFormValue("message"), "\r\n", "\n")
	return mailForm{
		Audience: r.PostFormValue("audience"), Player: strings.TrimSpace(r.PostFormValue("player")),
		Kind: r.PostFormValue("kind"), Amount: r.PostFormValue("amount"), Item: strings.TrimSpace(r.PostFormValue("item")),
		Message: strings.TrimSpace(message), Days: r.PostFormValue("days"), Reason: r.PostFormValue("reason"),
	}
}

// mailOrderOf turns the form into the order the economy checks: the amount for a currency (a whole number), the item code for an
// item, and the expiry (empty or 0 = the rewards default). Refusals are store.ErrBadInput.
func mailOrderOf(f mailForm) (economy.MailOrder, error) {
	o := economy.MailOrder{Type: f.Kind, Item: f.Item, Message: f.Message}
	if f.Kind != mailKindItem {
		n, err := strconv.ParseInt(strings.TrimSpace(f.Amount), 10, 64)
		if err != nil {
			return o, fmt.Errorf("%w: the amount is a whole number", store.ErrBadInput)
		}
		o.Amount = n
	}
	if d := strings.TrimSpace(f.Days); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil {
			return o, fmt.Errorf("%w: the expiry is a whole number of days", store.ErrBadInput)
		}
		o.Days = n
	}
	return o, nil
}

// resolvePlayer finds the one player a typed value names: "#<row id>", or an avatar id, a Discord identity or a part of the name
// that matches exactly one account. More than one match asks for the row id; none is ErrNoPlayer.
func resolvePlayer(q string) (int64, error) {
	q = strings.TrimSpace(q)
	if utf8.RuneCountInString(q) > mailFormMax {
		return 0, fmt.Errorf("%w: the player is at most %d characters", store.ErrBadInput, mailFormMax)
	}
	if rest, ok := strings.CutPrefix(q, "#"); ok {
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || id < 1 {
			return 0, fmt.Errorf("%w: the player number is #<row id>", store.ErrBadInput)
		}
		if _, found := store.PlayerByID(id); !found {
			return 0, store.ErrNoPlayer
		}
		return id, nil
	}
	rows, _ := store.FindPlayers(q)
	switch {
	case len(rows) == 0:
		return 0, store.ErrNoPlayer
	case len(rows) > 1:
		return 0, fmt.Errorf("%w: %d players match %q: use #<row id> from the player list", store.ErrBadInput, len(rows), q)
	}
	return rows[0].ID, nil
}

// attachmentText is the attachment in words: "500 Gems", "3 Face Shop tickets" or "Item CUAE00B9R (its name)".
func attachmentText(kind string, amount int64, item string) string {
	switch kind {
	case store.MailTypeGems:
		return fmt.Sprintf("%d Gems", amount)
	case store.MailTypeCash:
		return fmt.Sprintf("%d Cash", amount)
	case store.MailTypeTickets:
		return fmt.Sprintf("%d Face Shop tickets", amount)
	case store.MailTypeItem:
		return fmt.Sprintf("Item %s (%s)", item, economy.ItemName(item))
	}
	return kind
}

// ownerOnly answers the refusal for mail to every account from anyone but the owner, and reports whether the request may go on.
func (s *Server) ownerOnly(w http.ResponseWriter, sess session, audience string) bool {
	if audience == mailAudAll && !s.isOwner(sess) {
		s.logger.Printf("cherry: staff mail to every account refused for a non-owner")
		s.fail(w, http.StatusForbidden, "Only the owner can send mail to every account.")
		return false
	}
	return true
}

// handleMail: GET /mail, the compose form. ?player=#<row id> starts it on that player.
func (s *Server) handleMail(w http.ResponseWriter, r *http.Request, sess session) {
	form := mailForm{Audience: mailAudPlayer, Kind: store.MailTypeGems}
	if p := strings.TrimSpace(r.URL.Query().Get("player")); p != "" {
		if id, err := strconv.ParseInt(p, 10, 64); err == nil && id > 0 {
			form.Player = "#" + p
		} else {
			s.fail(w, http.StatusBadRequest, "The player number is a row id.")
			return
		}
	}
	v := s.base(r, sess, "Mail")
	v.Mail = mailPage{Form: form, Owner: s.isOwner(sess), Kinds: mailKinds}
	s.render(w, http.StatusOK, "mail", v)
}

// handleMailReview: POST /mail/review (privileged). It checks the form the way the send will and shows the recipients and a
// fresh send id on the confirm page. Nothing is sent.
func (s *Server) handleMailReview(w http.ResponseWriter, r *http.Request, sess session) {
	f := mailFormOf(r)
	if !s.ownerOnly(w, sess, f.Audience) {
		return
	}
	order, err := mailOrderOf(f)
	if err == nil {
		err = order.Check()
	}
	if err == nil {
		_, err = store.CleanStaffReason(f.Reason)
	}
	if err != nil {
		s.storeError(w, err)
		return
	}
	var player int64
	switch f.Audience {
	case mailAudPlayer:
		if player, err = resolvePlayer(f.Player); err != nil {
			s.storeError(w, err)
			return
		}
	case mailAudAll:
	default:
		s.fail(w, http.StatusBadRequest, "The audience is one player or every account.")
		return
	}
	n, ok := economy.MailRecipients(player)
	if !ok {
		s.storeError(w, store.ErrNoPlayer)
		return
	}
	f.PlayerID = player
	v := s.base(r, sess, "Review mail")
	v.Crumbs = []link{{Href: "/mail", Label: "Mail"}, {Label: "Review"}}
	v.Mail = mailPage{Form: f, Owner: s.isOwner(sess), Review: true, Recipients: n,
		SendID: randomToken(16), Attachment: attachmentText(f.Kind, order.Amount, order.Item), Expires: economy.MailExpiry(order.Days)}
	if player > 0 {
		v.Mail.Target = fmt.Sprintf("Player #%d %s", player, playerName(player))
	} else {
		v.Mail.Target = "Every account"
	}
	s.render(w, http.StatusOK, "mailreview", v)
}

// playerName is the nickname of the player with row id, for the review's text ("" when it has none).
func playerName(id int64) string {
	p, ok := store.PlayerByID(id)
	if !ok || p.Name == "" {
		return ""
	}
	return "(" + p.Name + ")"
}

// validSendID: the form's send id, a random token of sendIDMin to sendIDMax URL-safe characters.
func validSendID(v string) bool {
	return len(v) >= sendIDMin && len(v) <= sendIDMax && onlyChars(v, "-_")
}

// handleMailSend: POST /mail/send (privileged). The form is the review's, with the send id and the recipients the reviewer saw.
func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request, sess session) {
	f := mailFormOf(r)
	if !s.ownerOnly(w, sess, f.Audience) {
		return
	}
	sendID := r.PostFormValue("send")
	if !validSendID(sendID) {
		s.fail(w, http.StatusBadRequest, "The send id is not valid. Review the mail again.")
		return
	}
	order, err := mailOrderOf(f)
	if err != nil {
		s.storeError(w, err)
		return
	}
	var player int64
	switch f.Audience {
	case mailAudPlayer:
		id, err := strconv.ParseInt(r.PostFormValue("player_id"), 10, 64)
		if err != nil || id < 1 {
			s.fail(w, http.StatusBadRequest, "The player is not valid. Review the mail again.")
			return
		}
		player = id
	case mailAudAll:
	default:
		s.fail(w, http.StatusBadRequest, "The audience is one player or every account.")
		return
	}
	expect, err := strconv.Atoi(r.PostFormValue("expect"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "The recipient count is not valid. Review the mail again.")
		return
	}
	_, sent, err := economy.SendStaffMail(economy.StaffMail{SendID: sendID, Player: player, Order: order, Expect: expect,
		Actor: sess.identity, Reason: f.Reason}, economy.NowFunc())
	if err != nil {
		s.storeError(w, err)
		return
	}
	notice := mailNoticeSent
	if !sent {
		notice = "mail-already-sent"
	}
	redirect(w, http.StatusSeeOther, "/mail/sent?notice="+notice)
}

// handleMailSent: GET /mail/sent, the sends newest first; ?before=<id> reads the page that follows.
func (s *Server) handleMailSent(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	var before int64
	if raw := q["before"]; len(raw) > 0 {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	sends, err := store.ListMailSends(before, mailListRows)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Sent mail")
	v.Crumbs = []link{{Href: "/mail", Label: "Mail"}, {Label: "Sent"}}
	v.Mail = mailPage{Owner: s.isOwner(sess), Sends: sentRows(sends), NewestHref: "/mail/sent"}
	if len(sends) == mailListRows {
		v.Mail.OlderHref = fmt.Sprintf("/mail/sent?before=%d", sends[len(sends)-1].ID)
	}
	s.render(w, http.StatusOK, "mailsent", v)
}

// sentRows turns the sends into the list's rows: the target with its player link, the attachment, the counts and the text cut.
func sentRows(sends []store.MailSend) []mailRow {
	rows := []mailRow{}
	for _, m := range sends {
		row := mailRow{ID: m.ID, When: rfcDate(m.Created), By: auth.MaskIdentity(m.Actor), Target: "Every account",
			Recipients: m.Recipients, Skipped: m.Skipped, Attachment: attachmentText(m.Kind, m.Amount, m.Item), Expires: m.ExpiresDays,
			Text: cutText(m.Message, mailTextCut), Reason: m.Reason}
		if id, ok := strings.CutPrefix(m.Target, "player:"); ok {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil {
				row.Target, row.TargetHref = "Player #"+id, playerPath(n)
			}
		}
		rows = append(rows, row)
	}
	return rows
}
