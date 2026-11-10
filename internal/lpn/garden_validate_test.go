package lpn

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// validateRoundTrip sends one idx42 request for owner through gardenValidateHandle and returns the reply's msgid and
// body, read the way the client reads a GARDEN frame: a 4-byte length, then 'G', subtype, tunnel, msgid, body.
func validateRoundTrip(t *testing.T, owner uint64, logger *log.Logger) (uint16, []byte) {
	t.Helper()
	a, ap := net.Pipe()
	defer a.Close()
	defer ap.Close()
	type reply struct {
		msgid uint16
		body  []byte
	}
	got := make(chan reply, 1)
	go func() {
		var r reply
		defer func() { got <- r }()
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(ap, hdr); err != nil {
			t.Error(err)
			return
		}
		frame := make([]byte, binary.BigEndian.Uint32(hdr))
		if _, err := io.ReadFull(ap, frame); err != nil {
			t.Error(err)
			return
		}
		r = reply{binary.BigEndian.Uint16(frame[10:12]), frame[12:]}
	}()
	req := append([]byte{'G', 0x48, 1, 2, 3, 4, 5, 6, 7, 8, 0, gardenEnterValidateReq}, pbVar(nil, 1, owner)...)
	if err := gardenValidateHandle(&lockedConn{Conn: a}, req, logger); err != nil {
		t.Fatal(err)
	}
	r := <-got
	return r.msgid, r.body
}

// TestGardenEnterValidate: an existing avatar owner is valid with the normal result, an unknown aid is not, and
// both replies are idx49 with fields 1-4 as varints (is_brand_avatar always 0).
func TestGardenEnterValidate(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"gvalid": {AccessToken: "gvalid", SessionKey: "sk", Aid: "9721", Name: "Validate"}})
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)

	msgid, body := validateRoundTrip(t, 9721, logger)
	if msgid != gardenEnterValidateRes {
		t.Fatalf("reply msgid = %d, want %d", msgid, gardenEnterValidateRes)
	}
	v := pbFieldsOf(t, body)
	if v.vars[1] != 9721 || v.vars[2] != 0 || v.vars[3] != 1 || v.vars[4] != gardenValidResult {
		t.Fatalf("valid reply owner/brand/valid/result = %v", v.vars)
	}
	if len(v.msgs) != 0 {
		t.Fatalf("unexpected length-delimited fields %v", v.msgs)
	}

	msgid, body = validateRoundTrip(t, 9799, logger)
	if msgid != gardenEnterValidateRes {
		t.Fatalf("reply msgid = %d, want %d", msgid, gardenEnterValidateRes)
	}
	v = pbFieldsOf(t, body)
	if v.vars[1] != 9799 || v.vars[2] != 0 || v.vars[3] != 0 || v.vars[4] == gardenValidResult {
		t.Fatalf("invalid reply owner/brand/valid/result = %v", v.vars)
	}

	// Aid 0 is an account without an avatar, so it is never a valid owner.
	if _, body = validateRoundTrip(t, 0, logger); pbFieldsOf(t, body).vars[3] != 0 {
		t.Fatal("aid 0 validated")
	}
	if !strings.Contains(logs.String(), "GARDEN idx42 validate owner=9721 valid=true") ||
		!strings.Contains(logs.String(), "GARDEN idx42 validate owner=9799 valid=false") {
		t.Fatalf("log = %q", logs.String())
	}
}
