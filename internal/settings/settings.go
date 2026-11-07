// Package settings reads instance settings from instance_settings, one JSON
// value per key, same names as greengram-web's settings.js.
package settings

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Settings struct {
	RegistrationEnabled    bool `json:"registrationEnabled"`
	LoginAttemptsPerMinute int  `json:"loginAttemptsPerMinute"`
	DefaultPremiumDays     int  `json:"defaultPremiumDays"`
	QRExpirySeconds        int  `json:"qrExpirySeconds"`
	SessionDays            int  `json:"sessionDays"`
	// InactiveAccountDeletion runs the job that deletes accounts away for
	// longer than their account.setAccountTTL period.
	InactiveAccountDeletion bool `json:"inactiveAccountDeletion"`

	StarsAdjustmentLimit       int `json:"starsAdjustmentLimit"`
	StarsPerUSD                int `json:"starsPerUSD"`
	PremiumMonthlyStars        int `json:"premiumMonthlyStars"`
	PremiumQuarterlyStars      int `json:"premiumQuarterlyStars"`
	PremiumAnnualStars         int `json:"premiumAnnualStars"`
	ProfileRatingStarsPerLevel int `json:"profileRatingStarsPerLevel"`
	BoostsPerLevel             int `json:"boostsPerLevel"`
}

func Defaults() Settings {
	return Settings{
		RegistrationEnabled:    true,
		LoginAttemptsPerMinute: 15,
		QRExpirySeconds:        120,
		SessionDays:            7,

		InactiveAccountDeletion: true,

		StarsAdjustmentLimit:       1000000,
		StarsPerUSD:                100,
		PremiumMonthlyStars:        250,
		PremiumQuarterlyStars:      675,
		PremiumAnnualStars:         2250,
		ProfileRatingStarsPerLevel: 100,
		BoostsPerLevel:             2,
	}
}

// Source caches the table for a few seconds; the admin panel can wait that long.
type Source struct {
	db *pgxpool.Pool

	mu      sync.Mutex
	at      time.Time // zero: nothing to serve, the next Get reads the table
	last    Settings
	loading bool
	gen     int // Forget moves it, so a read from before can't fill the cache
}

func New(db *pgxpool.Pool) *Source { return &Source{db: db} }

// Get never holds mu over the query. It used to, and with the pool full, one
// caller waiting for a connection stopped every other caller, including ones
// holding the connections it waited for: everything froze until its deadline.
func (s *Source) Get(ctx context.Context) Settings {
	s.mu.Lock()
	// while one caller reads the table the rest take the old value
	if !s.at.IsZero() && (s.loading || time.Since(s.at) < 10*time.Second) {
		defer s.mu.Unlock()
		return s.last
	}
	s.loading = true
	gen := s.gen
	s.mu.Unlock()

	out, err := s.load(ctx)
	out.clamp()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loading = false
	if err != nil {
		// a busy moment isn't a reason to fall back to the defaults
		if !s.at.IsZero() {
			return s.last
		}
		return out
	}
	if gen == s.gen {
		s.last, s.at = out, time.Now()
	}
	return out
}

// load reads the table. On an error out is the defaults.
func (s *Source) load(ctx context.Context) (Settings, error) {
	out := Defaults()
	if s.db == nil {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT key, value FROM instance_settings")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	doc := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if rows.Scan(&k, &v) == nil {
			doc[k] = v
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if b, err := json.Marshal(doc); err == nil {
		json.Unmarshal(b, &out)
	}
	return out, nil
}

// ranges from settings.js
func (s *Settings) clamp() {
	s.LoginAttemptsPerMinute = min(max(s.LoginAttemptsPerMinute, 3), 100)
	s.DefaultPremiumDays = min(max(s.DefaultPremiumDays, 0), 3650)
	s.QRExpirySeconds = min(max(s.QRExpirySeconds, 30), 300)
	s.SessionDays = min(max(s.SessionDays, 1), 90)
	s.StarsAdjustmentLimit = min(max(s.StarsAdjustmentLimit, 1), 1000000000)
	s.StarsPerUSD = min(max(s.StarsPerUSD, 1), 1000000)
	s.PremiumMonthlyStars = min(max(s.PremiumMonthlyStars, 1), 1000000)
	s.PremiumQuarterlyStars = min(max(s.PremiumQuarterlyStars, 1), 1000000)
	s.PremiumAnnualStars = min(max(s.PremiumAnnualStars, 1), 1000000)
	s.ProfileRatingStarsPerLevel = min(max(s.ProfileRatingStarsPerLevel, 1), 1000000000)
	s.BoostsPerLevel = min(max(s.BoostsPerLevel, 1), 1000000)
}
