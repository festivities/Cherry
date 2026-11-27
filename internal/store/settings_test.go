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
	if err := AddStaffMember("dev-helper", StaffRoleHelper, "", "dev-owner", time.Now(), true); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("AddStaffMember = %v, want ErrNoDatabase", err)
	}
	if err := SetStaffRole("dev-helper", StaffRoleModerator, "dev-owner"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("SetStaffRole = %v, want ErrNoDatabase", err)
	}
}

// TestStaffMembersRoundTrip: members come back in the order added, with their role, note trimmed, time in UTC and actor;
// legacy rows without a role read as moderator; role changes and removals keep order and are audited.
func TestStaffMembersRoundTrip(t *testing.T) {
	docStore(t)
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.FixedZone("CET", 3600))
	if list, err := StaffMembers(); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}
	if err := AddStaffMember("discord:123456789012345678", StaffRoleModerator, "  Ana  ", "dev-owner", now, false); err != nil {
		t.Fatal(err)
	}
	if err := AddStaffMember("discord:223456789012345678", StaffRoleHelper, "", "dev-owner", now, false); err != nil {
		t.Fatal(err)
	}
	if err := AddStaffMember("dev-helper", StaffRoleHelper, "Helper", "dev-owner", now, true); err != nil {
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
	if first.Identity != "discord:123456789012345678" || first.Role != StaffRoleModerator || first.Note != "Ana" || first.By != "dev-owner" || first.Added != "2026-11-17T08:30:00Z" {
		t.Fatalf("first member: %+v", first)
	}
	if list[1].Role != StaffRoleHelper || list[1].Note != "" || list[2].Identity != "dev-helper" || list[2].Role != StaffRoleHelper {
		t.Fatalf("order, role or empty note: %+v", list)
	}
	if role, ok := StaffRoleOf("dev-helper"); !ok || role != StaffRoleHelper {
		t.Fatalf("StaffRoleOf(dev-helper) = %q %t, want helper true", role, ok)
	}
	if !IsStaffMember("dev-helper") || IsStaffMember("dev-nobody") {
		t.Fatal("IsStaffMember does not follow the list")
	}

	// Promote helper to moderator and verify audit + live lookup.
	if err := SetStaffRole("dev-helper", StaffRoleModerator, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if role, ok := StaffRoleOf("dev-helper"); !ok || role != StaffRoleModerator {
		t.Fatalf("after promote StaffRoleOf(dev-helper) = %q %t, want moderator true", role, ok)
	}
	if err := SetStaffRole("dev-helper", "owner", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("invalid role = %v, want ErrBadInput", err)
	}
	if err := SetStaffRole("dev-nobody", StaffRoleHelper, "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("unknown identity role = %v, want ErrBadInput", err)
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

	for _, action := range []string{"staff-add", "staff-role", "staff-remove"} {
		if _, ok := settingAudit(t, action, "staff"); !ok {
			t.Fatalf("no %s audit row", action)
		}
	}
	row, _ := settingAudit(t, "staff-add", "staff")
	if !strings.Contains(row.Detail, `"identity":"dev-helper"`) || !strings.Contains(row.Detail, `"role":"helper"`) || !strings.Contains(row.Detail, `"note":"Helper"`) {
		t.Fatalf("staff-add detail: %s", row.Detail)
	}

	// Legacy JSON row without a role field reads as moderator.
	d := attachedDB()
	if err := d.Update(func(tx *sql.Tx) error {
		return setMetaTx(tx, staffMembersKey, `[{"identity":"discord:323456789012345678","note":"Legacy","added":"2026-11-17T00:00:00Z","by":"dev-owner"}]`)
	}); err != nil {
		t.Fatal(err)
	}
	if role, ok := StaffRoleOf("discord:323456789012345678"); !ok || role != StaffRoleModerator {
		t.Fatalf("legacy member role = %q %t, want moderator true", role, ok)
	}
}

// TestStaffMemberBounds: the identity forms (a dev name only with devNames, at most 24 characters after dev-), the role,
// the note (at most 100 characters), the duplicate, the cap and an unknown removal are all ErrBadInput and change nothing.
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
		if err := AddStaffMember(b.identity, StaffRoleModerator, "", "dev-owner", now, b.devNames); !errors.Is(err, ErrBadInput) {
			t.Errorf("AddStaffMember(%q, devNames=%t) = %v, want ErrBadInput", b.identity, b.devNames, err)
		}
	}
	if err := AddStaffMember("dev-badrole", "admin", "", "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("bad role = %v, want ErrBadInput", err)
	}
	if err := AddStaffMember("dev-"+strings.Repeat("a", 24), StaffRoleModerator, "", "dev-owner", now, true); err != nil {
		t.Fatalf("a 24-character dev name: %v", err)
	}
	if err := AddStaffMember("discord:12345678901234567890", StaffRoleHelper, "", "dev-owner", now, false); err != nil {
		t.Fatalf("a 20-digit id: %v", err)
	}

	if err := AddStaffMember("dev-note", StaffRoleModerator, strings.Repeat("n", 101), "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a 101-character note = %v, want ErrBadInput", err)
	}
	if err := AddStaffMember("dev-note", StaffRoleModerator, strings.Repeat("ö", 100), "dev-owner", now, true); err != nil {
		t.Fatalf("a 100-character note (runes): %v", err)
	}
	if err := AddStaffMember("dev-note", StaffRoleHelper, "", "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a duplicate = %v, want ErrBadInput", err)
	}
	if err := RemoveStaffMember("dev-nobody", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("removing an unknown identity = %v, want ErrBadInput", err)
	}

	list, _ := StaffMembers()
	for len(list) < staffMax {
		if err := AddStaffMember(fmt.Sprintf("dev-m%d", len(list)), StaffRoleHelper, "", "dev-owner", now, true); err != nil {
			t.Fatal(err)
		}
		list, _ = StaffMembers()
	}
	if err := AddStaffMember("dev-over", StaffRoleHelper, "", "dev-owner", now, true); !errors.Is(err, ErrBadInput) {
		t.Fatalf("over staffMax = %v, want ErrBadInput", err)
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
			errs <- AddStaffMember(fmt.Sprintf("dev-c%02d", i), StaffRoleHelper, "", "dev-owner", time.Now(), true)
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

func TestMaintenanceWhitelistWithoutDatabase(t *testing.T) {
	_ = CloseStore()
	if IsMaintenanceWhitelisted("1001") || IsMaintenanceWhitelisted("") || IsMaintenanceWhitelisted("0") {
		t.Fatal("IsMaintenanceWhitelisted without a database returned true")
	}
	if set := MaintenanceWhitelistSet(); len(set) != 0 {
		t.Fatalf("MaintenanceWhitelistSet without a database = %v, want empty", set)
	}
	if _, err := MaintenanceWhitelist(); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("MaintenanceWhitelist = %v, want ErrNoDatabase", err)
	}
	if err := AddMaintenanceWhitelist("1001", "", "dev-owner", time.Now()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("AddMaintenanceWhitelist = %v, want ErrNoDatabase", err)
	}
	if err := RemoveMaintenanceWhitelist("1001", "dev-owner"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("RemoveMaintenanceWhitelist = %v, want ErrNoDatabase", err)
	}
}

func TestMaintenanceWhitelistRoundTrip(t *testing.T) {
	docStore(t)
	now := time.Date(2026, 11, 26, 18, 0, 0, 0, time.FixedZone("CET", 3600))
	if list, err := MaintenanceWhitelist(); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", list, err)
	}
	if set := MaintenanceWhitelistSet(); len(set) != 0 {
		t.Fatalf("empty set: %v", set)
	}
	if IsMaintenanceWhitelisted("") || IsMaintenanceWhitelisted("0") || IsMaintenanceWhitelisted("1001") {
		t.Fatal("unexpected IsMaintenanceWhitelisted true on empty list")
	}

	if err := AddMaintenanceWhitelist("1001", "  Tester one  ", "dev-owner", now); err != nil {
		t.Fatal(err)
	}
	if err := AddMaintenanceWhitelist("1002", "", "dev-owner", now); err != nil {
		t.Fatal(err)
	}
	if err := AddMaintenanceWhitelist("1003", "Tester three", "dev-owner", now); err != nil {
		t.Fatal(err)
	}

	list, err := MaintenanceWhitelist()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("%d entries, want 3", len(list))
	}
	if list[0].Aid != "1001" || list[0].Note != "Tester one" || list[0].By != "dev-owner" || list[0].Added != "2026-11-26T17:00:00Z" {
		t.Fatalf("first entry: %+v", list[0])
	}
	if list[1].Aid != "1002" || list[1].Note != "" || list[2].Aid != "1003" {
		t.Fatalf("order or empty note: %+v", list)
	}

	set := MaintenanceWhitelistSet()
	if len(set) != 3 {
		t.Fatalf("set len = %d, want 3", len(set))
	}
	for _, aid := range []string{"1001", "1002", "1003"} {
		if _, ok := set[aid]; !ok {
			t.Fatalf("set missing %s", aid)
		}
		if !IsMaintenanceWhitelisted(aid) {
			t.Fatalf("IsMaintenanceWhitelisted(%s) = false, want true", aid)
		}
	}
	if IsMaintenanceWhitelisted("") || IsMaintenanceWhitelisted("0") || IsMaintenanceWhitelisted("9999") {
		t.Fatal("IsMaintenanceWhitelisted returned true for non-whitelisted/empty/zero aid")
	}

	if err := RemoveMaintenanceWhitelist("1002", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	list, _ = MaintenanceWhitelist()
	if len(list) != 2 || list[0].Aid != "1001" || list[1].Aid != "1003" {
		t.Fatalf("after removal: %+v", list)
	}
	if IsMaintenanceWhitelisted("1002") {
		t.Fatal("removed aid still reads as whitelisted")
	}
	if err := RemoveMaintenanceWhitelist("1002", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("removing twice = %v, want ErrBadInput", err)
	}

	addRow, ok := settingAudit(t, "maint-whitelist-add", "maintenance")
	if !ok || addRow.Actor != "dev-owner" || !strings.Contains(addRow.Detail, `"aid":"1003"`) || !strings.Contains(addRow.Detail, `"note":"Tester three"`) {
		t.Fatalf("maint-whitelist-add audit row: ok=%t %+v", ok, addRow)
	}
	remRow, ok := settingAudit(t, "maint-whitelist-remove", "maintenance")
	if !ok || remRow.Actor != "dev-owner" || !strings.Contains(remRow.Detail, `"aid":"1002"`) {
		t.Fatalf("maint-whitelist-remove audit row: ok=%t %+v", ok, remRow)
	}
}

func TestMaintenanceWhitelistBounds(t *testing.T) {
	docStore(t)
	now := time.Now()
	for _, bad := range []string{"", "0", "-1", "-42", "abc", "01", "00123", "+10", " 10", "10 "} {
		if err := AddMaintenanceWhitelist(bad, "", "dev-owner", now); !errors.Is(err, ErrBadInput) {
			t.Errorf("AddMaintenanceWhitelist(%q) = %v, want ErrBadInput", bad, err)
		}
	}
	if err := AddMaintenanceWhitelist("500", strings.Repeat("n", 101), "dev-owner", now); !errors.Is(err, ErrBadInput) {
		t.Fatalf("101-rune note = %v, want ErrBadInput", err)
	}
	if err := AddMaintenanceWhitelist("500", strings.Repeat("ö", 100), "dev-owner", now); err != nil {
		t.Fatalf("100-rune note: %v", err)
	}
	if err := AddMaintenanceWhitelist("500", "", "dev-owner", now); !errors.Is(err, ErrBadInput) {
		t.Fatalf("duplicate aid = %v, want ErrBadInput", err)
	}
	if err := RemoveMaintenanceWhitelist("999999", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("removing unknown aid = %v, want ErrBadInput", err)
	}

	list, _ := MaintenanceWhitelist()
	for len(list) < maintWhitelistMax {
		if err := AddMaintenanceWhitelist(fmt.Sprintf("%d", 10000+len(list)), "", "dev-owner", now); err != nil {
			t.Fatal(err)
		}
		list, _ = MaintenanceWhitelist()
	}
	if err := AddMaintenanceWhitelist("99999", "", "dev-owner", now); !errors.Is(err, ErrBadInput) {
		t.Fatalf("over-cap add = %v, want ErrBadInput", err)
	}
	if list, _ := MaintenanceWhitelist(); len(list) != maintWhitelistMax {
		t.Fatalf("%d entries after cap, want %d", len(list), maintWhitelistMax)
	}
}
