package gaia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// tapResponse writes a TAP JSON response directly as raw bytes, so large
// source_id integers are encoded without float64 precision loss.
func tapResponse(w http.ResponseWriter, columns []string, rows [][]string) {
	var sb strings.Builder
	sb.WriteString(`{"metadata":[`)
	for i, col := range columns {
		if i > 0 {
			sb.WriteByte(',')
		}
		dt := "double"
		if col == "source_id" {
			dt = "long"
		}
		fmt.Fprintf(&sb, `{"name":%s,"datatype":%s}`, jsonStr(col), jsonStr(dt))
	}
	sb.WriteString(`],"data":[`)
	for ri, row := range rows {
		if ri > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('[')
		for ci, val := range row {
			if ci > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(val)
		}
		sb.WriteByte(']')
	}
	sb.WriteString(`]}`)
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, sb.String())
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func newTestClient(srv *httptest.Server) *Client {
	c := NewClient()
	c.Rate = 0
	c.HTTP = srv.Client()
	return c
}

// testQueryTAP calls the TAP endpoint on the given base URL, using UseNumber
// so large source_ids are preserved exactly.
func testQueryTAP(c *Client, ctx context.Context, base, adql string, limit int) ([]map[string]interface{}, error) {
	tapURL := base + "/tap-server/tap/sync"
	q := "REQUEST=doQuery&LANG=ADQL&FORMAT=json&MAXREC=" + strconv.Itoa(limit) + "&QUERY=" + urlEncode(adql)
	body, err := c.Get(ctx, tapURL+"?"+q)
	if err != nil {
		return nil, err
	}
	var resp wireTAPResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	rows := make([]map[string]interface{}, 0, len(resp.Data))
	for _, row := range resp.Data {
		m := make(map[string]interface{}, len(resp.Metadata))
		for i, col := range resp.Metadata {
			if i >= len(row) {
				continue
			}
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

func urlEncode(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			out = append(out, b)
		} else {
			out = append(out, '%', hexchar(b>>4), hexchar(b&0xf))
		}
	}
	return string(out)
}

func hexchar(c byte) byte {
	if c < 10 {
		return '0' + c
	}
	return 'A' + c - 10
}

func TestQueryTAP(t *testing.T) {
	cols := []string{"source_id", "ra", "dec", "parallax"}
	rows := [][]string{
		{"137338313799478912", "45.081118730187555", "35.303675774754296", "0.05336433239868396"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tap-server/tap/sync" {
			http.NotFound(w, r)
			return
		}
		tapResponse(w, cols, rows)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := testQueryTAP(c, context.Background(), srv.URL,
		"SELECT TOP 1 source_id,ra,dec,parallax FROM gaiadr3.gaia_source", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d rows, want 1", len(result))
	}
	if _, ok := result[0]["source_id"]; !ok {
		t.Error("row missing source_id")
	}
	raNum, ok := result[0]["ra"].(json.Number)
	if !ok {
		t.Fatalf("ra type = %T, want json.Number", result[0]["ra"])
	}
	raF, _ := raNum.Float64()
	if raF != 45.081118730187555 {
		t.Errorf("ra = %v, want 45.081118730187555", raF)
	}
}

func TestBrightStars(t *testing.T) {
	cols := []string{"source_id", "ra", "dec", "parallax", "parallax_error",
		"phot_g_mean_mag", "phot_bp_mean_mag", "phot_rp_mean_mag", "pmra", "pmdec"}
	rows := [][]string{
		{"4472832130942575872", "101.2874", "-16.7161", "374.49", "1.04", "0.46", "1.57", "-1.09", "-546.05", "-1223.14"},
		{"5853498713190525696", "217.4290", "-62.6795", "742.12", "1.40", "-0.01", "1.54", "-1.17", "-3781.74", "769.47"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tapResponse(w, cols, rows)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := testQueryTAP(c, context.Background(), srv.URL,
		"SELECT TOP 25 source_id,ra,dec,parallax,parallax_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,pmra,pmdec FROM gaiadr3.gaia_source WHERE phot_g_mean_mag < 8 ORDER BY phot_g_mean_mag ASC",
		25)
	if err != nil {
		t.Fatal(err)
	}
	stars := starsFromRows(result)
	if len(stars) != 2 {
		t.Fatalf("got %d stars, want 2", len(stars))
	}
	if stars[0].MagG != 0.46 {
		t.Errorf("MagG = %v, want 0.46", stars[0].MagG)
	}
	if stars[0].ID == "" {
		t.Error("star ID must not be empty")
	}
	if strings.Contains(stars[0].ID, ".") || strings.Contains(stars[0].ID, "e") {
		t.Errorf("ID = %q looks like a float, want plain integer string", stars[0].ID)
	}
	if stars[0].ID != "4472832130942575872" {
		t.Errorf("star ID = %q, want 4472832130942575872", stars[0].ID)
	}
}

func TestNearbyStars(t *testing.T) {
	cols := []string{"source_id", "ra", "dec", "parallax", "parallax_error",
		"phot_g_mean_mag", "phot_bp_mean_mag", "phot_rp_mean_mag", "pmra", "pmdec", "radial_velocity"}
	rows := [][]string{
		{"137338313799478912", "45.081", "35.303", "2.5", "0.1", "12.3", "13.1", "11.7", "5.2", "-3.1", "22.5"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tapResponse(w, cols, rows)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := testQueryTAP(c, context.Background(), srv.URL,
		"SELECT TOP 25 source_id,ra,dec,parallax,parallax_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,pmra,pmdec,radial_velocity FROM gaiadr3.gaia_source WHERE CONTAINS(POINT('ICRS',ra,dec),CIRCLE('ICRS',45,35,0.5))=1",
		25)
	if err != nil {
		t.Fatal(err)
	}
	stars := starsFromRows(result)
	if len(stars) != 1 {
		t.Fatalf("got %d stars, want 1", len(stars))
	}
	if stars[0].RadialVel != 22.5 {
		t.Errorf("RadialVel = %v, want 22.5", stars[0].RadialVel)
	}
}

func TestNearestStars(t *testing.T) {
	cols := []string{"source_id", "ra", "dec", "parallax", "parallax_error",
		"phot_g_mean_mag", "phot_bp_mean_mag", "phot_rp_mean_mag", "pmra", "pmdec", "radial_velocity"}
	rows := [][]string{
		{"5853498713190525696", "217.4290", "-62.6795", "742.12", "1.40", "-0.01", "1.54", "-1.17", "-3781.74", "769.47", "-21.8"},
		{"4472832130942575872", "101.2874", "-16.7161", "374.49", "1.04", "0.46", "1.57", "-1.09", "-546.05", "-1223.14", "-14.5"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tapResponse(w, cols, rows)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	result, err := testQueryTAP(c, context.Background(), srv.URL,
		"SELECT TOP 25 source_id,ra,dec,parallax,parallax_error,phot_g_mean_mag,phot_bp_mean_mag,phot_rp_mean_mag,pmra,pmdec,radial_velocity FROM gaiadr3.gaia_source WHERE parallax > 100 ORDER BY parallax DESC",
		25)
	if err != nil {
		t.Fatal(err)
	}
	stars := starsFromRows(result)
	if len(stars) != 2 {
		t.Fatalf("got %d stars, want 2", len(stars))
	}
	if stars[0].Parallax != 742.12 {
		t.Errorf("stars[0].Parallax = %v, want 742.12", stars[0].Parallax)
	}
	if stars[1].Parallax != 374.49 {
		t.Errorf("stars[1].Parallax = %v, want 374.49", stars[1].Parallax)
	}
	if stars[0].ID != "5853498713190525696" {
		t.Errorf("stars[0].ID = %q, want 5853498713190525696", stars[0].ID)
	}
}

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestGetRetriesOn503(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Retries = 5

	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "recovered" {
		t.Errorf("body = %q after retries", body)
	}
	if hits != 3 {
		t.Errorf("server saw %d hits, want 3", hits)
	}
}
