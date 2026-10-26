package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cherry/internal/assets"
)

// d1Creds are the Cloudflare account, API token and D1 database from cloudflare.json.
type d1Creds struct {
	accountID, token, database string
}

func loadD1Creds() (d1Creds, error) {
	p, err := credsPath()
	if err != nil {
		return d1Creds{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return d1Creds{}, err
	}
	var f struct {
		AccountID string `json:"account_id"`
		Token     string `json:"api_token"`
		Database  string `json:"d1_database_id"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return d1Creds{}, fmt.Errorf("%s: not valid JSON", p)
	}
	if f.AccountID == "" || f.Token == "" || f.Database == "" {
		return d1Creds{}, errors.New("cloudflare.json needs account_id, api_token and d1_database_id")
	}
	return d1Creds{accountID: f.AccountID, token: f.Token, database: f.Database}, nil
}

// d1API is the Cloudflare API root of the D1 REST calls.
const d1API = "https://api.cloudflare.com/client/v4"

// d1Client runs SQL through the D1 REST API.
type d1Client struct {
	creds d1Creds
	api   string // d1API; a field so a test can point the client at a fake
	hc    *http.Client
}

// query runs one statement and returns its result rows. Errors name the API's messages, never the token.
func (d *d1Client) query(ctx context.Context, sql string) ([]map[string]any, error) {
	body, err := json.Marshal(map[string]any{"sql": sql, "params": []any{}})
	if err != nil {
		return nil, err
	}
	url := d.api + "/accounts/" + d.creds.accountID + "/d1/database/" + d.creds.database + "/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.creds.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result []struct {
			Success bool             `json:"success"`
			Results []map[string]any `json:"results"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("d1: HTTP %d, unreadable reply", resp.StatusCode)
	}
	if !out.Success || len(out.Result) == 0 || !out.Result[0].Success {
		var msgs []string
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("d1: HTTP %d %s", resp.StatusCode, strings.Join(msgs, "; "))
	}
	return out.Result[0].Results, nil
}

// d1Batch is the rows per INSERT. Each statement stays far below D1's statement size limit.
const d1Batch = 100

// The D1 tables. manifest mirrors the ORIGINAL bulk files only (the static/ objects, one row per file of a
// category). generated records the Cherry-generated objects (cherry/ keys) with the recipe that made each one.
// Neither is read by Cherry; R2 stays the source of truth.
const (
	createManifest  = `CREATE TABLE IF NOT EXISTS manifest (cat TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, md5 TEXT NOT NULL, PRIMARY KEY (cat, path))`
	createGenerated = `CREATE TABLE IF NOT EXISTS generated (key TEXT PRIMARY KEY, size INTEGER NOT NULL, md5 TEXT NOT NULL, recipe TEXT NOT NULL, created TEXT NOT NULL)`
)

// d1Init creates both tables when they are missing. Every D1 write starts with it.
func d1Init(ctx context.Context, d *d1Client) error {
	for _, stmt := range []string{createManifest, createGenerated} {
		if _, err := d.query(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// syncGenerated upserts one generated row per Cherry-generated object in objs (key, size, md5, recipe, and the
// time of this run). Originals are not recorded here. With no generated objects it writes nothing.
func syncGenerated(ctx context.Context, d *d1Client, objs []*object, now time.Time, logger *log.Logger) error {
	var gen []*object
	for _, o := range objs {
		if o.generated {
			gen = append(gen, o)
		}
	}
	if len(gen) == 0 {
		return nil
	}
	if err := d1Init(ctx, d); err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339)
	for i := 0; i < len(gen); i += d1Batch {
		end := min(i+d1Batch, len(gen))
		var b strings.Builder
		b.WriteString("INSERT OR REPLACE INTO generated (key, size, md5, recipe, created) VALUES ")
		for j, o := range gen[i:end] {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "(%s, %d, %s, %s, %s)", sqlStr(o.key), o.size, sqlStr(o.md5), sqlStr(o.recipe), sqlStr(stamp))
		}
		if _, err := d.query(ctx, b.String()); err != nil {
			return fmt.Errorf("generated rows %d-%d: %w", i, end, err)
		}
	}
	fmt.Printf("d1 generated: %d rows written\n", len(gen))
	logger.Printf("d1 generated: %d rows", len(gen))
	return nil
}

// syncD1 mirrors the bulk indexes into the D1 table manifest(cat, path, size, md5). A category is
// rewritten only when its row count or size total differs, or when this run PUT one of its objects (or
// force is set), so a re-run writes no rows. R2 stays the source of truth; Cherry does not read D1.
// ponytail: the skip test compares row count and size sum only; -force-d1 rewrites when a same-size edit is suspected.
func syncD1(ctx context.Context, d *d1Client, manifests map[string][]assets.Entry, put map[string]bool, force bool, logger *log.Logger) error {
	if err := d1Init(ctx, d); err != nil {
		return err
	}
	for _, cat := range assets.Categories {
		entries := manifests[cat]
		var sum int64
		for _, e := range entries {
			sum += e.Size
		}
		rows, err := d.query(ctx, "SELECT COUNT(*) AS n, COALESCE(SUM(size), 0) AS s FROM manifest WHERE cat = "+sqlStr(cat))
		if err != nil {
			return err
		}
		var n, s float64
		if len(rows) > 0 {
			n, _ = rows[0]["n"].(float64)
			s, _ = rows[0]["s"].(float64)
		}
		if !force && !put[cat] && int(n) == len(entries) && int64(s) == sum {
			fmt.Printf("d1 %-9s current (%d rows)\n", cat, len(entries))
			logger.Printf("d1 %s current", cat)
			continue
		}
		if _, err := d.query(ctx, "DELETE FROM manifest WHERE cat = "+sqlStr(cat)); err != nil {
			return err
		}
		for i := 0; i < len(entries); i += d1Batch {
			end := min(i+d1Batch, len(entries))
			var b strings.Builder
			b.WriteString("INSERT INTO manifest (cat, path, size, md5) VALUES ")
			for j, e := range entries[i:end] {
				if j > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "(%s, %s, %d, %s)", sqlStr(cat), sqlStr(e.Path), e.Size, sqlStr(e.MD5))
			}
			if _, err := d.query(ctx, b.String()); err != nil {
				return fmt.Errorf("%s rows %d-%d: %w", cat, i, end, err)
			}
		}
		fmt.Printf("d1 %-9s %d rows written\n", cat, len(entries))
		logger.Printf("d1 %s: %d rows", cat, len(entries))
	}
	return nil
}

// sqlStr quotes a SQL string literal.
func sqlStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
