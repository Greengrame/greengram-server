package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/settings"
)

func (a *API) getSettings(q *req) (any, error) { return a.Settings.All(q.ctx) }

func (a *API) saveSettings(q *req) (any, error) {
	patch, err := settings.Check(q.body)
	if err != nil {
		return nil, err
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if err := a.Settings.Save(q.ctx, tx, patch); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "update_settings", q.me.ID, patch)
	})
	if err != nil {
		return nil, err
	}
	a.Settings.Forget()
	return a.Settings.All(q.ctx)
}

type auditEntry struct {
	ID        int64  `json:"id"`
	ActorID   int64  `json:"actor_id"`
	Action    string `json:"action"`
	TargetID  int64  `json:"target_id"`
	Details   string `json:"details"`
	CreatedAt int64  `json:"created_at"`
	Actor     string `json:"actor"`

	parsed map[string]any
}

// auditLog is the newest entries, optionally of one action. details is a
// JSON string like the web's TEXT column; the panel parses it.
func (a *API) auditLog(ctx context.Context, action string, limit int) ([]auditEntry, error) {
	rows, err := a.DB.Query(ctx, `SELECT a.id, a.actor_id, a.action, a.target_id, a.details::text, extract(epoch FROM a.created_at)::bigint, coalesce(u.name, '')
		FROM audit_log a LEFT JOIN users u ON u.id=a.actor_id WHERE $1 = '' OR a.action = $1 ORDER BY a.id DESC LIMIT $2`, action, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auditEntry, error) {
		var e auditEntry
		err := r.Scan(&e.ID, &e.ActorID, &e.Action, &e.TargetID, &e.Details, &e.CreatedAt, &e.Actor)
		json.Unmarshal([]byte(e.Details), &e.parsed)
		return e, err
	})
}

//go:embed countries.json
var baseCountries []byte

type country struct {
	ID          int64  `json:"id"`
	ISO2        string `json:"iso2"`
	CountryCode string `json:"country_code"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
}

func (a *API) customCountries(ctx context.Context) ([]country, error) {
	rows, err := a.DB.Query(ctx, "SELECT id, iso2, country_code, name, enabled FROM instance_countries ORDER BY name, country_code")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (country, error) {
		var c country
		err := r.Scan(&c.ID, &c.ISO2, &c.CountryCode, &c.Name, &c.Enabled)
		return c, err
	})
}

var (
	iso2Re   = regexp.MustCompile(`^(?:[A-Z]{2})?$`)
	prefixRe = regexp.MustCompile(`^[1-9][0-9]{0,126}$`)
)

func (a *API) saveCountry(q *req) (any, error) {
	var id int64
	if q.has("id") && q.body["id"] != nil {
		var ok bool
		if id, ok = q.int("id"); !ok || id < 1 {
			return nil, fail(400, "Choose a valid country entry.")
		}
	}
	iso2, _ := q.str("iso2")
	iso2 = strings.ToUpper(iso2)
	code, _ := q.str("countryCode")
	code = strings.TrimPrefix(code, "+")
	name, _ := q.str("name")
	name = strings.TrimSpace(name)
	enabled, enabledOK := q.boolean("enabled")
	if !iso2Re.MatchString(iso2) || !prefixRe.MatchString(code) || name == "" || utf8.RuneCountInString(name) > 80 || !enabledOK {
		return nil, fail(400, "Enter a name, a valid country code, an optional two-letter flag code, and an enabled state.")
	}
	err := a.tx(q.ctx, func(tx pgx.Tx) error {
		if id != 0 {
			var oldISO, oldCode string
			err := tx.QueryRow(q.ctx, "SELECT iso2, country_code FROM instance_countries WHERE id=$1", id).Scan(&oldISO, &oldCode)
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(404, "Country entry not found.")
			}
			if err != nil {
				return err
			}
			if oldISO != iso2 || oldCode != code {
				return fail(400, "Create a new entry to change the country identifier.")
			}
		}
		var saved int64
		if err := tx.QueryRow(q.ctx, `INSERT INTO instance_countries(iso2, country_code, name, enabled) VALUES ($1, $2, $3, $4)
			ON CONFLICT (iso2, country_code) DO UPDATE SET name=excluded.name, enabled=excluded.enabled RETURNING id`,
			iso2, code, name, enabled).Scan(&saved); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "update_country", saved, map[string]any{"iso2": iso2, "countryCode": code, "enabled": enabled})
	})
	if err != nil {
		return nil, err
	}
	return ok(), nil
}

func (a *API) deleteCountry(q *req) (any, error) {
	id, okID := q.int("id")
	if !okID || id < 1 {
		return nil, fail(400, "Choose a custom country entry.")
	}
	err := a.tx(q.ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(q.ctx, "DELETE FROM instance_countries WHERE id=$1", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail(404, "Country entry not found.")
		}
		return audit(q.ctx, tx, q.me.ID, "delete_country", id, map[string]any{})
	})
	if err != nil {
		return nil, err
	}
	return ok(), nil
}

type phoneCode struct {
	ISO2        string   `json:"iso2"`
	DefaultName string   `json:"defaultName"`
	Name        string   `json:"name"`
	CountryCode string   `json:"countryCode"`
	Patterns    []string `json:"patterns"`
}

// Countries is the phone picker list: the built-in countries with the
// instance's overrides applied. Patterns are all X's: local numbers can be
// any length, the server checks them.
func (a *API) Countries(ctx context.Context) ([]phoneCode, error) {
	var doc struct {
		PhoneCodes []phoneCode `json:"phoneCodes"`
	}
	if err := json.Unmarshal(baseCountries, &doc); err != nil {
		return nil, err
	}
	custom, err := a.customCountries(ctx)
	if err != nil {
		return nil, err
	}
	list := doc.PhoneCodes
	for _, c := range custom {
		kept := list[:0]
		for _, e := range list {
			if e.ISO2 != c.ISO2 || e.CountryCode != c.CountryCode {
				kept = append(kept, e)
			}
		}
		list = kept
		if c.Enabled {
			list = append(list, phoneCode{ISO2: c.ISO2, DefaultName: c.Name, Name: c.Name, CountryCode: c.CountryCode})
		}
	}
	for i := range list {
		list[i].Patterns = []string{strings.Repeat("X", max(1, 128-len(list[i].CountryCode)))}
	}
	return list, nil
}

func (a *API) countries(q *req) (any, error) {
	var doc map[string]any
	if err := json.Unmarshal(baseCountries, &doc); err != nil {
		return nil, err
	}
	list, err := a.Countries(q.ctx)
	if err != nil {
		return nil, err
	}
	doc["phoneCodes"] = list
	return doc, nil
}

// The default instance logo, until an admin uploads one.
//
//go:embed logo.png
var defaultLogo []byte

func (a *API) logoBytes(ctx context.Context) ([]byte, string, bool, error) {
	var b []byte
	var mime string
	err := a.DB.QueryRow(ctx, "SELECT bytes, mime FROM instance_branding WHERE id=1").Scan(&b, &mime)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultLogo, "image/png", false, nil
	}
	return b, mime, err == nil, err
}

func (a *API) branding(q *req) (any, error) {
	b, _, custom, err := a.logoBytes(q.ctx)
	if err != nil {
		return nil, err
	}
	set, err := a.Settings.All(q.ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return map[string]any{"name": set["siteName"], "logoUrl": "/branding/logo?v=" + hex.EncodeToString(sum[:8]), "hasCustomLogo": custom}, nil
}

func (a *API) logo(w http.ResponseWriter, r *http.Request) {
	b, mime, _, err := a.logoBytes(r.Context())
	if err != nil {
		http.Error(w, "logo unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Write(b)
}

const maxLogo = 5 << 20

func (a *API) uploadLogo(q *req) (any, error) {
	if o := q.r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != q.r.Host {
			return nil, fail(403, "Cross-origin uploads are not allowed.")
		}
	}
	if q.r.ContentLength > maxLogo {
		return nil, fail(413, "Instance logos can be up to 5 MB.")
	}
	b, err := io.ReadAll(io.LimitReader(q.r.Body, maxLogo+1))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > maxLogo {
		return nil, fail(413, "Upload an instance logo up to 5 MB.")
	}
	mime, width, height := imageInfo(b)
	if mime == "" {
		return nil, fail(400, "Upload a PNG, JPEG, GIF or WebP image.")
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(q.ctx, `INSERT INTO instance_branding(id, bytes, mime, width, height) VALUES (1, $1, $2, $3, $4)
			ON CONFLICT (id) DO UPDATE SET bytes=excluded.bytes, mime=excluded.mime, width=excluded.width, height=excluded.height, updated_at=now()`,
			b, mime, width, height); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "instance_logo", q.me.ID, map[string]any{"action": "upload", "mime": mime, "width": width, "height": height})
	})
	if err != nil {
		return nil, err
	}
	return a.branding(q)
}

func (a *API) resetLogo(q *req) (any, error) {
	err := a.tx(q.ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(q.ctx, "DELETE FROM instance_branding WHERE id=1"); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "instance_logo", q.me.ID, map[string]any{"action": "reset"})
	})
	if err != nil {
		return nil, err
	}
	return a.branding(q)
}

// imageInfo accepts raster formats only: an SVG logo would be a script
// served from our origin.
func imageInfo(b []byte) (mime string, width, height int) {
	if len(b) >= 30 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		switch string(b[12:16]) {
		case "VP8X":
			w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
			h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
			return "image/webp", w + 1, h + 1
		case "VP8 ":
			if b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
				return "image/webp", int(binary.LittleEndian.Uint16(b[26:]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:]) & 0x3fff)
			}
		case "VP8L":
			if b[20] == 0x2f {
				v := binary.LittleEndian.Uint32(b[21:])
				return "image/webp", int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1
			}
		}
		return "", 0, 0
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", 0, 0
	}
	return "image/" + format, cfg.Width, cfg.Height
}
