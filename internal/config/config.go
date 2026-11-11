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

// Config is the effective configuration. A listen address is a net.Listen address; DNS "" means the sink is off.
type Config struct {
	HTTPS, HTTP, Session, Gateway, DNS, Admin string
	DataDir, CacheDir                         string
	Public                                    bool
	Hostnames                                 []string
	AuthMode                                  string
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
	{"auth.discord.client-id", "", "Discord application client id (auth.mode discord)"},
	{"auth.discord.secret-file", "", "file holding the Discord client secret; mode 0600 off Windows; the secret is never a value"},
	{"auth.discord.redirect", "", "the exact OAuth callback URL registered with Discord (https)"},
	{"server.name", "Cherry", "operator display name in discovery (1 to 64 characters)"},
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
		DiscordClientID: values["auth.discord.client-id"], DiscordSecretFile: values["auth.discord.secret-file"],
		DiscordRedirect: values["auth.discord.redirect"], ServerName: strings.TrimSpace(values["server.name"]),
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
