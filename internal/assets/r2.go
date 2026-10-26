// Package assets streams Cherry's game assets from a Cloudflare R2 bucket (S3 API, SigV4, region
// "auto"). The bulk categories (animation, sound, ui, subui, tx) are listed by per-category manifests
// (manifest/<cat>.json) and the per-item files (item/<kind>/<id>/<file>) are fetched on demand. Local
// copies live in %LOCALAPPDATA%\Cherry\static (see Store).
//
// Streaming is on only when streaming.json enables it AND %LOCALAPPDATA%\Cherry\cloudflare.json exists
// (Open). The checkresource2 versions then come from the manifests, so the patcher fetches every category
// list again and downloads each entry whose path + MD5 is not in its saved list. The lab devices still hold
// the production lists of their backup (Cherry's old static versions matched them, so its "[]" lists were
// never fetched), so they download only the differences (24 MB on 2026-10-26); fresh devices get it all.
package assets

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// ErrNotFound reports an R2 404.
var ErrNotFound = errors.New("assets: object not found")

// requestTimeout bounds how long R2 may take to send response headers, and how long a body
// download may go without a byte before it is abandoned. A large file may take longer overall.
// A variable so tests can shorten it.
var requestTimeout = 20 * time.Second

// emptySHA256 is the payload hash of an empty body (SigV4 "x-amz-content-sha256").
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Config is the R2 access read from cloudflare.json. The keys are secrets: never log them.
type Config struct {
	Endpoint  string // https://<account>.r2.cloudflarestorage.com
	Bucket    string
	AccessKey string
	SecretKey string
}

// LoadConfig reads the r2_* fields of a cloudflare.json file; the other fields are ignored.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var f struct {
		Endpoint  string `json:"r2_endpoint"`
		Bucket    string `json:"r2_bucket"`
		AccessKey string `json:"r2_access_key_id"`
		SecretKey string `json:"r2_secret_access_key"`
	}
	// The decoder's message can quote the bad byte, so it is not included.
	if err := json.Unmarshal(b, &f); err != nil {
		return Config{}, fmt.Errorf("%s: not valid JSON", path)
	}
	cfg := Config{Endpoint: strings.TrimRight(f.Endpoint, "/"), Bucket: f.Bucket, AccessKey: f.AccessKey, SecretKey: f.SecretKey}
	if u, err := url.Parse(cfg.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
		return Config{}, fmt.Errorf("%s: r2_endpoint must be an https URL", path)
	}
	if cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return Config{}, fmt.Errorf("%s: r2_bucket and the R2 keys are required", path)
	}
	return cfg, nil
}

// Client is the R2 S3 API subset Cherry needs: HEAD, GET, PUT and ListObjectsV2.
type Client struct {
	cfg Config
	hc  *http.Client
	now func() time.Time
}

// NewClient returns a client for cfg with the request timeouts described at requestTimeout.
func NewClient(cfg Config) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = requestTimeout
	return &Client{cfg: cfg, hc: &http.Client{Transport: tr}, now: time.Now}
}

// Object is one stored object. ETag is the quoted MD5 hex for single-part uploads.
type Object struct {
	Key  string
	Size int64
	ETag string
}

// Head returns the size and ETag of key; ErrNotFound when it is absent.
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	resp, err := c.do(ctx, http.MethodHead, c.objectPath(key), "", nil, nil)
	if err != nil {
		return Object{}, err
	}
	defer resp.Body.Close()
	if err := statusErr(resp); err != nil {
		return Object{}, err
	}
	return Object{Key: key, Size: resp.ContentLength, ETag: resp.Header.Get("ETag")}, nil
}

// Get fetches key. The caller closes the body. A 404 is ErrNotFound. The body is abandoned when no
// byte arrives for requestTimeout.
func (c *Client) Get(ctx context.Context, key string) (*http.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	resp, err := c.do(ctx, http.MethodGet, c.objectPath(key), "", nil, nil)
	if err == nil {
		err = statusErr(resp)
		if err != nil {
			resp.Body.Close()
		}
	}
	if err != nil {
		cancel()
		return nil, err
	}
	idle := time.AfterFunc(requestTimeout, cancel)
	resp.Body = &idleBody{ReadCloser: resp.Body, idle: idle, cancel: cancel}
	return resp, nil
}

// Put stores body under key in one request. R2 checks the Content-MD5 header.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) error {
	sum := md5.Sum(body)
	hdr := http.Header{}
	hdr.Set("Content-Md5", base64.StdEncoding.EncodeToString(sum[:]))
	hdr.Set("Content-Type", contentType)
	resp, err := c.do(ctx, http.MethodPut, c.objectPath(key), "", hdr, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return statusErr(resp)
}

// ListPage returns one ListObjectsV2 page of keys under prefix and the token for the next page
// ("" after the last page).
func (c *Client) ListPage(ctx context.Context, prefix, token string) ([]Object, string, error) {
	q := url.Values{"list-type": {"2"}, "max-keys": {"1000"}, "prefix": {prefix}}
	if token != "" {
		q.Set("continuation-token", token)
	}
	resp, err := c.do(ctx, http.MethodGet, "/"+c.cfg.Bucket+"/", canonicalQuery(q), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if err := statusErr(resp); err != nil {
		return nil, "", err
	}
	var out struct {
		Truncated bool   `xml:"IsTruncated"`
		Next      string `xml:"NextContinuationToken"`
		Contents  []struct {
			Key  string `xml:"Key"`
			Size int64  `xml:"Size"`
			ETag string `xml:"ETag"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("list %s: %w", prefix, err)
	}
	objs := make([]Object, 0, len(out.Contents))
	for _, o := range out.Contents {
		objs = append(objs, Object{Key: o.Key, Size: o.Size, ETag: o.ETag})
	}
	if !out.Truncated {
		return objs, "", nil
	}
	return objs, out.Next, nil
}

func (c *Client) objectPath(key string) string {
	return "/" + c.cfg.Bucket + "/" + uriEncode(key, false)
}

// do sends one signed request. canonPath and query are already URI-encoded (the signature covers
// exactly what goes on the wire).
func (c *Client) do(ctx context.Context, method, canonPath, query string, hdr http.Header, body []byte) (*http.Response, error) {
	payload := emptySHA256
	var rdr io.Reader
	if body != nil {
		sum := sha256.Sum256(body)
		payload = hex.EncodeToString(sum[:])
		rdr = bytes.NewReader(body)
	}
	u, err := url.Parse(c.cfg.Endpoint + canonPath)
	if err != nil {
		return nil, err
	}
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	amzDate := c.now().UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	req.Header.Set("Authorization", signV4(method, canonPath, query, req.URL.Host, req.Header, payload, amzDate, "auto", c.cfg.AccessKey, c.cfg.SecretKey))
	return c.hc.Do(req)
}

// idleBody cancels its request when the body stops producing bytes for requestTimeout.
type idleBody struct {
	io.ReadCloser
	idle   *time.Timer
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.idle.Reset(requestTimeout)
	return n, err
}

func (b *idleBody) Close() error {
	b.idle.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}

func statusErr(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode/100 == 2:
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("r2 %s: HTTP %d %s", resp.Request.Method, resp.StatusCode, bytes.TrimSpace(snippet))
}

// signV4 returns the Authorization header value (AWS Signature Version 4) for an S3 request.
// headers are every header to sign besides host; their names are matched case-insensitively.
func signV4(method, canonPath, canonQuery, host string, headers http.Header, payloadHash, amzDate, region, accessKey, secretKey string) string {
	vals := map[string]string{"host": host}
	for k, v := range headers {
		vals[strings.ToLower(k)] = strings.TrimSpace(strings.Join(v, ","))
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, n := range names {
		canon.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	creq := strings.Join([]string{method, canonPath, canonQuery, canon.String(), signed, payloadHash}, "\n")
	date := amzDate[:8]
	scope := date + "/" + region + "/s3/aws4_request"
	creqSum := sha256.Sum256([]byte(creq))
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(creqSum[:])
	k := hmacSHA256([]byte("AWS4"+secretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	return "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// canonicalQuery encodes sorted query parameters the way SigV4 requires.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes every byte outside the RFC 3986 unreserved set. A key keeps its "/"
// separators unless slash is set.
func uriEncode(s string, slash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !slash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
