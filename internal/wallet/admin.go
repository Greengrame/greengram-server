package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Admin credits and debits are how crypto gets onto the instance. A
// request id may be retried; the same payload gets the same receipt, any
// other payload is refused.

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func requireAdmin(actor account.User) error {
	if actor.Role != "admin" || actor.Disabled {
		return fail(403, "Administrator access is required.")
	}
	return nil
}

// human is any person account, disabled or not; bots have no wallet.
func (s *Service) human(ctx context.Context, actor account.User, id any) (int64, error) {
	if err := requireAdmin(actor); err != nil {
		return 0, err
	}
	n, ok := jsInt(id)
	if !ok || n < 1 {
		return 0, fail(400, "Choose a person account.")
	}
	var found int64
	err := s.db.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND NOT is_bot", n).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fail(404, "Person account not found.")
	}
	return found, err
}

// payloadHash hashes the same JSON.stringify the web hashed, key order and all.
func payloadHash(actor, recipient int64, asset, units, direction, note string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(struct {
		ActorID     int64  `json:"actorId"`
		RecipientID int64  `json:"recipientId"`
		Asset       string `json:"asset"`
		Units       string `json:"units"`
		Direction   string `json:"direction"`
		Note        string `json:"note"`
	}{actor, recipient, asset, units, direction, note})
	sum := sha256.Sum256(bytes.TrimRight(b.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}

func (s *Service) receipt(ctx context.Context, q querier, requestID string) (map[string]any, error) {
	var txID, asset, units, dir, note string
	var recipient, actor int64
	var at int
	err := q.QueryRow(ctx, `SELECT transaction_id, recipient_id, actor_id, asset, units::text, direction, note, created_at
		FROM crypto_admin_transactions WHERE request_id=$1`, requestID).Scan(&txID, &recipient, &actor, &asset, &units, &dir, &note, &at)
	if err != nil {
		return nil, err
	}
	bal, err := Balance(ctx, q, recipient, asset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "monetaryValue": false, "transactionId": txID, "recipientId": recipient, "actorId": actor, "asset": asset,
		"amount": format(bigOf(units), asset), "direction": dir, "note": note, "createdAt": at, "balance": format(bal, asset)}, nil
}

// AdminTransaction is adminCryptoTransactions().
func (s *Service) AdminTransaction(ctx context.Context, actor account.User, data map[string]any) (map[string]any, error) {
	if err := requireAdmin(actor); err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fail(400, "Provide transaction details.")
	}
	recipient, err := s.human(ctx, actor, data["recipientId"])
	if err != nil {
		return nil, err
	}
	code := Canonical(jsString(data["asset"]))
	units, err := parseAmount(data["amount"], code)
	if err != nil {
		return nil, err
	}
	dir, _ := data["direction"].(string)
	requestID, _ := data["requestId"].(string)
	note, noteOK := "", true
	if v, ok := data["note"]; ok {
		str, isStr := v.(string)
		note, noteOK = strings.TrimSpace(str), isStr
	}
	if units.Sign() < 1 || dir != "credit" && dir != "debit" || !requestIDRe.MatchString(requestID) || !noteOK || len([]rune(note)) > 300 {
		return nil, fail(400, "Choose a positive amount, credit or debit, valid request ID and a note up to 300 characters.")
	}
	hash := payloadHash(actor.ID, recipient, code, units.String(), dir, note)
	var out map[string]any
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, recipient); err != nil {
			return err
		}
		var prev string
		err := tx.QueryRow(ctx, "SELECT payload_hash FROM crypto_admin_transactions WHERE request_id=$1", requestID).Scan(&prev)
		if err == nil {
			if prev != hash {
				return fail(409, "This request ID was already used for a different transaction.")
			}
			out, err = s.receipt(ctx, tx, requestID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		diff := new(big.Int).Set(units)
		if dir == "debit" {
			diff.Neg(diff)
		}
		bal, err := Balance(ctx, tx, recipient, code)
		if err != nil {
			return err
		}
		if bal.Add(bal, diff).Sign() < 0 {
			return fail(400, "The balance cannot become negative.")
		}
		txID := "crypto_" + randomHex(16)
		if err := setBalance(ctx, tx, recipient, code, bal); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crypto_admin_transactions(request_id, payload_hash, transaction_id, actor_id, recipient_id, asset, units, direction, note, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10)`, requestID, hash, txID, actor.ID, recipient, code, units.String(), dir, note, s.unix()); err != nil {
			return err
		}
		if err := s.event(ctx, tx, txID, recipient, code, diff, "admin_adjustment", actor.ID); err != nil {
			return err
		}
		out, err = s.receipt(ctx, tx, requestID)
		return err
	})
	return out, err
}

// AdminWallet is one person's wallet as the admin panel shows it.
func (s *Service) AdminWallet(ctx context.Context, actor account.User, id int64) (map[string]any, error) {
	recipient, err := s.human(ctx, actor, float64(id))
	if err != nil {
		return nil, err
	}
	m := s.market.Refresh(ctx)
	accounts, err := s.Accounts(ctx, recipient, m)
	if err != nil {
		return nil, err
	}
	history, err := s.History(ctx, recipient)
	if err != nil {
		return nil, err
	}
	return map[string]any{"monetaryValue": false, "label": "Wallet", "recipientId": recipient, "accounts": accounts, "history": history, "market": s.market.Read()}, nil
}
