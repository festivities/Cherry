// Package config is Cherry's startup configuration: the listeners, the state and cache directories, the public-server
// switch, the auth mode and the operator hostnames. Each setting comes from its flag, else its environment variable,
// else the JSON file (-config or CHERRY_CONFIG), else its default. Standard library only: every package may use it.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"
)

// Auth modes (auth.mode). lab-guest is today's device-bound guest login; discord signs players in through Discord;
// dev-fake replaces Discord with a test provider (CONTRACT §3, §9).
const (
	AuthLabGuest = "lab-guest"
	AuthDiscord  = "discord"
	AuthDevFake  = "dev-fake"
)

// Sign-up modes (auth.signup): who may get an account for the first time (CONTRACT §3, PLAN P6).
const (
	SignupOpen      = "open"
	SignupAllowlist = "allowlist"
	SignupClosed    = "closed"
)

// Config is the effective configuration. A listen address is a net.Listen address; DNS "" means the sink is off.
type Config struct {
	// The staff UI (listen.staff and staff.*; internal/admin). Empty Staff means the staff UI is off.
	Staff, StaffHostname, StaffCert, StaffKey, StaffOwner, StaffAllowlistFile, StaffRedirect string

	HTTPS, HTTP, Session, Gateway, DNS, Admin string
	DataDir, CacheDir                         string
	Public                                    bool
	Hostnames                                 []string
	AuthMode                                  string
	Signup, AllowlistFile                     string // resolved by parse: Signup is never "" there (DefaultSignup)
	DiscordClientID, DiscordSecretFile        string // the secret itself is read from the file, never a setting
	DiscordRedirect                           string
	ServerName                                string
	File                                      string // the JSON file read, "" when none
}

// setting is one row of the table. The flag is the key with dashes for dots, the environment name is CHERRY_ plus the
// key in upper case with underscores. An empty default for a directory means the user cache directory plus Cherry.
type setting struct{ key, def, help string }

var settings = []setting{
	{"listen.https", ":443", "HTTPS listener: game, web pages, certificates"},
	{"listen.http", ":80", "plain HTTP listener"},
	{"listen.session", ":10123", "session listener (LO frames)"},
	{"listen.gateway", ":10000", "gateway frame listener"},
	{"listen.dns", ":53", "DNS sink and relay, UDP; empty turns it off; server.public turns it off unless set"},
	{"listen.admin", "127.0.0.1:8099", "loopback staff tool"},
	{"data.dir", "", "state: stores, media, config, secrets, converted items"},
	{"cache.dir", "", "re-downloadable caches: static, dp-cache"},
	{"server.public", "false", "public server: needs auth.mode discord, no DNS sink"},
	{"server.hostnames", "", "operator hostnames, added to the certificate names (comma-separated)"},
	{"auth.mode", AuthLabGuest, "lab-guest, discord or dev-fake"},
	{"auth.signup", "", "open, allowlist or closed: who may make a new account (empty = allowlist in discord mode, open otherwise; lab-guest ignores it)"},
	{"auth.allowlist-file", "", "text file of Discord ids allowed to make a new account (auth.signup allowlist): one per line, discord:<id> or <id>, # comments"},
	{"auth.discord.client-id", "", "Discord application client id (auth.mode discord)"},
	{"auth.discord.secret-file", "", "file holding the Discord client secret; mode 0600 off Windows; the secret is never a value"},
	{"auth.discord.redirect", "", "the exact OAuth callback URL registered with Discord (https)"},
	{"server.name", "Cherry", "operator display name in discovery (1 to 64 characters)"},
	{"listen.staff", "", "staff UI listener (HTTPS, own hostname, Discord sign-in); empty turns the staff UI off"},
	{"staff.hostname", "", "staff UI hostname: required with listen.staff when server.public is true; the Host header must match it"},
	{"staff.tls-cert-file", "", "PEM certificate chain for the staff hostname (required when public; reloaded when the file changes); empty = self-signed for local use"},
	{"staff.tls-key-file", "", "PEM private key for staff.tls-cert-file"},
	{"staff.owner", "", "the owner's Discord user id (required with listen.staff in discord mode; dev-<name> in dev-fake)"},
	{"staff.allowlist-file", "", "text file of other staff: Discord ids, one per line, same format as auth.allowlist-file (dev-<name> in dev-fake); re-read on change, fail closed"},
	{"staff.discord.redirect", "", "the exact staff OAuth callback registered with Discord: https://<staff.hostname>[:port]/auth/callback (the client id and secret are auth.discord.*)"},
}

// Load resolves the configuration and checks it. args are the command-line flags, lookup reads the environment
// (os.LookupEnv in production; a set-but-empty variable counts as a value). flag.ErrHelp means -h was asked and the
// table was printed.
func Load(args []string, lookup func(string) (string, bool)) (Config, error) {
	c, err := parse(args, lookup)
	if err != nil {
		return Config{}, err
	}
	return c, c.check()
}

// parse resolves every setting without checking the combination.
func parse(args []string, lookup func(string) (string, bool)) (Config, error) {
	fs := flag.NewFlagSet("cherry", flag.ContinueOnError)
	fs.Usage = func() { usage(fs.Output()) }
	var file string
	fs.StringVar(&file, "config", "", "JSON file with the same keys as the table (env CHERRY_CONFIG)")
	flagVals := map[string]string{}
	for _, s := range settings {
		fs.Func(flagName(s.key), s.help, func(v string) error {
			flagVals[s.key] = v
			return nil
		})
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("config: unexpected argument %q", fs.Arg(0))
	}
	if file == "" {
		file, _ = lookup("CHERRY_CONFIG")
	}

	values := map[string]string{}
	set := map[string]bool{}
	for _, s := range settings {
		values[s.key] = s.def
	}
	if file != "" {
		fileVals, err := readFile(file)
		if err != nil {
			return Config{}, err
		}
		for k, v := range fileVals {
			values[k], set[k] = v, true
		}
	}
	for _, s := range settings {
		if v, ok := lookup(envName(s.key)); ok {
			values[s.key], set[s.key] = v, true
		}
	}
	for k, v := range flagVals {
		values[k], set[k] = v, true
	}

	c := Config{
		HTTPS: values["listen.https"], HTTP: values["listen.http"], Session: values["listen.session"],
		Gateway: values["listen.gateway"], DNS: values["listen.dns"], Admin: values["listen.admin"],
		AuthMode: values["auth.mode"], File: file,
		Signup: values["auth.signup"], AllowlistFile: values["auth.allowlist-file"],
		DiscordClientID: values["auth.discord.client-id"], DiscordSecretFile: values["auth.discord.secret-file"],
		DiscordRedirect: values["auth.discord.redirect"], ServerName: strings.TrimSpace(values["server.name"]),
		Staff: values["listen.staff"], StaffHostname: strings.ToLower(strings.TrimSpace(values["staff.hostname"])),
		StaffCert: values["staff.tls-cert-file"], StaffKey: values["staff.tls-key-file"],
		StaffOwner: strings.TrimSpace(values["staff.owner"]), StaffAllowlistFile: values["staff.allowlist-file"],
		StaffRedirect: strings.TrimSpace(values["staff.discord.redirect"]),
	}
	if c.Signup == "" {
		c.Signup = DefaultSignup(c.AuthMode)
	}
	var err error
	if c.Public, err = strconv.ParseBool(values["server.public"]); err != nil {
		return Config{}, fmt.Errorf("config: server.public: %q is not true or false", values["server.public"])
	}
	if c.Public && !set["listen.dns"] {
		c.DNS = ""
	}
	if c.DataDir, err = dirValue(values["data.dir"], "data.dir"); err != nil {
		return Config{}, err
	}
	if c.CacheDir, err = dirValue(values["cache.dir"], "cache.dir"); err != nil {
		return Config{}, err
	}
	for _, h := range strings.Split(values["server.hostnames"], ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			c.Hostnames = append(c.Hostnames, h)
		}
	}
	return c, nil
}

// check refuses the combinations that must not start, each with one line that names the setting.
func (c Config) check() error {
	switch c.AuthMode {
	case AuthLabGuest, AuthDiscord, AuthDevFake:
	default:
		return fmt.Errorf("config: auth.mode %q is unknown (lab-guest, discord or dev-fake)", c.AuthMode)
	}
	if c.Public {
		if c.AuthMode == AuthLabGuest || c.AuthMode == AuthDevFake {
			return fmt.Errorf("config: server.public=true refuses auth.mode %s (lab only)", c.AuthMode)
		}
		if c.DNS != "" {
			return errors.New("config: server.public=true turns the DNS sink off; set listen.dns to an empty value")
		}
	}
	if n := utf8.RuneCountInString(c.ServerName); n == 0 || n > 64 || strings.IndexFunc(c.ServerName, unicode.IsControl) >= 0 {
		return errors.New("config: server.name must be 1 to 64 printable characters")
	}
	if c.AuthMode == AuthDiscord {
		var missing []string
		if c.DiscordClientID == "" {
			missing = append(missing, "auth.discord.client-id")
		}
		if c.DiscordSecretFile == "" {
			missing = append(missing, "auth.discord.secret-file")
		}
		if c.DiscordRedirect == "" {
			missing = append(missing, "auth.discord.redirect")
		}
		if len(missing) > 0 {
			return fmt.Errorf("config: auth.mode discord needs %s", strings.Join(missing, ", "))
		}
		if err := checkRedirect(c.DiscordRedirect); err != nil {
			return fmt.Errorf("config: auth.discord.redirect: %v", err)
		}
		if err := checkSecretFile(c.DiscordSecretFile); err != nil {
			return fmt.Errorf("config: auth.discord.secret-file: %v", err)
		}
	}
	if err := c.checkStaff(); err != nil {
		return err
	}
	switch c.Signup {
	case SignupOpen, SignupClosed:
	case SignupAllowlist:
		if c.AuthMode != AuthLabGuest && c.AllowlistFile == "" {
			return errors.New("config: auth.signup allowlist needs auth.allowlist-file")
		}
	default:
		return fmt.Errorf("config: auth.signup %q is unknown (open, allowlist or closed)", c.Signup)
	}
	return nil
}

// checkRedirect: the callback is an absolute https URL with no user info or fragment; Discord matches it exactly.
func checkRedirect(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("not a URL")
	case u.Scheme != "https" || u.Host == "":
		return errors.New("must be an absolute https URL")
	case u.User != nil || u.Fragment != "" || u.RawFragment != "":
		return errors.New("must not carry user info or a fragment")
	}
	return nil
}

// checkSecretFile: a regular file; off Windows no group or other access (the file is never read here).
func checkSecretFile(path string) error {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return errors.New("cannot read the file")
	case !info.Mode().IsRegular():
		return errors.New("not a regular file")
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0:
		return errors.New("readable by group or others; chmod 600 it")
	}
	return nil
}

// checkStaff refuses the staff UI settings that cannot start, each with one line that names the setting (PLAN P4). With
// listen.staff empty the staff settings must be empty too: a setting that would be silently ignored looks like a staff UI
// that is there when it is not.
func (c Config) checkStaff() error {
	if c.Staff == "" {
		for _, s := range []struct{ key, value string }{
			{"staff.hostname", c.StaffHostname}, {"staff.tls-cert-file", c.StaffCert}, {"staff.tls-key-file", c.StaffKey},
			{"staff.owner", c.StaffOwner}, {"staff.allowlist-file", c.StaffAllowlistFile}, {"staff.discord.redirect", c.StaffRedirect},
		} {
			if s.value != "" {
				return fmt.Errorf("config: %s needs listen.staff", s.key)
			}
		}
		return nil
	}
	host, port, err := net.SplitHostPort(c.Staff)
	if err != nil {
		return fmt.Errorf("config: listen.staff %q is not host:port", c.Staff)
	}
	if c.AuthMode == AuthLabGuest {
		return errors.New("config: listen.staff refuses auth.mode lab-guest: the staff UI signs staff in with Discord (dev-fake for tests)")
	}
	// The test provider signs anyone in as any dev name, so its staff UI listens on loopback only.
	if c.AuthMode == AuthDevFake && !loopbackHost(host) {
		return errors.New("config: listen.staff with auth.mode dev-fake must bind a loopback address (the test provider signs anyone in)")
	}
	if c.StaffHostname != "" && !validHostname(c.StaffHostname) {
		return fmt.Errorf("config: staff.hostname %q is not a host name", c.StaffHostname)
	}
	if (c.StaffCert == "") != (c.StaffKey == "") {
		return errors.New("config: staff.tls-cert-file and staff.tls-key-file must be set together")
	}
	if c.Public {
		if c.StaffHostname == "" {
			return errors.New("config: server.public=true with listen.staff needs staff.hostname")
		}
		if c.StaffCert == "" {
			return errors.New("config: server.public=true with listen.staff needs staff.tls-cert-file and staff.tls-key-file (a publicly trusted certificate)")
		}
	}
	if c.AuthMode == AuthDiscord {
		if c.StaffOwner == "" {
			return errors.New("config: auth.mode discord with listen.staff needs staff.owner (the owner's Discord user id)")
		}
		if c.StaffRedirect == "" {
			return errors.New("config: auth.mode discord with listen.staff needs staff.discord.redirect")
		}
	}
	if c.StaffOwner != "" && !validStaffOwner(c.StaffOwner, c.AuthMode) {
		return errors.New("config: staff.owner must be a Discord user id (or dev-<name> in dev-fake)")
	}
	if c.StaffRedirect != "" {
		if err := checkStaffRedirect(c.StaffRedirect, c.StaffHostname, port); err != nil {
			return fmt.Errorf("config: staff.discord.redirect: %v", err)
		}
	}
	return nil
}

// checkStaffRedirect: the staff callback is https://<hostname>[:port]/auth/callback with no user info, query or fragment;
// Discord matches it exactly, so the port must be the one listen.staff serves.
func checkStaffRedirect(raw, hostname, listenPort string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("not a URL")
	case u.Scheme != "https" || u.Host == "":
		return errors.New("must be an absolute https URL")
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "":
		return errors.New("must not carry user info, a query or a fragment")
	case u.Path != "/auth/callback":
		return errors.New("must end in /auth/callback")
	case hostname != "" && strings.ToLower(u.Hostname()) != hostname:
		return errors.New("must name staff.hostname")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if port != listenPort {
		return errors.New("must use the port of listen.staff")
	}
	return nil
}

// loopbackHost: localhost or a loopback address (an empty host listens on every interface, so it is not one).
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validHostname: LDH labels of 1 to 63 characters, no leading or trailing hyphen, 253 characters at most (IPv4 literals
// pass too: they are digits and dots).
func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// validStaffOwner: a Discord user id (1 to 20 digits); dev-fake also takes dev-<name>, its test identity.
func validStaffOwner(owner, mode string) bool {
	if name, ok := strings.CutPrefix(owner, "dev-"); ok && mode == AuthDevFake {
		return ValidDevName(name)
	}
	if len(owner) < 1 || len(owner) > 20 {
		return false
	}
	for i := 0; i < len(owner); i++ {
		if owner[i] < '0' || owner[i] > '9' {
			return false
		}
	}
	return true
}

// ValidDevName reports whether n is a dev-fake provider name: 1 to 24 letters, digits, '-' or '_' (the identity is dev-<n>).
func ValidDevName(n string) bool {
	if len(n) < 1 || len(n) > 24 {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// readFile reads a JSON object of the table's keys. Unknown keys are errors, so a typo cannot pass silently. A value is
// a string, a boolean or a list of strings (joined with commas, the flag's form).
func readFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	out := map[string]string{}
	for k, v := range doc {
		if !isKnown(k) {
			return nil, fmt.Errorf("config: %s: unknown key %q", path, k)
		}
		s, err := scalar(v)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %s: %w", path, k, err)
		}
		out[k] = s
	}
	return out, nil
}

func scalar(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", errors.New("want a string, a boolean or a list of strings")
	}
	return strings.Join(list, ","), nil
}

func isKnown(key string) bool {
	for _, s := range settings {
		if s.key == key {
			return true
		}
	}
	return false
}

// dirValue makes a directory absolute; an empty value is the default.
func dirValue(v, key string) (string, error) {
	if v == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("config: %s: no user cache directory (%v); set %s", key, err, envName(key))
		}
		return filepath.Join(base, "Cherry"), nil
	}
	return filepath.Abs(v)
}

// The directories the packages use. Activate sets them once at startup. Until then (tests, and tools that did not call
// it) they are the defaults, resolved on every call so that a test's LocalAppData or XDG_CACHE_HOME takes effect.
var active struct {
	data, cache string
	cfg         Config
}

// Activate makes c the configuration Current returns and its directories the ones DataDir and CacheDir return.
func Activate(c Config) {
	active.data, active.cache, active.cfg = c.DataDir, c.CacheDir, c
}

// Current is the configuration Activate was given. Before that (tests) it is the zero value: AuthMode "" is lab-guest.
func Current() Config { return active.cfg }

// DefaultSignup is the sign-up mode an empty auth.signup means: allowlist for discord (the operator opens it on purpose),
// open otherwise.
func DefaultSignup(mode string) string {
	if mode == AuthDiscord {
		return SignupAllowlist
	}
	return SignupOpen
}

// IsIdentity reports whether mode signs players in with provider identities (discord, dev-fake) rather than the lab's
// device guests (lab-guest). The one place that decides it.
func IsIdentity(mode string) bool { return mode == AuthDiscord || mode == AuthDevFake }

// IdentityMode is IsIdentity for the activated configuration; false before Activate (tests run lab-guest).
func IdentityMode() bool { return IsIdentity(active.cfg.AuthMode) }

// DataDir is the state directory: stores, media, config, secrets and converted items. It must survive a cache wipe.
func DataDir() (string, error) {
	if active.data != "" {
		return active.data, nil
	}
	return defaultDir()
}

// CacheDir is the re-downloadable cache: the R2 read-through copy (static) and the generated thumbnails (dp-cache).
func CacheDir() (string, error) {
	if active.cache != "" {
		return active.cache, nil
	}
	return defaultDir()
}

// defaultDir is the lab's folder: the user cache directory plus Cherry (%LOCALAPPDATA%\Cherry on Windows).
func defaultDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Cherry"), nil
}

func flagName(key string) string { return strings.ReplaceAll(key, ".", "-") }

func envName(key string) string {
	return "CHERRY_" + strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(key))
}

func shown(s setting) string {
	switch {
	case s.def != "":
		return s.def
	case s.key == "data.dir" || s.key == "cache.dir":
		return "user cache dir\\Cherry"
	case s.key == "auth.signup":
		return "mode default"
	}
	return "(none)"
}

// usage prints the table: every flag with its environment name, file key, default and meaning.
func usage(w io.Writer) {
	fmt.Fprint(w, "usage: cherry [flags]\n\n"+
		"Each setting comes from its flag, else its environment variable, else the JSON file, else its default.\n"+
		"An empty value is a value: -listen-dns= turns the DNS sink off. Booleans take =true or =false; lists are\n"+
		"comma-separated. The file is a JSON object with the file keys below as its keys.\n\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FLAG\tENVIRONMENT\tFILE KEY\tDEFAULT\tMEANING")
	fmt.Fprintln(tw, "-config\tCHERRY_CONFIG\t(none)\t(none)\tJSON file with the same keys")
	for _, s := range settings {
		fmt.Fprintf(tw, "-%s\t%s\t%s\t%s\t%s\n", flagName(s.key), envName(s.key), s.key, shown(s), s.help)
	}
	tw.Flush()
}
