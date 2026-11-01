package economy

import (
	"encoding/json"
	"strings"
	"testing"
)

// The client takes a row's aid from the last aid key present, so an empty key would drop the row (manage-applications.md).
func TestCircleAvatarLeavesEmptyAidKeysOut(t *testing.T) {
	av := circleAvatarView("1006", roleMember, 1)
	av.ApplyAid = "1006"
	body, err := json.Marshal(av)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"senderAid", "receiverAid"} {
		if strings.Contains(string(body), key) {
			t.Fatalf("%s present: %s", key, body)
		}
	}
	if !strings.Contains(string(body), `"applyAid":"1006"`) {
		t.Fatalf("applyAid missing: %s", body)
	}
}
