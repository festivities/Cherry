package store

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Macro-region names used by GeographySummary.
const (
	RegionSoutheastAsia = "Southeast Asia"
	RegionEastAsia      = "East Asia"
	RegionSouthWestAsia = "South & West Asia"
	RegionOceania       = "Oceania"
	RegionNorthAmerica  = "North America"
	RegionSouthAmerica  = "South America"
	RegionEurope        = "Europe"
	RegionAfrica        = "Africa"
	RegionOther         = "Other"
	RegionUnknown       = "Unknown"
)

const geographyNewTop = 15

type countryMeta struct {
	Name   string
	Region string
}

// countryTable maps ISO-3166-1 alpha-2 codes to display names and macro-regions.
var countryTable = map[string]countryMeta{
	// Southeast Asia
	"ID": {"Indonesia", RegionSoutheastAsia},
	"TH": {"Thailand", RegionSoutheastAsia},
	"MY": {"Malaysia", RegionSoutheastAsia},
	"SG": {"Singapore", RegionSoutheastAsia},
	"VN": {"Vietnam", RegionSoutheastAsia},
	"PH": {"Philippines", RegionSoutheastAsia},
	"BN": {"Brunei", RegionSoutheastAsia},
	"KH": {"Cambodia", RegionSoutheastAsia},
	"LA": {"Laos", RegionSoutheastAsia},
	"MM": {"Myanmar", RegionSoutheastAsia},
	"TL": {"Timor-Leste", RegionSoutheastAsia},

	// East Asia
	"JP": {"Japan", RegionEastAsia},
	"KR": {"South Korea", RegionEastAsia},
	"CN": {"China", RegionEastAsia},
	"TW": {"Taiwan", RegionEastAsia},
	"HK": {"Hong Kong", RegionEastAsia},
	"MO": {"Macau", RegionEastAsia},
	"MN": {"Mongolia", RegionEastAsia},
	"KP": {"North Korea", RegionEastAsia},

	// South & West Asia
	"IN": {"India", RegionSouthWestAsia},
	"PK": {"Pakistan", RegionSouthWestAsia},
	"BD": {"Bangladesh", RegionSouthWestAsia},
	"LK": {"Sri Lanka", RegionSouthWestAsia},
	"NP": {"Nepal", RegionSouthWestAsia},
	"BT": {"Bhutan", RegionSouthWestAsia},
	"MV": {"Maldives", RegionSouthWestAsia},
	"AF": {"Afghanistan", RegionSouthWestAsia},
	"AE": {"United Arab Emirates", RegionSouthWestAsia},
	"SA": {"Saudi Arabia", RegionSouthWestAsia},
	"QA": {"Qatar", RegionSouthWestAsia},
	"KW": {"Kuwait", RegionSouthWestAsia},
	"BH": {"Bahrain", RegionSouthWestAsia},
	"OM": {"Oman", RegionSouthWestAsia},
	"JO": {"Jordan", RegionSouthWestAsia},
	"LB": {"Lebanon", RegionSouthWestAsia},
	"IL": {"Israel", RegionSouthWestAsia},
	"PS": {"Palestine", RegionSouthWestAsia},
	"TR": {"Turkey", RegionSouthWestAsia},
	"IR": {"Iran", RegionSouthWestAsia},
	"IQ": {"Iraq", RegionSouthWestAsia},
	"SY": {"Syria", RegionSouthWestAsia},
	"YE": {"Yemen", RegionSouthWestAsia},
	"KZ": {"Kazakhstan", RegionSouthWestAsia},
	"KG": {"Kyrgyzstan", RegionSouthWestAsia},
	"UZ": {"Uzbekistan", RegionSouthWestAsia},
	"TJ": {"Tajikistan", RegionSouthWestAsia},
	"TM": {"Turkmenistan", RegionSouthWestAsia},
	"AZ": {"Azerbaijan", RegionSouthWestAsia},
	"GE": {"Georgia", RegionSouthWestAsia},
	"AM": {"Armenia", RegionSouthWestAsia},
	"IO": {"British Indian Ocean Territory", RegionSouthWestAsia},

	// Oceania
	"AU": {"Australia", RegionOceania},
	"NZ": {"New Zealand", RegionOceania},
	"FJ": {"Fiji", RegionOceania},
	"PG": {"Papua New Guinea", RegionOceania},
	"GU": {"Guam", RegionOceania},
	"WS": {"Samoa", RegionOceania},
	"TO": {"Tonga", RegionOceania},
	"NC": {"New Caledonia", RegionOceania},
	"AS": {"American Samoa", RegionOceania},
	"CC": {"Cocos (Keeling) Islands", RegionOceania},
	"CK": {"Cook Islands", RegionOceania},
	"CX": {"Christmas Island", RegionOceania},
	"FM": {"Micronesia", RegionOceania},
	"KI": {"Kiribati", RegionOceania},
	"MH": {"Marshall Islands", RegionOceania},
	"MP": {"Northern Mariana Islands", RegionOceania},
	"NF": {"Norfolk Island", RegionOceania},
	"NR": {"Nauru", RegionOceania},
	"NU": {"Niue", RegionOceania},
	"PF": {"French Polynesia", RegionOceania},
	"PN": {"Pitcairn Islands", RegionOceania},
	"PW": {"Palau", RegionOceania},
	"SB": {"Solomon Islands", RegionOceania},
	"TK": {"Tokelau", RegionOceania},
	"TV": {"Tuvalu", RegionOceania},
	"UM": {"U.S. Minor Outlying Islands", RegionOceania},
	"VU": {"Vanuatu", RegionOceania},
	"WF": {"Wallis and Futuna", RegionOceania},

	// North America
	"US": {"United States", RegionNorthAmerica},
	"CA": {"Canada", RegionNorthAmerica},
	"MX": {"Mexico", RegionNorthAmerica},
	"BZ": {"Belize", RegionNorthAmerica},
	"CR": {"Costa Rica", RegionNorthAmerica},
	"PA": {"Panama", RegionNorthAmerica},
	"GT": {"Guatemala", RegionNorthAmerica},
	"HN": {"Honduras", RegionNorthAmerica},
	"SV": {"El Salvador", RegionNorthAmerica},
	"NI": {"Nicaragua", RegionNorthAmerica},
	"DO": {"Dominican Republic", RegionNorthAmerica},
	"HT": {"Haiti", RegionNorthAmerica},
	"PR": {"Puerto Rico", RegionNorthAmerica},
	"JM": {"Jamaica", RegionNorthAmerica},
	"CU": {"Cuba", RegionNorthAmerica},
	"TT": {"Trinidad and Tobago", RegionNorthAmerica},
	"BS": {"Bahamas", RegionNorthAmerica},
	"BB": {"Barbados", RegionNorthAmerica},
	"AG": {"Antigua and Barbuda", RegionNorthAmerica},
	"AI": {"Anguilla", RegionNorthAmerica},
	"AW": {"Aruba", RegionNorthAmerica},
	"BL": {"Saint Barthélemy", RegionNorthAmerica},
	"BM": {"Bermuda", RegionNorthAmerica},
	"BQ": {"Caribbean Netherlands", RegionNorthAmerica},
	"CW": {"Curaçao", RegionNorthAmerica},
	"DM": {"Dominica", RegionNorthAmerica},
	"GD": {"Grenada", RegionNorthAmerica},
	"GL": {"Greenland", RegionNorthAmerica},
	"GP": {"Guadeloupe", RegionNorthAmerica},
	"KN": {"Saint Kitts and Nevis", RegionNorthAmerica},
	"KY": {"Cayman Islands", RegionNorthAmerica},
	"LC": {"Saint Lucia", RegionNorthAmerica},
	"MF": {"Saint Martin", RegionNorthAmerica},
	"MQ": {"Martinique", RegionNorthAmerica},
	"MS": {"Montserrat", RegionNorthAmerica},
	"PM": {"Saint Pierre and Miquelon", RegionNorthAmerica},
	"SX": {"Sint Maarten", RegionNorthAmerica},
	"TC": {"Turks and Caicos Islands", RegionNorthAmerica},
	"VC": {"Saint Vincent and the Grenadines", RegionNorthAmerica},
	"VG": {"British Virgin Islands", RegionNorthAmerica},
	"VI": {"U.S. Virgin Islands", RegionNorthAmerica},

	// South America
	"BR": {"Brazil", RegionSouthAmerica},
	"AR": {"Argentina", RegionSouthAmerica},
	"CL": {"Chile", RegionSouthAmerica},
	"CO": {"Colombia", RegionSouthAmerica},
	"PE": {"Peru", RegionSouthAmerica},
	"VE": {"Venezuela", RegionSouthAmerica},
	"EC": {"Ecuador", RegionSouthAmerica},
	"BO": {"Bolivia", RegionSouthAmerica},
	"PY": {"Paraguay", RegionSouthAmerica},
	"UY": {"Uruguay", RegionSouthAmerica},
	"GY": {"Guyana", RegionSouthAmerica},
	"SR": {"Suriname", RegionSouthAmerica},
	"FK": {"Falkland Islands", RegionSouthAmerica},
	"GF": {"French Guiana", RegionSouthAmerica},

	// Europe
	"GB": {"United Kingdom", RegionEurope},
	"DE": {"Germany", RegionEurope},
	"FR": {"France", RegionEurope},
	"IT": {"Italy", RegionEurope},
	"ES": {"Spain", RegionEurope},
	"PT": {"Portugal", RegionEurope},
	"NL": {"Netherlands", RegionEurope},
	"BE": {"Belgium", RegionEurope},
	"CH": {"Switzerland", RegionEurope},
	"AT": {"Austria", RegionEurope},
	"SE": {"Sweden", RegionEurope},
	"NO": {"Norway", RegionEurope},
	"DK": {"Denmark", RegionEurope},
	"FI": {"Finland", RegionEurope},
	"IS": {"Iceland", RegionEurope},
	"IE": {"Ireland", RegionEurope},
	"PL": {"Poland", RegionEurope},
	"CZ": {"Czechia", RegionEurope},
	"SK": {"Slovakia", RegionEurope},
	"HU": {"Hungary", RegionEurope},
	"RO": {"Romania", RegionEurope},
	"BG": {"Bulgaria", RegionEurope},
	"GR": {"Greece", RegionEurope},
	"UA": {"Ukraine", RegionEurope},
	"RU": {"Russia", RegionEurope},
	"BY": {"Belarus", RegionEurope},
	"HR": {"Croatia", RegionEurope},
	"RS": {"Serbia", RegionEurope},
	"ME": {"Montenegro", RegionEurope},
	"SI": {"Slovenia", RegionEurope},
	"EE": {"Estonia", RegionEurope},
	"LV": {"Latvia", RegionEurope},
	"LT": {"Lithuania", RegionEurope},
	"LU": {"Luxembourg", RegionEurope},
	"MT": {"Malta", RegionEurope},
	"CY": {"Cyprus", RegionEurope},
	"AL": {"Albania", RegionEurope},
	"BA": {"Bosnia and Herzegovina", RegionEurope},
	"MK": {"North Macedonia", RegionEurope},
	"MD": {"Moldova", RegionEurope},
	"AD": {"Andorra", RegionEurope},
	"LI": {"Liechtenstein", RegionEurope},
	"MC": {"Monaco", RegionEurope},
	"SM": {"San Marino", RegionEurope},
	"VA": {"Vatican City", RegionEurope},
	"AX": {"Åland Islands", RegionEurope},
	"FO": {"Faroe Islands", RegionEurope},
	"GG": {"Guernsey", RegionEurope},
	"GI": {"Gibraltar", RegionEurope},
	"IM": {"Isle of Man", RegionEurope},
	"JE": {"Jersey", RegionEurope},
	"SJ": {"Svalbard and Jan Mayen", RegionEurope},

	// Africa
	"ZA": {"South Africa", RegionAfrica},
	"EG": {"Egypt", RegionAfrica},
	"MA": {"Morocco", RegionAfrica},
	"NG": {"Nigeria", RegionAfrica},
	"KE": {"Kenya", RegionAfrica},
	"GH": {"Ghana", RegionAfrica},
	"ET": {"Ethiopia", RegionAfrica},
	"TZ": {"Tanzania", RegionAfrica},
	"UG": {"Uganda", RegionAfrica},
	"DZ": {"Algeria", RegionAfrica},
	"TN": {"Tunisia", RegionAfrica},
	"CM": {"Cameroon", RegionAfrica},
	"CI": {"Côte d'Ivoire", RegionAfrica},
	"SN": {"Senegal", RegionAfrica},
	"MU": {"Mauritius", RegionAfrica},
	"SD": {"Sudan", RegionAfrica},
	"SS": {"South Sudan", RegionAfrica},
	"LY": {"Libya", RegionAfrica},
	"AO": {"Angola", RegionAfrica},
	"BF": {"Burkina Faso", RegionAfrica},
	"BI": {"Burundi", RegionAfrica},
	"BJ": {"Benin", RegionAfrica},
	"BW": {"Botswana", RegionAfrica},
	"CD": {"DR Congo", RegionAfrica},
	"CF": {"Central African Republic", RegionAfrica},
	"CG": {"Republic of the Congo", RegionAfrica},
	"CV": {"Cabo Verde", RegionAfrica},
	"DJ": {"Djibouti", RegionAfrica},
	"EH": {"Western Sahara", RegionAfrica},
	"ER": {"Eritrea", RegionAfrica},
	"GA": {"Gabon", RegionAfrica},
	"GM": {"Gambia", RegionAfrica},
	"GN": {"Guinea", RegionAfrica},
	"GQ": {"Equatorial Guinea", RegionAfrica},
	"GW": {"Guinea-Bissau", RegionAfrica},
	"KM": {"Comoros", RegionAfrica},
	"LR": {"Liberia", RegionAfrica},
	"LS": {"Lesotho", RegionAfrica},
	"MG": {"Madagascar", RegionAfrica},
	"ML": {"Mali", RegionAfrica},
	"MR": {"Mauritania", RegionAfrica},
	"MW": {"Malawi", RegionAfrica},
	"MZ": {"Mozambique", RegionAfrica},
	"NA": {"Namibia", RegionAfrica},
	"NE": {"Niger", RegionAfrica},
	"RE": {"Réunion", RegionAfrica},
	"RW": {"Rwanda", RegionAfrica},
	"SC": {"Seychelles", RegionAfrica},
	"SH": {"Saint Helena", RegionAfrica},
	"SL": {"Sierra Leone", RegionAfrica},
	"SO": {"Somalia", RegionAfrica},
	"ST": {"São Tomé and Príncipe", RegionAfrica},
	"SZ": {"Eswatini", RegionAfrica},
	"TD": {"Chad", RegionAfrica},
	"TG": {"Togo", RegionAfrica},
	"YT": {"Mayotte", RegionAfrica},
	"ZM": {"Zambia", RegionAfrica},
	"ZW": {"Zimbabwe", RegionAfrica},
}

// CountryInfo normalizes an account's raw country string and returns its canonical code ("" when unset),
// display name ("Unknown" when unset, or the code itself if unmapped), and macro-region.
func CountryInfo(raw string) (code, name, region string) {
	code = strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return "", "Unknown", RegionUnknown
	}
	if m, ok := countryTable[code]; ok {
		return code, m.Name, m.Region
	}
	return code, code, RegionOther
}

// GeographyCountry aggregates avatar, online, 7-day creation, and avatar-type counts for one country bucket.
type GeographyCountry struct {
	Code        string
	Name        string
	Region      string
	Total       int
	Share       float64
	Online      int
	OnlineShare float64
	OnlineBar   int
	New7d       int
	New7dShare  float64
	New7dBar    int
	Female      int
	Male        int
	Animal      int
	UnsetGender int
	Bar         int
}

// GeographyRegion aggregates counts for one macro-region.
type GeographyRegion struct {
	Name   string
	Total  int
	Share  float64
	Online int
	New7d  int
	Bar    int
}

// GeographyAvatarType aggregates counts for one avatar gender/type bucket.
type GeographyAvatarType struct {
	Label string
	Count int
	Share float64
	Bar   int
}

// GeographyReport is the full snapshot returned by GeographySummary.
type GeographyReport struct {
	TotalAvatars    int
	PendingCreation int
	CountriesCount  int
	TopCountry      GeographyCountry
	OnlineTotal     int
	OnlineMapped    int
	OnlineMappedPct float64
	UnknownCount    int
	UnknownPct      float64
	New7dTotal      int
	Regions         []GeographyRegion
	Countries       []GeographyCountry
	OnlineCountries []GeographyCountry
	NewCountries    []GeographyCountry
	AvatarTypes     []GeographyAvatarType
}

// shareAndBar computes percentage (0..100) and a 5%-step CSS bar width (0..100).
func shareAndBar(part, total int) (float64, int) {
	if total <= 0 || part <= 0 {
		return 0, 0
	}
	pct := float64(part) * 100 / float64(total)
	bar := ((part*100/total + 2) / 5) * 5
	bar = min(max(bar, 0), 100)
	return pct, bar
}

// GeographySummary scans accountList under AccountsMu and cross-references onlineAids to build geographic
// and avatar-type insights as of now. Accounts without a created avatar (Aid "" or "0") are counted in
// PendingCreation and excluded from avatar totals.
func GeographySummary(onlineAids map[string]bool, now time.Time) GeographyReport {
	cutoffMs := now.Add(-7 * 24 * time.Hour).UnixMilli()

	var rep GeographyReport
	for aid, on := range onlineAids {
		if on && aid != "" && aid != "0" {
			rep.OnlineTotal++
		}
	}

	byCountry := map[string]*GeographyCountry{}
	byRegion := map[string]*GeographyRegion{}
	var female, male, animal, unsetGender int

	AccountsMu.Lock()
	for _, acc := range accountList {
		if acc.Aid == "" || acc.Aid == "0" {
			rep.PendingCreation++
			continue
		}
		rep.TotalAvatars++

		code, name, region := CountryInfo(acc.Country)
		c := byCountry[code]
		if c == nil {
			c = &GeographyCountry{Code: code, Name: name, Region: region}
			byCountry[code] = c
		}
		c.Total++

		r := byRegion[region]
		if r == nil {
			r = &GeographyRegion{Name: region}
			byRegion[region] = r
		}
		r.Total++

		if onlineAids[acc.Aid] {
			c.Online++
			r.Online++
			if code != "" {
				rep.OnlineMapped++
			}
		}
		if acc.AvatarCreated > 0 && acc.AvatarCreated >= cutoffMs {
			c.New7d++
			r.New7d++
			rep.New7dTotal++
		}

		switch strings.ToUpper(strings.TrimSpace(acc.Gender)) {
		case "FEMALE":
			c.Female++
			female++
		case "MALE":
			c.Male++
			male++
		case "ANIMAL":
			c.Animal++
			animal++
		default:
			c.UnsetGender++
			unsetGender++
		}
	}
	AccountsMu.Unlock()

	if unk := byCountry[""]; unk != nil {
		rep.UnknownCount = unk.Total
		rep.UnknownPct, _ = shareAndBar(rep.UnknownCount, rep.TotalAvatars)
	}
	rep.OnlineMappedPct, _ = shareAndBar(rep.OnlineMapped, rep.OnlineTotal)

	onlineDenom := max(rep.OnlineTotal, 1)
	if rep.OnlineTotal == 0 {
		onlineDenom = 0
	}

	rep.Countries = make([]GeographyCountry, 0, len(byCountry))
	for _, c := range byCountry {
		c.Share, c.Bar = shareAndBar(c.Total, rep.TotalAvatars)
		c.OnlineShare, c.OnlineBar = shareAndBar(c.Online, onlineDenom)
		c.New7dShare, c.New7dBar = shareAndBar(c.New7d, rep.New7dTotal)
		rep.Countries = append(rep.Countries, *c)
		if c.Code != "" {
			rep.CountriesCount++
		}
	}

	slices.SortFunc(rep.Countries, func(a, b GeographyCountry) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.Code, b.Code))
	})

	for _, c := range rep.Countries {
		if c.Code != "" {
			rep.TopCountry = c
			break
		}
	}

	rep.Regions = make([]GeographyRegion, 0, len(byRegion))
	for _, r := range byRegion {
		r.Share, r.Bar = shareAndBar(r.Total, rep.TotalAvatars)
		rep.Regions = append(rep.Regions, *r)
	}
	slices.SortFunc(rep.Regions, func(a, b GeographyRegion) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.Name, b.Name))
	})

	for _, c := range rep.Countries {
		if c.Online > 0 {
			rep.OnlineCountries = append(rep.OnlineCountries, c)
		}
		if c.New7d > 0 {
			rep.NewCountries = append(rep.NewCountries, c)
		}
	}
	slices.SortFunc(rep.OnlineCountries, func(a, b GeographyCountry) int {
		return cmp.Or(cmp.Compare(b.Online, a.Online), cmp.Compare(b.Total, a.Total), cmp.Compare(a.Code, b.Code))
	})
	slices.SortFunc(rep.NewCountries, func(a, b GeographyCountry) int {
		return cmp.Or(cmp.Compare(b.New7d, a.New7d), cmp.Compare(b.Total, a.Total), cmp.Compare(a.Code, b.Code))
	})
	if len(rep.NewCountries) > geographyNewTop {
		rep.NewCountries = rep.NewCountries[:geographyNewTop]
	}

	for _, g := range []struct {
		label string
		count int
	}{
		{"Female", female},
		{"Male", male},
		{"Animal", animal},
		{"Unset", unsetGender},
	} {
		sh, bar := shareAndBar(g.count, rep.TotalAvatars)
		rep.AvatarTypes = append(rep.AvatarTypes, GeographyAvatarType{
			Label: g.label,
			Count: g.count,
			Share: sh,
			Bar:   bar,
		})
	}

	return rep
}
