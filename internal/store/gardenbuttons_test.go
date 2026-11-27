package store

import (
	"errors"
	"testing"
	"time"
)

// TestGardenButtons: add keeps order, refuses a third button, a repeated picture and an empty label; remove by picture;
// each change writes its audit row; without a database the game list is empty.
func TestGardenButtons(t *testing.T) {
	_ = CloseStore()
	if GardenButtons() != nil {
		t.Fatal("GardenButtons without a database is not empty")
	}
	if err := AddGardenButton(GardenButton{Icon: "a", Label: "A", Link: "x"}, "dev-owner", time.Now()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("add without a database = %v", err)
	}
	docStore(t)
	now := time.Date(2026, 11, 28, 3, 0, 0, 0, time.UTC)
	add := func(icon, label string) error {
		return AddGardenButton(GardenButton{Icon: icon, Label: label, Link: "goSomewhere(closet)"}, "dev-owner", now)
	}
	if err := add("72_home_l_my_garden", "  Closet  "); err != nil {
		t.Fatal(err)
	}
	if err := add("72_home_l_my_garden", "Again"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("repeated picture = %v", err)
	}
	if err := add("24_event_icon", " "); !errors.Is(err, ErrBadInput) {
		t.Fatalf("empty label = %v", err)
	}
	if err := add("24_event_icon", "Event"); err != nil {
		t.Fatal(err)
	}
	if err := add("87_home_free_gift", "Third"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("third button = %v", err)
	}
	list := GardenButtons()
	if len(list) != 2 || list[0].Icon != "72_home_l_my_garden" || list[0].Label != "Closet" || list[1].Icon != "24_event_icon" ||
		list[0].Added != "2026-11-28T03:00:00Z" || list[0].By != "dev-owner" {
		t.Fatalf("list = %+v", list)
	}
	if _, ok := settingAudit(t, "garden-button-add", "garden"); !ok {
		t.Fatal("no add audit row")
	}
	if err := RemoveGardenButton("nope", "dev-owner"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("remove unknown = %v", err)
	}
	if err := RemoveGardenButton("72_home_l_my_garden", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if list, err := GardenButtonList(); err != nil || len(list) != 1 || list[0].Icon != "24_event_icon" {
		t.Fatalf("after remove = %+v %v", list, err)
	}
	if _, ok := settingAudit(t, "garden-button-remove", "garden"); !ok {
		t.Fatal("no remove audit row")
	}
}
