package store

import (
	"testing"
	"time"
)

// TestMailSendRowAndAuditAreOneSave: a send's row, its audit row and the mailbox are one save; the send id is kept and found; a
// second row under the same id fails and writes nothing.
func TestMailSendRowAndAuditAreOneSave(t *testing.T) {
	attachTestDB(t)
	acc, id := staffAccount(t, "dev-ms", "1101", "Mailer", 0)
	rec := MailSend{SendID: "send-one-aaaaaaaaaaaa", Created: time.Now().UTC().Format(time.RFC3339), Actor: "discord:111",
		Target: PlayerTarget(id), Recipients: 1, Kind: MailTypeGems, Amount: 5, ExpiresDays: 30, Message: "hi", Reason: "thanks"}

	AccountsMu.Lock()
	acc.Mail = append(acc.Mail, MailRow{Seq: 1, Type: MailTypeGems, Amount: 5, Sender: "s", Expires: time.Now().Add(time.Hour).UnixMilli()})
	err := SaveMailSendLocked(rec, acc)
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := MailSendLocked(rec.SendID)
	if err != nil || !found || got.Reason != "thanks" || got.Recipients != 1 || got.Target != PlayerTarget(id) {
		t.Fatalf("kept send = %+v, found %v, err %v", got, found, err)
	}
	if n := len(auditRowsOf(t, "mail")); n != 1 {
		t.Fatalf("%d mail audit rows, want 1", n)
	}

	AccountsMu.Lock()
	acc.Mail = append(acc.Mail, MailRow{Seq: 2, Type: MailTypeGems, Amount: 5, Sender: "s", Expires: time.Now().Add(time.Hour).UnixMilli()})
	err = SaveMailSendLocked(rec, acc) // the same send id again: the unique key refuses it
	acc.Mail = acc.Mail[:1]
	AccountsMu.Unlock()
	if err == nil {
		t.Fatal("a second send under one id was saved")
	}
	if list, err := ListMailSends(0, 10); err != nil || len(list) != 1 {
		t.Fatalf("sends after the refused one = %d (err %v), want 1", len(list), err)
	}
	if n := len(auditRowsOf(t, "mail")); n != 1 {
		t.Fatalf("a refused send wrote %d mail audit rows in total, want 1", n)
	}
}

// TestListMailSendsNewestFirstAndBounded: the list is newest first, pages by id, and refuses a page outside 1 to 200.
func TestListMailSendsNewestFirstAndBounded(t *testing.T) {
	attachTestDB(t)
	acc, _ := staffAccount(t, "dev-ml", "1102", "Lister", 0)
	for _, sid := range []string{"list-send-aaaaaaaaaaaaa1", "list-send-aaaaaaaaaaaaa2", "list-send-aaaaaaaaaaaaa3"} {
		AccountsMu.Lock()
		err := SaveMailSendLocked(MailSend{SendID: sid, Created: time.Now().UTC().Format(time.RFC3339), Actor: "discord:1",
			Target: AllPlayersTarget, Recipients: 1, Kind: MailTypeGems, Amount: 1, Reason: sid}, acc)
		AccountsMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := ListMailSends(0, 2)
	if err != nil || len(list) != 2 || list[0].SendID != "list-send-aaaaaaaaaaaaa3" {
		t.Fatalf("first page = %+v, err %v", list, err)
	}
	older, err := ListMailSends(list[1].ID, 2)
	if err != nil || len(older) != 1 || older[0].SendID != "list-send-aaaaaaaaaaaaa1" {
		t.Fatalf("older page = %+v, err %v", older, err)
	}
	if _, err := ListMailSends(0, 0); err == nil {
		t.Fatal("a page of 0 rows was accepted")
	}
}
