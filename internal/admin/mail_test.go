package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/economy"
	"cherry/internal/store"
)

// hiddenValue reads the value of the hidden field name from a page (the review page carries the send id that way).
func hiddenValue(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no hidden field %s on the page", name)
	}
	return m[1]
}

// mailboxes returns the mailbox of the account with row id.
func mailboxes(t *testing.T, id int64) []store.MailRow {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.PlayerAccountLocked(id)
	if acc == nil {
		t.Fatalf("no account %d", id)
	}
	return slices.Clone(acc.Mail)
}

// totalMailboxes is the mailboxes a send to every account reaches: every account.
func totalMailboxes(t *testing.T) int {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return len(store.AllAccountsLocked())
}

// sends returns the staff mail sends, newest first.
func sends(t *testing.T) []store.MailSend {
	t.Helper()
	list, err := store.ListMailSends(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// sendForm is the form the review page posts to the send: the send id, the recipients the reviewer saw and the mail.
func sendForm(csrf, sendID, audience string, playerID int64, expect int, kind, amount, item, message, days, reason string) url.Values {
	return url.Values{"csrf": {csrf}, "send": {sendID}, "audience": {audience}, "player_id": {strconv.FormatInt(playerID, 10)},
		"expect": {strconv.Itoa(expect)}, "kind": {kind}, "amount": {amount}, "item": {item}, "message": {message},
		"days": {days}, "reason": {reason}}
}

// reviewForm is the compose form's post to the review.
func reviewForm(csrf, audience, player, kind, amount, item, message, days, reason string) url.Values {
	return url.Values{"csrf": {csrf}, "audience": {audience}, "player": {player}, "kind": {kind}, "amount": {amount},
		"item": {item}, "message": {message}, "days": {days}, "reason": {reason}}
}

// TestMailOneReviewThenSendOnce: the review shows one mailbox and sends nothing. The send puts one row in the mailbox, keeps the
// send and writes its audit row. The same send posted again is answered "already sent" and sends nothing.
func TestMailOneReviewThenSendOnce(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-mail1", "6001", "Mia", 0)

	review := c.post("/mail/review", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "500", "", "Thanks for testing", "30", "event prize"))
	expect(t, review, http.StatusOK)
	checkPage(t, "mail review", review)
	if !strings.Contains(review.body, "<strong>1</strong> mailbox") || !strings.Contains(review.body, "500 Gems") {
		t.Fatalf("the review does not show one mailbox and the attachment: %.300s", review.body)
	}
	if n := len(mailboxes(t, id)); n != 0 {
		t.Fatalf("the review sent %d mails", n)
	}
	sendID := hiddenValue(t, review.body, "send")
	form := sendForm(c.csrf(), sendID, "player", id, 1, "gems", "500", "", "Thanks for testing", "30", "event prize")

	first := c.post("/mail/send", form)
	expect(t, first, http.StatusSeeOther)
	if loc := first.Header.Get("Location"); loc != "/mail/sent?notice=mail-sent" {
		t.Fatalf("send redirects to %q", loc)
	}
	again := c.post("/mail/send", form)
	expect(t, again, http.StatusSeeOther)
	if loc := again.Header.Get("Location"); loc != "/mail/sent?notice=mail-already-sent" {
		t.Fatalf("the repeated send redirects to %q, want already sent", loc)
	}

	rows := mailboxes(t, id)
	if len(rows) != 1 || rows[0].Type != store.MailTypeGems || rows[0].Amount != 500 || rows[0].Message != "Thanks for testing" || rows[0].Sender == "" {
		t.Fatalf("mailbox = %+v, want one 500 Gems row", rows)
	}
	list := sends(t)
	if len(list) != 1 || list[0].Target != store.PlayerTarget(id) || list[0].Recipients != 1 || list[0].Skipped != 0 ||
		list[0].Actor != "dev-owner" || list[0].Reason != "event prize" || list[0].SendID != sendID {
		t.Fatalf("sent mail = %+v, want one row for the player", list)
	}
	if !hasAudit(auditRows(t), "dev-owner", "mail", "event prize") {
		t.Fatal("no mail audit row with the reason")
	}
	page := c.get("/mail/sent")
	expect(t, page, http.StatusOK)
	checkPage(t, "/mail/sent", page)
	for _, want := range []string{fmt.Sprintf("Player #%d", id), "500 Gems", "event prize", "Thanks for testing"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the sent mail list does not show %q", want)
		}
	}
	if p := c.get(playerURL(id)); !strings.Contains(p.body, `href="/mail?player=`) {
		t.Error("the player page does not link to mail")
	}
}

// devConfigWithStaff is the dev-fake staff UI with one more staff member, dev-staff, on the allowlist.
func devConfigWithStaff(t *testing.T) config.Config {
	t.Helper()
	cfg := devConfig()
	path := filepath.Join(t.TempDir(), "staff.txt")
	if err := os.WriteFile(path, []byte("dev-staff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.StaffAllowlistFile = path
	return cfg
}

// TestMailRefusalsSendNothing: a missing or wrong CSRF token, a stale sign-in, and a non-owner's mail to every account are refused
// at the review and at the send; none of them sends.
func TestMailRefusalsSendNothing(t *testing.T) {
	fx := newFixture(t, devConfigWithStaff(t))
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-mail2", "6002", "Refuse", 0)
	before := totalMailboxes(t)
	f := reviewForm("", "player", fmt.Sprintf("#%d", id), "gems", "5", "", "x", "", "reason")

	noCSRF := c.post("/mail/review", f)
	expect(t, noCSRF, http.StatusForbidden)
	wrong := reviewForm("wrong", "player", fmt.Sprintf("#%d", id), "gems", "5", "", "x", "", "reason")
	expect(t, c.post("/mail/review", wrong), http.StatusForbidden)
	noCSRFSend := c.post("/mail/send", sendForm("", "abcdefghijklmnopqrstuvwx", "player", id, 1, "gems", "5", "", "", "", "reason"))
	expect(t, noCSRFSend, http.StatusForbidden)

	// A sign-in older than the fresh window: the send is refused and the sign-in page says so.
	fx.clk.advance(6 * time.Minute)
	stale := c.post("/mail/send", sendForm(c.csrf(), "abcdefghijklmnopqrstuvwx", "player", id, 1, "gems", "5", "", "", "", "reason"))
	expect(t, stale, http.StatusForbidden)
	if !strings.Contains(stale.body, "needs a Discord sign-in") {
		t.Fatal("the stale send does not ask for a fresh sign-in")
	}

	// A signed-in staff member who is not the owner: mail to every account is refused, at review and at send.
	st := fx.client()
	st.signInDev("staff")
	expect(t, st.post("/mail/review", reviewForm(st.csrf(), "all", "", "gems", "5", "", "x", "", "reason")), http.StatusForbidden)
	expect(t, st.post("/mail/send", sendForm(st.csrf(), "abcdefghijklmnopqrstuvwx", "all", 0, before, "gems", "5", "", "", "", "reason")), http.StatusForbidden)

	if n := len(mailboxes(t, id)); n != 0 {
		t.Fatalf("refused mail reached %d mailboxes", n)
	}
	if n := len(sends(t)); n != 0 {
		t.Fatalf("refused mail left %d sends", n)
	}
}

// TestMailAttachmentsAreChecked: the review refuses each bad attachment, amount, expiry, text or player, and nothing is sent.
func TestMailAttachmentsAreChecked(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	id := seedPlayer(t, "dev-mail3", "6003", "Checked", 0)
	seedPlayer(t, "dev-twin1", "6004", "Twin A", 0)
	seedPlayer(t, "dev-twin2", "6005", "Twin B", 0)
	long := strings.Repeat("m", 501)
	cases := []struct {
		name   string
		form   url.Values
		status int
	}{
		{"unknown attachment", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "materials", "5", "", "", "", "r"), http.StatusBadRequest},
		{"zero amount", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "0", "", "", "", "r"), http.StatusBadRequest},
		{"amount not a number", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "ten", "", "", "", "r"), http.StatusBadRequest},
		{"amount above the maximum", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "cash", "999999999999", "", "", "", "r"), http.StatusBadRequest},
		{"item code the catalog does not sell", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "item", "", "CUXX00000", "", "", "r"), http.StatusBadRequest},
		{"expiry above 3650", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "5", "", "", "4000", "r"), http.StatusBadRequest},
		{"expiry not a number", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "5", "", "", "soon", "r"), http.StatusBadRequest},
		{"message of 501 bytes", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "5", "", long, "", "r"), http.StatusBadRequest},
		{"no reason", reviewForm(c.csrf(), "player", fmt.Sprintf("#%d", id), "gems", "5", "", "", "", ""), http.StatusBadRequest},
		{"no such player", reviewForm(c.csrf(), "player", "#999999", "gems", "5", "", "", "", "r"), http.StatusNotFound},
		{"a name that matches nobody", reviewForm(c.csrf(), "player", "nobody-here", "gems", "5", "", "", "", "r"), http.StatusNotFound},
		{"a name that matches two", reviewForm(c.csrf(), "player", "Twin", "gems", "5", "", "", "", "r"), http.StatusBadRequest},
		{"an audience that is neither", reviewForm(c.csrf(), "everyone", "", "gems", "5", "", "", "", "r"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := c.post("/mail/review", tc.form); got.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, got.StatusCode, tc.status)
		}
	}
	if n := len(mailboxes(t, id)); n != 0 {
		t.Fatalf("refused reviews sent %d mails", n)
	}
	if n := len(sends(t)); n != 0 {
		t.Fatalf("refused reviews kept %d sends", n)
	}
}

// TestMailToEveryAccountIsOwnerOnlyAndCounted: the owner's review shows every account; the send refuses a count that changed since
// the review, and the right count sends once: one mailbox row per account, one send row with the count, and the repeat sends nothing.
func TestMailToEveryAccountIsOwnerOnlyAndCounted(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-all1", "6011", "Ann", 0)
	seedPlayer(t, "dev-all2", "6012", "Ben", 0)
	n := totalMailboxes(t)

	review := c.post("/mail/review", reviewForm(c.csrf(), "all", "", "cash", "25", "", "For everyone", "14", "anniversary"))
	expect(t, review, http.StatusOK)
	if !strings.Contains(review.body, fmt.Sprintf("<strong>%d</strong> mailboxes", n)) || !strings.Contains(review.body, "Every account") {
		t.Fatalf("the review does not show %d mailboxes to every account", n)
	}
	sendID := hiddenValue(t, review.body, "send")

	// The count the reviewer saw is not the count now: the send is refused and nothing is sent.
	stale := c.post("/mail/send", sendForm(c.csrf(), sendID, "all", 0, n-1, "cash", "25", "", "For everyone", "14", "anniversary"))
	expect(t, stale, http.StatusBadRequest)
	if !strings.Contains(stale.body, "review it again") {
		t.Fatalf("the count refusal does not say to review again: %.200s", stale.body)
	}
	if got := len(sends(t)); got != 0 {
		t.Fatalf("a refused count kept %d sends", got)
	}

	form := sendForm(c.csrf(), sendID, "all", 0, n, "cash", "25", "", "For everyone", "14", "anniversary")
	expect(t, c.post("/mail/send", form), http.StatusSeeOther)
	expect(t, c.post("/mail/send", form), http.StatusSeeOther) // the double submit

	list := sends(t)
	if len(list) != 1 || list[0].Target != store.AllPlayersTarget || list[0].Recipients != int64(n) || list[0].Skipped != 0 ||
		list[0].Amount != 25 || list[0].Kind != store.MailTypeCash || list[0].ExpiresDays != 14 {
		t.Fatalf("sent mail = %+v, want one all-players send to %d", list, n)
	}
	store.AccountsMu.Lock()
	for _, acc := range store.AllAccountsLocked() {
		if len(acc.Mail) != 1 || acc.Mail[0].Amount != 25 || acc.Mail[0].Type != store.MailTypeCash {
			t.Errorf("account %s has %d mails, want one 25 Cash row", acc.Aid, len(acc.Mail))
		}
	}
	store.AccountsMu.Unlock()
	if !hasAudit(auditRows(t), "dev-owner", "mail", "anniversary") {
		t.Fatal("no audit row for the send to every account")
	}
}

// TestMailToEveryAccountSkipsAFullMailbox: a mailbox that is full gets nothing, and the send counts it as skipped.
func TestMailToEveryAccountSkipsAFullMailbox(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	full := seedPlayer(t, "dev-full1", "6021", "Full", 0)
	seedPlayer(t, "dev-full2", "6022", "Open", 0)
	store.AccountsMu.Lock()
	acc := store.PlayerAccountLocked(full)
	for i := 0; i < economy.MailMaxRows(); i++ {
		acc.Mail = append(acc.Mail, store.MailRow{Seq: int64(i + 1), Type: store.MailTypeGems, Amount: 1, Expires: time.Now().Add(48 * time.Hour).UnixMilli()})
	}
	err := store.SaveLocked(nil, acc)
	store.AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	n := totalMailboxes(t)

	review := c.post("/mail/review", reviewForm(c.csrf(), "all", "", "gems", "3", "", "", "", "full boxes"))
	expect(t, review, http.StatusOK)
	expect(t, c.post("/mail/send", sendForm(c.csrf(), hiddenValue(t, review.body, "send"), "all", 0, n, "gems", "3", "", "", "", "full boxes")), http.StatusSeeOther)

	if got := len(mailboxes(t, full)); got != economy.MailMaxRows() {
		t.Fatalf("the full mailbox has %d rows, want %d", got, economy.MailMaxRows())
	}
	list := sends(t)
	if len(list) != 1 || list[0].Recipients != int64(n-1) || list[0].Skipped != 1 {
		t.Fatalf("sent mail = %+v, want %d recipients and 1 skipped", list, n-1)
	}
}
