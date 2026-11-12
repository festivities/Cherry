package auth

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cherry/internal/httpx"
)

// DiscordBase is the Discord origin for the token, user and revoke calls and the authorize page. Tests point it at an
// httptest server; production never changes it.
var DiscordBase = "https://discord.com"

// maxDiscordBody caps every Discord response read.
const maxDiscordBody = 16 << 10

// discordHTTP has a timeout and follows no redirect, so a bearer token never goes to another host. Three calls run inside
// one callback, so 5 seconds each keeps the callback under the HTTPS server's 30-second write timeout.
var discordHTTP = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// DiscordApp is a confidential Discord OAuth2 client: its id, its secret and the redirect URI registered for the flow
// that uses it. The players' flow and the staff UI each hold one; they share the application, not the redirect.
type DiscordApp struct {
	ClientID, Secret, Redirect string
}

// DiscordUser is what the identify scope gives (GET users/@me). MFAEnabled is false unless Discord says true.
type DiscordUser struct {
	ID, Username, GlobalName string
	MFAEnabled               bool
}

// Identify exchanges a Discord OAuth code for the user, then revokes the token (best effort). The token lives only in
// this call: it is not stored, not returned and not logged. The failure is "" or the code to send: codeExpired when
// Discord rejects the code, codeServer otherwise. logger gets one line per failure, never a value; nil means log.Default.
func (app DiscordApp) Identify(code string, logger *log.Logger) (DiscordUser, string) {
	if logger == nil {
		logger = log.Default()
	}
	form := url.Values{
		"client_id": {app.ClientID}, "client_secret": {app.Secret}, "grant_type": {"authorization_code"},
		"code": {code}, "redirect_uri": {app.Redirect},
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	status, err := discordDo(http.MethodPost, DiscordBase+"/api/oauth2/token", form.Encode(), "", &tok)
	if err != nil {
		logger.Printf("cherry: auth discord token call failed")
		return DiscordUser{}, codeServer
	}
	if status == http.StatusBadRequest { // Discord rejects a code it does not know, or one used or expired
		return DiscordUser{}, codeExpired
	}
	if status != http.StatusOK || tok.AccessToken == "" {
		logger.Printf("cherry: auth discord token status=%d", status)
		return DiscordUser{}, codeServer
	}
	defer app.revoke(tok.AccessToken, logger)

	var me struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		MFA        bool   `json:"mfa_enabled"`
	}
	status, err = discordDo(http.MethodGet, DiscordBase+"/api/users/@me", "", "Bearer "+tok.AccessToken, &me)
	if err != nil || status != http.StatusOK {
		logger.Printf("cherry: auth discord user call status=%d", status)
		return DiscordUser{}, codeServer
	}
	if !httpx.IsAllDigits(me.ID) || len(me.ID) > 20 {
		logger.Printf("cherry: auth discord user id is not a snowflake")
		return DiscordUser{}, codeServer
	}
	return DiscordUser{ID: me.ID, Username: me.Username, GlobalName: me.GlobalName, MFAEnabled: me.MFA}, ""
}

// revoke asks Discord to drop the token. Best effort: the sign-in does not wait on it, and a failure is logged by status.
func (app DiscordApp) revoke(token string, logger *log.Logger) {
	form := url.Values{"client_id": {app.ClientID}, "client_secret": {app.Secret}, "token": {token}}
	status, err := discordDo(http.MethodPost, DiscordBase+"/api/oauth2/token/revoke", form.Encode(), "", nil)
	if err != nil || status != http.StatusOK {
		logger.Printf("cherry: auth discord revoke status=%d", status)
	}
}

// discordUserID is the players' flow's use of Identify: the id, or the failure code.
func (s *Service) discordUserID(code string) (string, string) {
	u, failure := DiscordApp{ClientID: s.clientID, Secret: s.secret, Redirect: s.redirect}.Identify(code, s.logger)
	return u.ID, failure
}

// AuthorizeURL is Discord's authorize page with the contract's parameters, in the contract's order. consent adds
// prompt=consent (the staff UI: every login is a fresh approval); the players' flow leaves it out.
func AuthorizeURL(clientID, redirectURI, state string, consent bool) string {
	u := DiscordBase + "/oauth2/authorize?response_type=code&client_id=" + url.QueryEscape(clientID) +
		"&scope=identify&redirect_uri=" + url.QueryEscape(redirectURI) + "&state=" + url.QueryEscape(state)
	if consent {
		u += "&prompt=consent"
	}
	return u
}

// discordDo makes one call. A 200 body is decoded into out when out is not nil; any body is read under the cap.
func discordDo(method, target, form, authorization string, out any) (int, error) {
	req, err := http.NewRequest(method, target, strings.NewReader(form))
	if err != nil {
		return 0, err
	}
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := discordHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxDiscordBody)
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	_, _ = io.Copy(io.Discard, body)
	return resp.StatusCode, nil
}
