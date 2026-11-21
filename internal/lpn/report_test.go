package lpn

import (
	"strconv"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// gatewayReportBody is a report as the client sends it: 1 target aid, 5 the typed remark, 6 chat serial, 7 the reason code.
func gatewayReportBody(target uint64, remark string, serial uint64, code string) []byte {
	b := pbVar(nil, 1, target)
	b = pbLen(b, 2, []byte("target nickname"))
	b = pbLen(b, 4, []byte("policy"))
	b = pbLen(b, 5, []byte(remark))
	b = pbVar(b, 6, serial)
	return pbLen(b, 7, []byte(code))
}

// reportRows returns every stored report, newest first.
func reportRows(t *testing.T) []store.ReportRow {
	t.Helper()
	rows, err := store.ListReports("", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestCircleReportIsStoredWithoutAnAnswer: cs_report_chat (14) from a logged-in member stores the report with the verified aid
// as reporter and the circle the session entered as channel; the client gets no frame back.
func TestCircleReportIsStoredWithoutAnAnswer(t *testing.T) {
	testutil.UseDatabase(t)
	testutil.InstallAccounts(t, withLoginAccounts(nil, 7, 9))
	c := newCirClient(t, &circleRooms{}, &circleChats{}, 7, 41)
	c.login()
	c.sess.circleID = 1940000001
	c.send(circleCsReportChat, gatewayReportBody(9, "circle remark", 42, "R3"))
	c.none()

	rows := reportRows(t)
	if len(rows) != 1 {
		t.Fatalf("stored %d reports, want 1", len(rows))
	}
	r := rows[0]
	if r.Source != store.ReportCircle || r.ReporterAid != "7" || r.TargetAid != "9" || r.Reason != "R3" ||
		r.Message != "circle remark" || r.RefDetail != "42" || r.Snapshot().Channel != "1940000001" {
		t.Fatalf("circle row = %+v (snapshot %+v)", r, r.Snapshot())
	}
}

// TestCircleReportNeedsTheLoginAndAValidReason: before a verified login nothing is read; a reason outside R<n> is dropped.
func TestCircleReportNeedsTheLoginAndAValidReason(t *testing.T) {
	testutil.UseDatabase(t)
	testutil.InstallAccounts(t, withLoginAccounts(nil, 7, 9))
	c := newCirClient(t, &circleRooms{}, &circleChats{}, 7, 41)
	c.send(circleCsReportChat, gatewayReportBody(9, "early", 1, "R3")) // no login yet: ignored
	c.none()
	c.login()
	c.send(circleCsReportChat, gatewayReportBody(9, "bad code", 1, "Q9"))
	c.none()
	if rows := reportRows(t); len(rows) != 0 {
		t.Fatalf("stored %d reports, want none", len(rows))
	}
}

// TestSquareReportIsStored: cr_report_chat (12) stores the report as the verified reporter, with the Square's theme as the
// channel once the session has entered a Square, and sends nothing back.
func TestSquareReportIsStored(t *testing.T) {
	testutil.UseDatabase(t)
	testutil.InstallAccounts(t, withLoginAccounts(nil, 7, 9))
	c := newSqClient(t, &squareRoom{}, 1)
	c.send(0, loginBody(7))
	c.read()
	c.send(12, gatewayReportBody(9, "square remark", 5, "R6"))
	c.none()
	c.sess.entered = true
	c.sess.square = squaresFile.Get().entry(squareThemeID)
	c.send(12, gatewayReportBody(9, "in the square", 6, "R6"))
	c.none()

	rows := reportRows(t)
	if len(rows) != 2 {
		t.Fatalf("stored %d reports, want 2", len(rows))
	}
	inSquare, outside := rows[0], rows[1]
	if outside.Source != store.ReportSquare || outside.ReporterAid != "7" || outside.TargetAid != "9" || outside.Message != "square remark" ||
		outside.RefDetail != "5" || outside.Snapshot().Channel != "" {
		t.Fatalf("report outside a Square = %+v", outside)
	}
	if inSquare.Snapshot().Channel != strconv.FormatUint(squareThemeID, 10) || inSquare.RefDetail != "6" {
		t.Fatalf("report in a Square = %+v (channel %q)", inSquare, inSquare.Snapshot().Channel)
	}
}

// TestRoomPartyReportIsStored: cr_report_chat (13) stores the report as the verified reporter, with the room's host as the
// channel, and sends nothing back.
func TestRoomPartyReportIsStored(t *testing.T) {
	testutil.UseDatabase(t)
	testutil.InstallAccounts(t, withLoginAccounts(nil, 7, 9))
	s, c := newRPClient(t)
	if err := s.handleData(c.req(0, loginBody(7))); err != nil {
		t.Fatal(err)
	}
	c.read()
	s.host = 9
	if err := s.handleData(c.req(13, gatewayReportBody(9, "party remark", 8, "R7"))); err != nil {
		t.Fatal(err)
	}
	c.none()

	rows := reportRows(t)
	if len(rows) != 1 {
		t.Fatalf("stored %d reports, want 1", len(rows))
	}
	r := rows[0]
	if r.Source != store.ReportRoomParty || r.ReporterAid != "7" || r.TargetAid != "9" || r.Reason != "R7" || r.Message != "party remark" ||
		r.RefDetail != "8" || r.Snapshot().Channel != "9" {
		t.Fatalf("room party row = %+v (snapshot %+v)", r, r.Snapshot())
	}
}
