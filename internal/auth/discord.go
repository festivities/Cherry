package auth

import (
	"encoding/json"
	"io"
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

// discordUserID exchanges a Discord OAuth code for the user's id, then revokes the token (best effort). The token lives
// only in this call: it is not stored, not returned and not logged. It returns the failure code to send, or "".
func (s *Service) discordUserID(code string) (id, failure string) {
	form := url.Values{
		"client_id": {s.clientID}, "client_secret": {s.secret}, "grant_type": {"authorization_code"},
		"code": {code}, "redirect_uri": {s.redirect},
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	status, err := discordDo(http.MethodPost, DiscordBase+"/api/oauth2/token", form.Encode(), "", &tok)
	if err != nil {
		s.logger.Printf("cherry: auth discord token call failed")
		return "", codeServer
	}
	if status == http.StatusBadRequest { // Discord rejects a code it does not know, or one used or expired
		return "", codeExpired
	}
	if status != http.StatusOK || tok.AccessToken == "" {
		s.logger.Printf("cherry: auth discord token status=%d", status)
		return "", codeServer
	}
	defer s.revoke(tok.AccessToken)

	var me struct {
		ID string `json:"id"`
	}
	status, err = discordDo(http.MethodGet, DiscordBase+"/api/users/@me", "", "Bearer "+tok.AccessToken, &me)
	if err != nil || status != http.StatusOK {
		s.logger.Printf("cherry: auth discord user call status=%d", status)
		return "", codeServer
	}
	if !httpx.IsAllDigits(me.ID) || len(me.ID) > 20 {
		s.logger.Printf("cherry: auth discord user id is not a snowflake")
		return "", codeServer
	}
	return me.ID, ""
}

// revoke asks Discord to drop the token. Best effort: the sign-in does not wait on it, and a failure is logged by status.
func (s *Service) revoke(token string) {
	form := url.Values{"client_id": {s.clientID}, "client_secret": {s.secret}, "token": {token}}
	status, err := discordDo(http.MethodPost, DiscordBase+"/api/oauth2/token/revoke", form.Encode(), "", nil)
	if err != nil || status != http.StatusOK {
		s.logger.Printf("cherry: auth discord revoke status=%d", status)
	}
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
