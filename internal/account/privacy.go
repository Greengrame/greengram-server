package account

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Privacy is one key's rules, stored as the same JSON greengram-web keeps in
// account_settings.privacy[key], so the import copies it as is.
type Privacy struct {
	Visibility         string   `json:"visibility"` // everybody, contacts, nobody
	AllowUserIDs       []string `json:"allowUserIds"`
	AllowChatIDs       []string `json:"allowChatIds"`
	BlockUserIDs       []string `json:"blockUserIds"`
	BlockChatIDs       []string `json:"blockChatIds"`
	BotsPrivacy        string   `json:"botsPrivacy"` // none, allow, disallow
	ShouldAllowPremium bool     `json:"shouldAllowPremium,omitempty"`
}

var PrivacyKeys = map[string]bool{
	"phoneNumber": true, "addByPhone": true, "lastSeen": true, "profilePhoto": true, "voiceMessages": true,
	"forwards": true, "chatInvite": true, "phoneCall": true, "phoneP2P": true, "bio": true, "birthday": true,
	"gifts": true, "noPaidMessages": true, "savedMusic": true,
}

func DefaultPrivacy(key string) Privacy {
	p := Privacy{Visibility: "everybody", AllowUserIDs: []string{}, AllowChatIDs: []string{}, BlockUserIDs: []string{}, BlockChatIDs: []string{}, BotsPrivacy: "none"}
	if key == "phoneNumber" || key == "birthday" {
		p.Visibility = "contacts"
	}
	return p
}

func (s *Service) Privacy(ctx context.Context, userID int64, key string) (Privacy, error) {
	if !PrivacyKeys[key] {
		return Privacy{}, fail(400, "PRIVACY_KEY_INVALID")
	}
	var raw []byte
	err := s.db.QueryRow(ctx, "SELECT rules FROM privacy_rules WHERE user_id=$1 AND key=$2", userID, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPrivacy(key), nil
	}
	if err != nil {
		return Privacy{}, err
	}
	p := DefaultPrivacy(key)
	if err := json.Unmarshal(raw, &p); err != nil {
		return Privacy{}, err
	}
	return p, nil
}

// PrivacyMany is Privacy for every user and key at once: user -> key ->
// rules, defaults filled in.
func (s *Service) PrivacyMany(ctx context.Context, users []int64, keys []string) (map[int64]map[string]Privacy, error) {
	out := make(map[int64]map[string]Privacy, len(users))
	for _, u := range users {
		m := make(map[string]Privacy, len(keys))
		for _, k := range keys {
			m[k] = DefaultPrivacy(k)
		}
		out[u] = m
	}
	if len(users) == 0 || len(keys) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT user_id, key, rules FROM privacy_rules WHERE user_id = ANY($1) AND key = ANY($2)", users, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			user int64
			key  string
			raw  []byte
		)
		if err := rows.Scan(&user, &key, &raw); err != nil {
			return nil, err
		}
		p := DefaultPrivacy(key)
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if out[user] != nil {
			out[user][key] = p
		}
	}
	return out, rows.Err()
}

func (s *Service) SetPrivacy(ctx context.Context, userID int64, key string, p Privacy) error {
	if !PrivacyKeys[key] {
		return fail(400, "PRIVACY_KEY_INVALID")
	}
	switch p.Visibility {
	case "everybody", "contacts", "nobody":
	default:
		return fail(400, "PRIVACY_VALUE_INVALID")
	}
	if len(p.AllowUserIDs)+len(p.BlockUserIDs)+len(p.AllowChatIDs)+len(p.BlockChatIDs) > 1000 {
		return fail(400, "PRIVACY_TOO_LONG")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO privacy_rules(user_id, key, rules) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET rules = excluded.rules`, userID, key, raw)
	return err
}
