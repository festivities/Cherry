package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// settingAudit returns the newest audit row with the action and target, or false.
func settingAudit(t *testing.T, action, target string) (AuditRow, bool) {
	t.Helper()
	rows, err := AuditRows(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Action == action && r.Target == target {
			return r, true
		}
	}
	return AuditRow{}, false
}

// TestChatRetentionOverride: unset, the configured value applies; a stored 0..3650 replaces it (0 keeps every message); a
// bad value is ErrBadInput and changes nothing; clearing brings the configured value back. Each change is audited.
func TestChatRetentionOverride(t *testing.T) {
	docStore(t)
	if days, set, err := ChatRetentionSetting(); err != nil || set || days != 0 {
		t.Fatalf("unset: %d %t %v", days, set, err)
	}
	if got := ChatRetention(30); got != 30 {
		t.Fatalf("unset ChatRetention = %d, want the fallback 30", got)
	}

	if err := SetChatRetention(7, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := ChatRetention(30); got != 7 {
		t.Fatalf("set ChatRetention = %d, want 7", got)
	}
	if days, set, err := ChatRetentionSetting(); err != nil || !set || days != 7 {
		t.Fatalf("set: %d %t %v", days, set, err)
	}
	if err := SetChatRetention(0, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := ChatRetention(30); got != 0 {
		t.Fatalf("zero ChatRetention = %d, want 0 (keep every message)", got)
	}
	for _, bad := range []int{-1, ChatDaysMax + 1} {
		if err := SetChatRetention(bad, "dev-owner"); !errors.Is(err, ErrBadInput) {
			t.Fatalf("SetChatRetention(%d) = %v, want ErrBadInput", bad, err)
		}
	}
	if got := ChatRetention(30); got != 0 {
		t.Fatalf("a refused value changed the retention to %d", got)
	}

	if err := ClearChatRetention("dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := ChatRetention(30); got != 30 {
		t.Fatalf("cleared ChatRetention = %d, want the fallback 30", got)
	}
	if _, set, _ := ChatRetentionSetting(); set {
		t.Fatal("cleared retention still reads as set")
	}

	rows, err := AuditRows(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Action == "setting" {
			got = append(got, rows[i].Detail)
		}
	}
	want := []string{
		`{"new":"7","old":"default"}`,
		`{"new":"0","old":"7"}`,
		`{"new":"default","old":"0"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("setting audit details:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if row, ok := settingAudit(t, "setting", chatDaysKey); !ok || row.Actor != "dev-owner" {
		t.Fatalf("no setting row by the owner: %+v", row)
	}
}

// TestChatRetentionIgnoresAStoredNonNumber: a stored value that is not a whole number from 0 to 3650 reads as no value, so the
// configured value applies, and ChatRetentionSetting reports it as not set.
func TestChatRetentionIgnoresAStoredNonNumber(t *testing.T) {
	docStore(t)
	d := attachedDB()
	for _, v := range []string{"abc", "-1", "3651", ""} {
		if err := d.Update(func(tx *sql.Tx) error { return setMetaTx(tx, chatDaysKey, v) }); err != nil {
			t.Fatal(err)
		}
		if got := ChatRetention(30); got != 30 {
			t.Fatalf("stored %q: ChatRetention = %d, want 30", v, got)
		}
		if _, set, err := ChatRetentionSetting(); err != nil || set {
			t.Fatalf("stored %q: set=%t err=%v", v, set, err)
		}
	}
}

// TestChatRetentionWithoutDatabase: with no database the configured value applies and the setters say so.
func TestChatRetentionWithoutDatabase(t *testing.T) {
	_ = CloseStore()
	if got := ChatRetention(30); got != 30 {
		t.Fatalf("ChatRetention without a database = %d, want 30", got)
	}
	if _, _, err := ChatRetentionSetting(); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("ChatRetentionSetting = %v, want ErrNoDatabase", err)
	}
	if err := SetChatRetention(7, "dev-owner"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("SetChatRetention = %v, want ErrNoDatabase", err)
	}
	if IsStaffMember("dev-helper") {
		t.Fatal("IsStaffMember without a database is true")
	}
	if _, err := StaffMembers(); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("StaffMembers = %v, want ErrNoDatabase", err)
	}
	if err := AddStaffMember("dev-helper", "", "dev-owner", time.Now(), true); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("AddStaffMember = %v, want ErrNoDatabase", err)
	}
}

// TestStaffMembersRoundTrip: members come back in the order added, with the note trimmed, the time in UTC and the actor; a
// removal keeps the others' order; IsStaffMember follows the list; each change is audited.
func TestStaffMembersRoundTrip(t *testing.T) {
	docStore(t)
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.FixedZone("CET", 3600))
	if list, err := StaffMembers(); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}
	if err := AddStaffMember("discord:123456789012345678", "  Ana  ", "dev-owner", now, false); err != nil {
		t.Fatal(err)
	}
	if err := AddStaffMember("discord:223456789012345678", "", "dev-owner", now, false); err != nil {
		t.Fatal(err)
	}
	if err := AddStaffMember("dev-helper", "Helper", "dev-owner", now, true); err != nil {
		t.Fatal(err)
	}
	list, err := StaffMembers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("%d members, want 3", len(list))
	}
	first := list[0]
	if first.Identity != "discord:123456789012345678" || first.Note != "Ana" || first.By != "dev-owner" || first.Added != "2026-11-17T08:30:00Z" {
		t.Fatalf("first member: %+v", first)
	}
	if list[1].Note != "" || list[2].Identity != "dev-helper" {
		t.Fatalf("order or empty note: %+v", list)
	}
	if !IsStaffMember("dev-helper") || IsStaffMember("dev-nobody") {
		t.Fatal("IsStaffMember does not follow the list")
	}

	if err := RemoveStaffMember("discord:123456789012345678", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	list, _ = StaffMembers()
	if len(list) != 2 || list[0].Identity != "discord:223456789012345678" || list[1].Identity != "dev-helper" {
		t.Fatalf("after the removal: %+v", list)
	}
	if IsStaffMember("discord:123456789012345678") {
		t.Fatal("a removed member still reads as staff")
	}
	if err := RemoveStaffMember("dev-helper", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStaffMember("dev-helper", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("removing twice = %v, want ErrBadInput", err)
	}
	if list, err := StaffMembers(); err != nil || len(list) != 1 {
		t.Fatalf("after emptying one: %v %v", list, err)
	}

	for _, action := range []string{"staff-add", "staff-remove"} {
		if _, ok := settingAudit(t, action, "staff"); !ok {
			t.Fatalf("no %s audit row", action)
		}
	}
	row, _ := settingAudit(t, "staff-add", "staff")
	if !strings.Contains(row.Detail, `"identity":"dev-helper"`) || !strings.Contains(row.Detail, `"note":"Helper"`) {
		t.Fatalf("staff-add detail: %s", row.Detail)
	}
}

// TestStaffMemberBounds: the identity forms (a dev name only with devNames, at most 24 characters after dev-), the note
// (at most 100 characters), the duplicate, the cap of 50 and an unknown removal are all ErrBadInput and change nothing.
func TestStaffMemberBounds(t *testing.T) {
	docStore(t)
	now := time.Now()
	bad := []struct {
		identity string
		devNames bool
	}{
		{"discord:1234567890123456", false},      // 16 digits
		{"discord:123456789012345678901", false}, // 21 digits
		{"discord:12345678901234567a", false},
		{"123456789012345678", false},            // a bare id: the panel adds the prefix, the store does not
		{"dev-helper", false},                    // dev names only with devNames
		{"dev-", true},                           // no name
		{"dev-" + strings.Repeat("a", 25), true}, // more than the sign-in takes
		{"dev-with space", true},
		{"dev-helper\n", true},
		{"owner", true},
	}
	for _, b := range bad {
		if err := AddStaffMember(b.identity, "", "dev-owner", now, b.devNames); !errors.Is(err, ErrBadInput) {
			t.Errorf("AddStaffMember(%q, devNames=%t) = %v, want ErrBadInput", b.identity, b.devNames, err)
		}
	}
	if err := AddStaffMember("dev-"+strings.Repeat("a", 24), "", "dev-owner", now, true); err != nil {
		t.Fatalf("a 24-character dev name: %v", err)
	}
	if err := AddStaffMember("discord:12345678901234567890", "", "dev-owner", now, false); err != nil {
		t.Fatalf("a 20-digit id: %v", err)
	}

	if err := AddStaffMember("dev-note", strings.Repeat("n", 101), "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a 101-character note = %v, want ErrBadInput", err)
	}
	if err := AddStaffMember("dev-note", strings.Repeat("ö", 100), "dev-owner", now, true); err != nil {
		t.Fatalf("a 100-character note (runes): %v", err)
	}
	if err := AddStaffMember("dev-note", "", "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a duplicate = %v, want ErrBadInput", err)
	}
	if err := RemoveStaffMember("dev-nobody", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("removing an unknown identity = %v, want ErrBadInput", err)
	}

	list, _ := StaffMembers()
	for len(list) < staffMax {
		if err := AddStaffMember(fmt.Sprintf("dev-m%d", len(list)), "", "dev-owner", now, true); err != nil {
			t.Fatal(err)
		}
		list, _ = StaffMembers()
	}
	if err := AddStaffMember("dev-over", "", "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("the 51st member = %v, want ErrBadInput", err)
	}
	if list, _ := StaffMembers(); len(list) != staffMax {
		t.Fatalf("%d members after the cap, want %d", len(list), staffMax)
	}
}

// TestStaffMembersConcurrentAdds: adds that run at once all land: each change reads and writes the list in its own
// transaction, so none overwrites another.
func TestStaffMembersConcurrentAdds(t *testing.T) {
	docStore(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AddStaffMember(fmt.Sprintf("dev-c%02d", i), "", "dev-owner", time.Now(), true)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if list, err := StaffMembers(); err != nil || len(list) != n {
		t.Fatalf("%d members after %d concurrent adds (%v)", len(list), n, err)
	}
}
