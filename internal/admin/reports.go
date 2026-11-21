package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/auth"
	"cherry/internal/store"
)

// The report queue (PLAN Roadmap 8, staff "Moderation"; schema v9 reports): the player reports the game stored, one report with
// the copy of what it named as it was when the report came in, and resolve or dismiss with a note (audited). The queue opens
// on the open reports, newest first; a status filter and a page marker come from the query. The reporter and the target link
// to their player page, or to the deleted avatar's record when the account is gone and only that avatar remains.

const reportPage = 50 // reports on a queue page

// reportStatusFilters are the queue's filters; "all" is every status.
var reportStatusFilters = []string{store.ReportOpen, store.ReportResolved, store.ReportDismissed, "all"}

// reportSourceLabels name the sources as the queue shows them.
var reportSourceLabels = map[string]string{
	store.ReportChat: "Chat (1:1)", store.ReportChatImg: "Chat (picture)", store.ReportTChat: "Lounge chat",
	store.ReportDiary: "Diary post", store.ReportDiaryComment: "Diary comment", store.ReportGuestbook: "Guestbook entry",
	store.ReportGuestbookReply: "Guestbook reply", store.ReportCircle: "Circle chat", store.ReportSquare: "Square chat",
	store.ReportRoomParty: "Room Party chat",
}

// reportReasonLabels are the client's reason strings (5035 to 5042) by their code. A code outside them shows as it was sent.
var reportReasonLabels = map[string]string{
	"R1": "Dating/Meeting purposes", "R2": "Harvesting personal info", "R3": "Nuisance/Harassment", "R4": "Illicit activities",
	"R5": "Obscenity", "R6": "Spamming/Advertising", "R7": "Third-party rights violations", "R8": "Other violations",
}

// reportListRow is one row of the queue (and of the search's report group).
type reportListRow struct {
	ID                                     int64
	Href, Created, Source, Reason, Message string
	Status                                 string
	Reporter, ReporterHref                 string
	Target, TargetHref                     string
}

// reportFilterLink is one filter tab of the queue.
type reportFilterLink struct {
	Href, Label string
	Current     bool
}

// reportsPage is the queue: its rows, the filter tabs and the link to the older page.
type reportsPage struct {
	Rows      []reportListRow
	Filters   []reportFilterLink
	Status    string
	OlderHref string
}

// reportLineView is one chat line of a report's snapshot.
type reportLineView struct {
	Time, Sender, SenderHref, Name, Text string
}

// reportItemView is the reported diary post, comment, guestbook entry or reply as it was stored when the report came in.
type reportItemView struct {
	Found                 bool
	No, Post, Author      string
	AuthorHref            string
	Nickname, Date, Title string
	Text                  string
}

// reportDetail is one report on its page: the facts, the snapshot and the handling.
type reportDetail struct {
	ID                                  int64
	Created, Source, ReasonLabel        string
	Reporter, ReporterHref              string
	Target, TargetHref                  string
	Message, RefID, RefDetail           string
	Status, HandledBy, HandledAt, Note  string
	Open                                bool
	TargetReports                       int    // reports against the target in any status
	TargetSearchHref                    string // the search page for the target's reports
	Host, HostHref                      string // guestbook owner, when the route named one
	Item                                *reportItemView
	SnapshotNote, ChatKind, ChatChannel string
	Line                                *reportLineView
	Lines                               []reportLineView
	ChatHref                            string // the chat log around the report's time (chat sources)
}

// reportsHref is the queue with the filter and, when before is set, the page marker.
func reportsHref(status string, before int64) string {
	q := url.Values{"status": {status}}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	return "/reports?" + q.Encode()
}

// reportNumber reads the {id} of a report route; ok is false (and the reply written) for a malformed one.
func (s *Server) reportNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.fail(w, http.StatusBadRequest, "The report number is not valid.")
		return 0, false
	}
	return id, true
}

// aidHref is the page of an avatar id: its player page when the account exists, else its newest deleted avatar's record,
// else "" (no link).
func (s *Server) aidHref(aid string) string {
	if id, ok := store.PlayerRowOfAid(aid); ok {
		return playerPath(id)
	}
	if d, ok, err := store.DeletedAvatarOfAid(aid); err == nil && ok {
		return deletedPath(d.ID)
	}
	return ""
}

// reportListRows turns the reports into the rows of a list.
func (s *Server) reportListRows(rows []store.ReportRow) []reportListRow {
	out := []reportListRow{}
	for _, r := range rows {
		out = append(out, reportListRow{
			ID: r.ID, Href: fmt.Sprintf("/reports/%d", r.ID), Created: rfcDate(r.Created),
			Source: sourceLabel(r.Source), Reason: reasonLabel(r.Reason), Message: cutText(r.Message, writingText), Status: r.Status,
			Reporter: r.ReporterAid, ReporterHref: s.aidHref(r.ReporterAid),
			Target: r.TargetAid, TargetHref: s.aidHref(r.TargetAid),
		})
	}
	return out
}

// sourceLabel is the queue's name of a report source; an unknown source shows as it is.
func sourceLabel(src string) string {
	if l, ok := reportSourceLabels[src]; ok {
		return l
	}
	return src
}

// reasonLabel is the code and the client's reason text for it (R3: Nuisance/Harassment); a code outside R1 to R8 shows as it was sent.
func reasonLabel(code string) string {
	if l, ok := reportReasonLabels[code]; ok {
		return code + ": " + l
	}
	return code
}

// handleReports: GET /reports. The open reports by default; ?status= picks open, resolved, dismissed or all.
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	status := strings.TrimSpace(q.Get("status"))
	if status == "" {
		status = store.ReportOpen
	}
	if !slices.Contains(reportStatusFilters, status) {
		s.fail(w, http.StatusBadRequest, "The status is open, resolved, dismissed or all.")
		return
	}
	var before int64
	if raw := q["before"]; len(raw) > 0 {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	dbStatus := status
	if status == "all" {
		dbStatus = ""
	}
	rows, err := store.ListReports(dbStatus, before, reportPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	page := reportsPage{Rows: s.reportListRows(rows), Status: status}
	for _, f := range reportStatusFilters {
		page.Filters = append(page.Filters, reportFilterLink{Href: reportsHref(f, 0), Label: strings.ToUpper(f[:1]) + f[1:], Current: f == status})
	}
	if len(rows) == reportPage {
		page.OlderHref = reportsHref(status, rows[len(rows)-1].ID)
	}
	v := s.base(r, sess, "Reports")
	v.Reports = page
	s.render(w, http.StatusOK, "reports", v)
}

// handleReport: GET /reports/{id}. The report, what it named as stored then, and the resolve and dismiss forms while it is open.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := s.reportNumber(w, r)
	if !ok {
		return
	}
	row, found, err := store.ReportByID(id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !found {
		s.fail(w, http.StatusNotFound, "No such report.")
		return
	}
	det, err := s.reportDetailOf(row)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, fmt.Sprintf("Report #%d", id))
	v.Crumbs = []link{{Href: "/reports", Label: "Reports"}, {Label: fmt.Sprintf("#%d", id)}}
	v.Report = det
	s.render(w, http.StatusOK, "report", v)
}

// reportDetailOf builds the page of one report from its row and its snapshot.
func (s *Server) reportDetailOf(row store.ReportRow) (reportDetail, error) {
	others, err := store.CountReportsAgainst(row.TargetAid)
	if err != nil {
		return reportDetail{}, err
	}
	det := reportDetail{
		ID: row.ID, Created: rfcDate(row.Created), Source: sourceLabel(row.Source), ReasonLabel: reasonLabel(row.Reason),
		Reporter: row.ReporterAid, ReporterHref: s.aidHref(row.ReporterAid), Target: row.TargetAid, TargetHref: s.aidHref(row.TargetAid),
		Message: row.Message, RefID: row.RefID, RefDetail: row.RefDetail, Status: row.Status, Note: row.Note,
		Open: row.Status == store.ReportOpen, TargetReports: others, TargetSearchHref: "/search?q=" + url.QueryEscape(row.TargetAid),
	}
	if row.HandledBy != "" {
		det.HandledBy, det.HandledAt = auth.MaskIdentity(row.HandledBy), rfcDate(row.HandledAt)
	}
	snap := row.Snapshot()
	det.SnapshotNote, det.ChatKind, det.ChatChannel = snap.Note, snap.Kind, snap.Channel
	if snap.Host != "" {
		det.Host, det.HostHref = snap.Host, s.aidHref(snap.Host)
	}
	if it := snap.Item; it != nil {
		det.Item = &reportItemView{Found: it.Found, No: it.No, Post: it.Post, Author: it.Author, AuthorHref: s.aidHref(it.Author),
			Nickname: it.Nickname, Date: msDate(it.Date), Title: it.Title, Text: it.Text}
	}
	if snap.Line != nil {
		l := s.reportLine(*snap.Line)
		det.Line = &l
	}
	for _, l := range snap.Lines {
		det.Lines = append(det.Lines, s.reportLine(l))
	}
	if snap.Kind != "" {
		f := store.ChatFilter{Kind: snap.Kind, Channel: snap.Channel}
		if snap.Channel == "" {
			f.Player = row.TargetAid // a lounge report names no room: the target's lines
		}
		if t, err := time.Parse(time.RFC3339, row.Created); err == nil {
			f.Around = t.UnixMilli()
		}
		det.ChatHref = chatHref(f, 0)
	}
	return det, nil
}

// reportLine turns one snapshot line into the page's row, the sender linked to its player page where one exists.
func (s *Server) reportLine(l store.ReportLine) reportLineView {
	return reportLineView{Time: rfcDate(l.Time), Sender: l.Sender, SenderHref: s.aidHref(l.Sender), Name: l.Name, Text: l.Text}
}

// handleReportResolve: POST /reports/{id}/resolve. The form: note (required).
func (s *Server) handleReportResolve(w http.ResponseWriter, r *http.Request, sess session) {
	s.decideReport(w, r, sess, store.ReportResolved, "report-resolved")
}

// handleReportDismiss: POST /reports/{id}/dismiss. The form: note (required).
func (s *Server) handleReportDismiss(w http.ResponseWriter, r *http.Request, sess session) {
	s.decideReport(w, r, sess, store.ReportDismissed, "report-dismissed")
}

// decideReport saves the status of an open report with the note and its audit row, then shows the report with the notice.
func (s *Server) decideReport(w http.ResponseWriter, r *http.Request, sess session, status, notice string) {
	id, ok := s.reportNumber(w, r)
	if !ok {
		return
	}
	if err := store.SetReportStatus(id, status, r.PostFormValue("note"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/reports/%d?notice=%s", id, notice))
}
