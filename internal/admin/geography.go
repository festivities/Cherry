package admin

import (
	"fmt"
	"net/http"
	"strconv"

	"cherry/internal/store"
)

// geographyCountryRow formats one country entry for the Geography Insights tables.
type geographyCountryRow struct {
	Code        string
	Name        string
	Region      string
	Total       int
	Share       string
	Online      int
	OnlineShare string
	OnlineBar   int
	New7d       int
	New7dShare  string
	New7dBar    int
	Female      int
	Male        int
	Animal      int
	Bar         int
}

// geographyRegionRow formats one macro-region entry.
type geographyRegionRow struct {
	Name   string
	Total  int
	Share  string
	Online int
	New7d  int
	Bar    int
}

// geographyTypeRow formats one avatar-type breakdown row.
type geographyTypeRow struct {
	Label string
	Count int
	Share string
	Bar   int
}

// geographyView holds all data rendered by templates/geography.html.
type geographyView struct {
	TotalAvatars      int
	PendingCreation   int
	CountriesCount    int
	TopCountryName    string
	TopCountryTotal   int
	TopCountryShare   string
	OnlineMapped      int
	OnlineTotal       int
	OnlineMappedShare string
	UnknownCount      int
	UnknownShare      string
	ByIPCount         int
	OnlineCountries   []geographyCountryRow
	Regions           []geographyRegionRow
	NewCountries      []geographyCountryRow
	AvatarTypes       []geographyTypeRow
	Countries         []geographyCountryRow
}

func formatPct(pct float64) string {
	return fmt.Sprintf("%.1f%%", pct)
}

func toGeoCountryRow(c store.GeographyCountry) geographyCountryRow {
	return geographyCountryRow{
		Code:        c.Code,
		Name:        c.Name,
		Region:      c.Region,
		Total:       c.Total,
		Share:       formatPct(c.Share),
		Online:      c.Online,
		OnlineShare: formatPct(c.OnlineShare),
		OnlineBar:   c.OnlineBar,
		New7d:       c.New7d,
		New7dShare:  formatPct(c.New7dShare),
		New7dBar:    c.New7dBar,
		Female:      c.Female,
		Male:        c.Male,
		Animal:      c.Animal,
		Bar:         c.Bar,
	}
}

func (s *Server) geographyAt() *geographyView {
	conns := s.online()
	onlineAids := make(map[string]bool, len(conns))
	for _, c := range conns {
		onlineAids[strconv.FormatUint(c.Aid, 10)] = true
	}
	rep := store.GeographySummary(onlineAids, s.now())

	gv := &geographyView{
		TotalAvatars:      rep.TotalAvatars,
		PendingCreation:   rep.PendingCreation,
		CountriesCount:    rep.CountriesCount,
		TopCountryName:    rep.TopCountry.Name,
		TopCountryTotal:   rep.TopCountry.Total,
		TopCountryShare:   formatPct(rep.TopCountry.Share),
		OnlineMapped:      rep.OnlineMapped,
		OnlineTotal:       rep.OnlineTotal,
		OnlineMappedShare: formatPct(rep.OnlineMappedPct),
		UnknownCount:      rep.UnknownCount,
		UnknownShare:      formatPct(rep.UnknownPct),
		ByIPCount:         rep.ByIPCount,
		OnlineCountries:   make([]geographyCountryRow, 0, len(rep.OnlineCountries)),
		Regions:           make([]geographyRegionRow, 0, len(rep.Regions)),
		NewCountries:      make([]geographyCountryRow, 0, len(rep.NewCountries)),
		AvatarTypes:       make([]geographyTypeRow, 0, len(rep.AvatarTypes)),
		Countries:         make([]geographyCountryRow, 0, len(rep.Countries)),
	}
	for _, c := range rep.OnlineCountries {
		gv.OnlineCountries = append(gv.OnlineCountries, toGeoCountryRow(c))
	}
	for _, r := range rep.Regions {
		gv.Regions = append(gv.Regions, geographyRegionRow{
			Name:   r.Name,
			Total:  r.Total,
			Share:  formatPct(r.Share),
			Online: r.Online,
			New7d:  r.New7d,
			Bar:    r.Bar,
		})
	}
	for _, c := range rep.NewCountries {
		gv.NewCountries = append(gv.NewCountries, toGeoCountryRow(c))
	}
	for _, t := range rep.AvatarTypes {
		gv.AvatarTypes = append(gv.AvatarTypes, geographyTypeRow{
			Label: t.Label,
			Count: t.Count,
			Share: formatPct(t.Share),
			Bar:   t.Bar,
		})
	}
	for _, c := range rep.Countries {
		gv.Countries = append(gv.Countries, toGeoCountryRow(c))
	}
	return gv
}

// handleGeography: GET /geography.
func (s *Server) handleGeography(w http.ResponseWriter, r *http.Request, sess session) {
	v := s.base(r, sess, "Geography")
	v.Geography = s.geographyAt()
	s.render(w, http.StatusOK, "geography", v)
}
