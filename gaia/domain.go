package gaia

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes gaia as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/gaia-cli/gaia"
//
// exactly as a database/sql program enables a driver with `import _
// "github.com/lib/pq"`. The init below registers it; the host then dereferences
// gaia:// URIs by routing to the operations Register installs. The same
// Domain also builds the standalone gaia binary (see cli.NewApp), so the
// binary and a host share one source of truth.
func init() { kit.Register(Domain{}) }

// Domain is the gaia driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against, and
// the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "gaia",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "gaia",
			Short:  "Query the ESA Gaia DR3 stellar catalog (1.8 billion stars).",
			Long: `Query the ESA Gaia DR3 stellar catalog from the command line.

gaia talks to the ESA TAP service at gea.esac.esa.int over plain HTTPS,
shapes the ADQL/TAP response into clean records, and prints output that
pipes into the rest of your tools. No API key required.`,
			Site: Host,
			Repo: "https://github.com/tamnd/gaia-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	// bright — brightest stars by G-band magnitude.
	kit.Handle(app, kit.OpMeta{
		Name:    "bright",
		Group:   "read",
		List:    true,
		Summary: "List the brightest stars by G-band magnitude",
	}, brightCmd)

	// nearby — stars near a given sky coordinate.
	kit.Handle(app, kit.OpMeta{
		Name:    "nearby",
		Group:   "read",
		List:    true,
		Summary: "List stars near a sky coordinate (RA DEC)",
		Args: []kit.Arg{
			{Name: "ra", Help: "right ascension in degrees (ICRS)"},
			{Name: "dec", Help: "declination in degrees (ICRS)"},
		},
	}, nearbyCmd)

	// nearest — stars closest to Earth by parallax.
	kit.Handle(app, kit.OpMeta{
		Name:    "nearest",
		Group:   "read",
		List:    true,
		Summary: "List the nearest stars (largest parallax)",
	}, nearestCmd)

	// query — raw ADQL passthrough.
	kit.Handle(app, kit.OpMeta{
		Name:    "query",
		Group:   "read",
		List:    true,
		Summary: "Run a raw ADQL query against gaiadr3.gaia_source",
		Args:    []kit.Arg{{Name: "adql", Help: "ADQL query string"}},
	}, queryCmd)
}

// newClient builds the client from the host-resolved config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := NewClient()
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.HTTP.Timeout = cfg.Timeout
	}
	return c, nil
}

// --- inputs ---

type brightInput struct {
	MaxMag float64 `kit:"flag" help:"maximum G-band magnitude (default 8)"`
	Limit  int     `kit:"flag,inherit" help:"max results"`
	Client *Client `kit:"inject"`
}

type nearbyInput struct {
	RA     string  `kit:"arg"  help:"right ascension in degrees (ICRS)"`
	Dec    string  `kit:"arg"  help:"declination in degrees (ICRS)"`
	Radius float64 `kit:"flag" help:"search radius in degrees (default 0.5)"`
	Limit  int     `kit:"flag,inherit" help:"max results"`
	Client *Client `kit:"inject"`
}

type nearestInput struct {
	Limit  int     `kit:"flag,inherit" help:"max results"`
	Client *Client `kit:"inject"`
}

type queryInput struct {
	ADQL   string  `kit:"arg"          help:"ADQL query string"`
	Limit  int     `kit:"flag,inherit" help:"max results"`
	Client *Client `kit:"inject"`
}

// --- handlers ---

func brightCmd(ctx context.Context, in brightInput, emit func(*Star) error) error {
	maxMag := in.MaxMag
	if maxMag == 0 {
		maxMag = 8
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	stars, err := in.Client.BrightStars(ctx, maxMag, limit)
	if err != nil {
		return err
	}
	for _, s := range stars {
		if err := emit(s); err != nil {
			return err
		}
	}
	return nil
}

func nearbyCmd(ctx context.Context, in nearbyInput, emit func(*Star) error) error {
	ra, err := strconv.ParseFloat(in.RA, 64)
	if err != nil {
		return errs.Usage("ra must be a number in degrees, got %q", in.RA)
	}
	dec, err := strconv.ParseFloat(in.Dec, 64)
	if err != nil {
		return errs.Usage("dec must be a number in degrees, got %q", in.Dec)
	}
	radius := in.Radius
	if radius <= 0 {
		radius = 0.5
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	stars, err := in.Client.NearbyStars(ctx, ra, dec, radius, limit)
	if err != nil {
		return err
	}
	for _, s := range stars {
		if err := emit(s); err != nil {
			return err
		}
	}
	return nil
}

func nearestCmd(ctx context.Context, in nearestInput, emit func(*Star) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	stars, err := in.Client.NearestStars(ctx, limit)
	if err != nil {
		return err
	}
	for _, s := range stars {
		if err := emit(s); err != nil {
			return err
		}
	}
	return nil
}

// rawRow is emitted by the query command so callers get plain maps.
type rawRow struct {
	ID   string                 `json:"id"   kit:"id"`
	Data map[string]interface{} `json:"data"`
}

func queryCmd(ctx context.Context, in queryInput, emit func(*rawRow) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	rows, err := in.Client.QueryTAP(ctx, in.ADQL, limit)
	if err != nil {
		return err
	}
	for i, row := range rows {
		rr := &rawRow{
			ID:   fmt.Sprintf("%d", i),
			Data: row,
		}
		// Use source_id as ID when present.
		if sid, ok := row["source_id"]; ok && sid != nil {
			switch x := sid.(type) {
			case float64:
				rr.ID = strconv.FormatInt(int64(x), 10)
			case string:
				rr.ID = x
			}
		}
		if err := emit(rr); err != nil {
			return err
		}
	}
	return nil
}

// --- Resolver: URI-native string functions, pure and network-free ---

// Classify turns any accepted input into the canonical (type, id).
// For Gaia, any non-empty string is treated as a star source_id.
func (Domain) Classify(input string) (uriType, id string, err error) {
	if input == "" {
		return "", "", errs.Usage("empty gaia reference")
	}
	return "star", input, nil
}

// Locate returns the TAP query URL for a star by source_id.
func (Domain) Locate(uriType, id string) (string, error) {
	if uriType != "star" {
		return "", errs.Usage("gaia has no resource type %q", uriType)
	}
	q := fmt.Sprintf("SELECT * FROM gaiadr3.gaia_source WHERE source_id=%s", id)
	return tapEndpoint + "?REQUEST=doQuery&LANG=ADQL&FORMAT=json&QUERY=" + q, nil
}
