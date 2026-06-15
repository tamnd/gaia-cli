// Package gaia is the library behind the gaia command line:
// the HTTP client, request shaping, and the typed data models for ESA Gaia DR3.
//
// The Client here is the spine every command shares. It sets a real
// User-Agent, paces requests so a busy session stays polite, and retries the
// transient failures (429 and 5xx) that any public service throws under load.
package gaia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent identifies the client to the ESA TAP service.
const DefaultUserAgent = "gaia-cli/dev (+https://github.com/tamnd/gaia-cli)"

// Host is the ESA Gaia TAP server hostname.
const Host = "gea.esac.esa.int"

// BaseURL is the root every request is built from.
const BaseURL = "https://" + Host

// tapEndpoint is the TAP sync endpoint.
const tapEndpoint = BaseURL + "/tap-server/tap/sync"

// Client talks to the ESA Gaia TAP service over HTTP.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// Rate is the minimum gap between requests. Zero means no pacing.
	Rate    time.Duration
	Retries int

	last time.Time
}

// NewClient returns a Client with sensible defaults: 60s timeout (TAP can be slow),
// 500ms minimum gap between requests, and three retries on transient errors.
func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		UserAgent: DefaultUserAgent,
		Rate:      500 * time.Millisecond,
		Retries:   3,
	}
}

// Star is a single record from gaiadr3.gaia_source.
type Star struct {
	ID           string  `json:"id"                       kit:"id"`
	RA           float64 `json:"ra"`
	Dec          float64 `json:"dec"`
	Parallax     float64 `json:"parallax_mas,omitempty"`
	ParallaxErr  float64 `json:"parallax_error,omitempty"`
	MagG         float64 `json:"mag_g,omitempty"`
	MagBP        float64 `json:"mag_bp,omitempty"`
	MagRP        float64 `json:"mag_rp,omitempty"`
	ProperMotRA  float64 `json:"pmra,omitempty"`
	ProperMotDec float64 `json:"pmdec,omitempty"`
	RadialVel    float64 `json:"radial_velocity_km_s,omitempty"`
}

// wireTAPResponse is the raw TAP JSON envelope.
type wireTAPResponse struct {
	Metadata []struct {
		Name     string `json:"name"`
		DataType string `json:"datatype"`
	} `json:"metadata"`
	Data [][]json.RawMessage `json:"data"`
}

// QueryTAP executes an ADQL query against the ESA Gaia TAP service and returns
// the rows as a slice of maps keyed by column name.
func (c *Client) QueryTAP(ctx context.Context, adql string, limit int) ([]map[string]interface{}, error) {
	q := url.Values{}
	q.Set("REQUEST", "doQuery")
	q.Set("LANG", "ADQL")
	q.Set("FORMAT", "json")
	q.Set("MAXREC", strconv.Itoa(limit))
	q.Set("QUERY", adql)
	endpoint := tapEndpoint + "?" + q.Encode()

	body, err := c.Get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var resp wireTAPResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("tap parse: %w", err)
	}

	rows := make([]map[string]interface{}, 0, len(resp.Data))
	for _, row := range resp.Data {
		m := make(map[string]interface{}, len(resp.Metadata))
		for i, col := range resp.Metadata {
			if i >= len(row) {
				continue
			}
			// Use a decoder with UseNumber so large int64 source_ids are not
			// silently rounded when converted to float64.
			dec := json.NewDecoder(strings.NewReader(string(row[i])))
			dec.UseNumber()
			var v interface{}
			if err := dec.Decode(&v); err == nil {
				m[col.Name] = v
			}
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// BrightStars returns stars brighter than maxMag in the G band, sorted by
// ascending magnitude (brightest first).
func (c *Client) BrightStars(ctx context.Context, maxMag float64, limit int) ([]*Star, error) {
	adql := fmt.Sprintf(
		"SELECT TOP %d source_id,ra,dec,parallax,parallax_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,pmra,pmdec "+
			"FROM gaiadr3.gaia_source WHERE phot_g_mean_mag < %g ORDER BY phot_g_mean_mag ASC",
		limit, maxMag,
	)
	rows, err := c.QueryTAP(ctx, adql, limit)
	if err != nil {
		return nil, err
	}
	return starsFromRows(rows), nil
}

// NearbyStars returns stars within radiusDeg degrees of the given ICRS coordinates.
func (c *Client) NearbyStars(ctx context.Context, ra, dec, radiusDeg float64, limit int) ([]*Star, error) {
	adql := fmt.Sprintf(
		"SELECT TOP %d source_id,ra,dec,parallax,parallax_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,pmra,pmdec,radial_velocity "+
			"FROM gaiadr3.gaia_source WHERE CONTAINS(POINT('ICRS',ra,dec),CIRCLE('ICRS',%g,%g,%g))=1",
		limit, ra, dec, radiusDeg,
	)
	rows, err := c.QueryTAP(ctx, adql, limit)
	if err != nil {
		return nil, err
	}
	return starsFromRows(rows), nil
}

// NearestStars returns the stars with the largest parallax (closest to Earth),
// filtered to parallax > 100 mas (within ~10 parsecs).
func (c *Client) NearestStars(ctx context.Context, limit int) ([]*Star, error) {
	adql := fmt.Sprintf(
		"SELECT TOP %d source_id,ra,dec,parallax,parallax_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,pmra,pmdec,radial_velocity "+
			"FROM gaiadr3.gaia_source WHERE parallax > 100 ORDER BY parallax DESC",
		limit,
	)
	rows, err := c.QueryTAP(ctx, adql, limit)
	if err != nil {
		return nil, err
	}
	return starsFromRows(rows), nil
}

// starsFromRows converts TAP row maps into Star records.
func starsFromRows(rows []map[string]interface{}) []*Star {
	out := make([]*Star, 0, len(rows))
	for _, row := range rows {
		out = append(out, parseStar(row))
	}
	return out
}

// parseStar extracts Star fields from a TAP row map.
func parseStar(row map[string]interface{}) *Star {
	s := &Star{}
	// source_id is a large int64. JSON decodes it as json.Number (when using
	// Decoder with UseNumber) or as a string if we stored it that way.
	if v, ok := row["source_id"]; ok {
		switch x := v.(type) {
		case json.Number:
			// Preserve exact integer value from the JSON number string.
			s.ID = x.String()
		case float64:
			// Fall back: round-trip through the json.Number string to avoid
			// float64 precision loss on large source_ids.
			s.ID = strconv.FormatInt(int64(x), 10)
		case string:
			s.ID = x
		default:
			s.ID = fmt.Sprintf("%v", v)
		}
	}
	s.RA = rowFloat(row, "ra")
	s.Dec = rowFloat(row, "dec")
	s.Parallax = rowFloat(row, "parallax")
	s.ParallaxErr = rowFloat(row, "parallax_error")
	s.MagG = rowFloat(row, "phot_g_mean_mag")
	s.MagBP = rowFloat(row, "phot_bp_mean_mag")
	s.MagRP = rowFloat(row, "phot_rp_mean_mag")
	s.ProperMotRA = rowFloat(row, "pmra")
	s.ProperMotDec = rowFloat(row, "pmdec")
	s.RadialVel = rowFloat(row, "radial_velocity")
	return s
}

// rowFloat extracts a float64 from a row map entry, handling json.Number,
// float64, and string representations.
func rowFloat(row map[string]interface{}, key string) float64 {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	default:
		f, _ := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
		return f
	}
}

// Get fetches a URL and returns the response body. It paces and retries
// according to the client's settings.
func (c *Client) Get(ctx context.Context, reqURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, reqURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", reqURL, lastErr)
}

func (c *Client) do(ctx context.Context, reqURL string) (body []byte, retry bool, err error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// pace blocks until at least Rate has passed since the previous request.
func (c *Client) pace() {
	if c.Rate <= 0 {
		return
	}
	if wait := c.Rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
