package assets

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The SigV4 example from the AWS S3 documentation ("GET Object" with a Range header).
func TestSignV4AWSExample(t *testing.T) {
	headers := http.Header{}
	headers.Set("Range", "bytes=0-9")
	headers.Set("X-Amz-Content-Sha256", emptySHA256)
	headers.Set("X-Amz-Date", "20130524T000000Z")
	got := signV4("GET", "/test.txt", "", "examplebucket.s3.amazonaws.com", headers, emptySHA256,
		"20130524T000000Z", "us-east-1", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got != want {
		t.Fatalf("signature\n got %s\nwant %s", got, want)
	}
}

func TestUriEncode(t *testing.T) {
	if got := uriEncode("ani emo/a+b~c.png", false); got != "ani%20emo/a%2Bb~c.png" {
		t.Fatalf("key encoding = %q", got)
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Fatalf("query encoding = %q", got)
	}
}

func TestClientRoundTrip(t *testing.T) {
	f := newFakeR2(t)
	c := NewClient(f.cfg())
	ctx := context.Background()
	if err := c.Put(ctx, "static/animation/ani emo/a.png", []byte("pixels"), "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	obj, err := c.Head(ctx, "static/animation/ani emo/a.png")
	sum := md5.Sum([]byte("pixels"))
	if err != nil || obj.Size != 6 || obj.ETag != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Fatalf("head = %+v, %v", obj, err)
	}
	resp, err := c.Get(ctx, "static/animation/ani emo/a.png")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "pixels" {
		t.Fatalf("get body = %q", body)
	}
	if _, err := c.Head(ctx, "static/missing.png"); err != ErrNotFound {
		t.Fatalf("head missing: %v", err)
	}
	if _, err := c.Get(ctx, "static/missing.png"); err != ErrNotFound {
		t.Fatalf("get missing: %v", err)
	}
}

func TestListPageContinuation(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>true</IsTruncated>`+
			`<NextContinuationToken>tok2</NextContinuationToken>`+
			`<Contents><Key>static/a.png</Key><Size>3</Size><ETag>"abc"</ETag></Contents></ListBucketResult>`)
	}))
	defer srv.Close()
	c := NewClient(Config{Endpoint: srv.URL, Bucket: "cherry-assets", AccessKey: "AKTEST", SecretKey: "S"})
	objs, next, err := c.ListPage(context.Background(), "static/", "tok1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if next != "tok2" || len(objs) != 1 || objs[0].Key != "static/a.png" || objs[0].Size != 3 {
		t.Fatalf("page = %+v next=%q", objs, next)
	}
	if query != "continuation-token=tok1&list-type=2&max-keys=1000&prefix=static%2F" {
		t.Fatalf("query = %q", query)
	}
}

func TestGetAbandonsStalledBody(t *testing.T) {
	old := requestTimeout
	requestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })
	f := newFakeR2(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.mu.Lock()
	f.dribble["static/animation/big.png"] = release
	f.mu.Unlock()
	f.put("static/animation/big.png", []byte("0123456789"))
	resp, err := NewClient(f.cfg()).Get(context.Background(), "static/animation/big.png")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	start := time.Now()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("stalled body read returned no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("stalled body held the read for %v", d)
	}
}
