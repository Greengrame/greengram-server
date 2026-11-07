package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The whole settings document the admin panel edits. Keys, defaults and
// ranges are settings.js's, so the panel's form works unchanged. Settings
// (the struct) is the typed subset the server itself reads.

func defaultsDoc() map[string]any {
	name := strings.TrimSpace(os.Getenv("INSTANCE_NAME"))
	if name == "" {
		name = "Greengram"
	}
	return map[string]any{
		"siteName":                   name,
		"registrationEnabled":        true,
		"inactiveAccountDeletion":    true,
		"messageMaxLength":           4096,
		"groupMemberLimit":           101,
		"sessionDays":                7,
		"defaultPremiumDays":         0,
		"starsAdjustmentLimit":       1000000,
		"starsPerUSD":                100,
		"loginAttemptsPerMinute":     15,
		"otpExpirySeconds":           300,
		"maxOtpAttempts":             5,
		"qrExpirySeconds":            120,
		"premiumMonthlyStars":        250,
		"premiumQuarterlyStars":      675,
		"premiumAnnualStars":         2250,
		"profileRatingStarsPerLevel": 100,
		"boostsPerLevel":             2,
		"premiumMessageMaxLength":    10000,
	}
}

var ranges = map[string][2]int64{
	"starsPerUSD": {1, 1000000}, "profileRatingStarsPerLevel": {1, 1000000000}, "boostsPerLevel": {1, 1000000},
	"messageMaxLength": {1, 10000}, "groupMemberLimit": {2, 500}, "sessionDays": {1, 90}, "defaultPremiumDays": {0, 3650},
	"starsAdjustmentLimit": {1, 1000000000}, "loginAttemptsPerMinute": {3, 100}, "otpExpirySeconds": {60, 600},
	"maxOtpAttempts": {3, 10}, "qrExpirySeconds": {30, 300}, "premiumMonthlyStars": {1, 1000000},
	"premiumQuarterlyStars": {1, 1000000}, "premiumAnnualStars": {1, 1000000}, "premiumMessageMaxLength": {1, 10000},
}

// Invalid is a value the admin may not save; the message is for them.
type Invalid struct{ Message string }

func (e *Invalid) Error() string { return e.Message }

// All is the full document: defaults with the stored values on top. Keys
// stored by other features (not in the defaults) pass through untouched.
func (s *Source) All(ctx context.Context) (map[string]any, error) {
	out := defaultsDoc()
	rows, err := s.db.Query(ctx, "SELECT key, value FROM instance_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var val any
		if json.Unmarshal(v, &val) == nil {
			out[k] = val
		}
	}
	return out, rows.Err()
}

// Check validates a patch the way saveSettings does and returns it cleaned.
func Check(patch map[string]any) (map[string]any, error) {
	def := defaultsDoc()
	if len(patch) == 0 {
		return nil, &Invalid{"Choose valid settings."}
	}
	out := map[string]any{}
	for k, v := range patch {
		if _, ok := def[k]; !ok {
			return nil, &Invalid{"Choose valid settings."}
		}
		switch k {
		case "siteName":
			name, ok := v.(string)
			name = strings.TrimSpace(name)
			if !ok || name == "" || utf8.RuneCountInString(name) > 50 {
				return nil, &Invalid{"The workspace name must contain 1–50 characters."}
			}
			out[k] = name
		case "registrationEnabled", "inactiveAccountDeletion":
			b, ok := v.(bool)
			if !ok && k == "registrationEnabled" {
				return nil, &Invalid{"Choose a valid registration policy."}
			}
			if !ok {
				return nil, &Invalid{"Turn inactive account deletion on or off."}
			}
			out[k] = b
		default:
			r := ranges[k]
			f, ok := v.(float64)
			n := int64(f)
			if !ok || float64(n) != f || n < r[0] || n > r[1] {
				return nil, &Invalid{fmt.Sprintf("%s must be between %d and %d.", k, r[0], r[1])}
			}
			out[k] = n
		}
	}
	return out, nil
}

// Save writes a checked patch inside tx and drops the cache.
func (s *Source) Save(ctx context.Context, tx pgx.Tx, patch map[string]any) error {
	for k, v := range patch {
		raw, _ := json.Marshal(v)
		if _, err := tx.Exec(ctx, `INSERT INTO instance_settings(key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value=excluded.value, updated_at=now()`, k, raw); err != nil {
			return err
		}
	}
	s.Forget()
	return nil
}

// Forget makes the next Get read the table again.
func (s *Source) Forget() {
	s.mu.Lock()
	s.at = time.Time{}
	s.gen++
	s.mu.Unlock()
}
