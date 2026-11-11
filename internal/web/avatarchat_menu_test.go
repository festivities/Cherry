package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestAvatarChatMenuRowAndBadge: the home menu's last row is the 1:1 chat (label Chat, the messages picture), and CHAT in
// badge/infos is 0 without a session (the count itself is the talk list's, tested in lpn).
func TestAvatarChatMenuRowAndBadge(t *testing.T) {
	if n := len(homeLinks); n != 56 || homeLinks[n-1] != "goSomewhere(avatarChat)" {
		t.Fatalf("home menu has %d rows, last %q; want 56 rows ending with goSomewhere(avatarChat)", n, homeLinks[n-1])
	}
	rows := homeIconRows()
	if row := rows[len(rows)-1]; row.Name != "Chat" || row.Image != "cherry/menu/messages.png" {
		t.Fatalf("chat row = %+v", row)
	}
	if rec := serve(t, http.MethodGet, "/v4/badge/infos/"); !strings.Contains(rec.Body.String(), `"CHAT":0`) {
		t.Fatalf("badge body %s has no CHAT 0", rec.Body.String())
	}
}
