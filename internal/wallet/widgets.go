package wallet

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/secret"
)

type LocalWalletCard struct {
	Address    string             `json:"address"`
	Amount     string             `json:"amount"`
	Stickers   []LocalCardSticker `json:"stickers"`
	PublicKey  string             `json:"public_key,omitempty"`
	StateInit  string             `json:"state_init,omitempty"`
	Background string             `json:"background_nft_address,omitempty"`
}

type WidgetTransaction struct {
	ValidUntil int                     `json:"valid_until"`
	Messages   []WidgetTransferMessage `json:"messages"`
}

type WidgetTransferMessage struct {
	Address string `json:"address"`
	Amount  string `json:"amount"`
	Payload string `json:"payload,omitempty"`
}

func (s *Service) WithSigningKey(key *secret.Key) *Service {
	s.signingKey = key
	return s
}

func (s *Service) localWalletKey(user int64) (ed25519.PrivateKey, error) {
	var seed []byte
	var err error
	if s.signingKey == nil {
		return nil, fail(503, "Local wallet signing is unavailable.")
	}
	seed, err = hex.DecodeString(s.signingKey.Lookup("local-wallet:v1:" + strconv.FormatInt(user, 10)))
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func (s *Service) LocalCard(ctx context.Context, user int64, publish bool) (LocalWalletCard, error) {
	var card LocalWalletCard
	var key ed25519.PrivateKey
	var public ed25519.PublicKey
	var balance *big.Int
	var err error
	key, err = s.localWalletKey(user)
	if err != nil {
		return card, err
	}
	public = key.Public().(ed25519.PublicKey)
	card.Address = "gram:" + hex.EncodeToString(public)
	card.PublicKey = hex.EncodeToString(public)
	card.StateInit = base64.StdEncoding.EncodeToString(public)
	card.Stickers = []LocalCardSticker{}
	balance, err = s.TONBalance(ctx, user)
	if err != nil {
		return card, err
	}
	card.Amount = balance.String()
	_, err = s.db.Exec(ctx, `INSERT INTO local_wallet_cards(user_id,address,public) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET public=local_wallet_cards.public OR excluded.public`, user, card.Address, publish)
	if err != nil {
		return card, err
	}
	err = s.fillCardStickers(ctx, user, &card)
	return card, err
}

func (s *Service) PublicLocalCard(ctx context.Context, user int64) (*LocalWalletCard, error) {
	var exists bool
	var card LocalWalletCard
	var err error
	err = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM local_wallet_cards WHERE user_id=$1 AND public)", user).Scan(&exists)
	if err != nil || !exists {
		return nil, err
	}
	card, err = s.LocalCard(ctx, user, false)
	if err != nil {
		return nil, err
	}
	card.PublicKey, card.StateInit = "", ""
	return &card, nil
}

func (s *Service) SignWidgetData(ctx context.Context, user account.User, bot int64, payload json.RawMessage, passcode string) (string, error) {
	var key ed25519.PrivateKey
	var domain = "bot" + strconv.FormatInt(bot, 10)
	var timestamp = s.unix()
	var canonical []byte
	var digest [32]byte
	var signature []byte
	var result []byte
	var value map[string]any
	var err error
	if len(payload) == 0 || len(payload) > 16384 || !json.Valid(payload) {
		return "", fail(400, "Invalid signing payload.")
	}
	_, err = s.verify(ctx, user.ID, passcode)
	if err != nil {
		return "", err
	}
	err = json.Unmarshal(payload, &value)
	if err != nil || value["type"] != "text" && value["type"] != "binary" && value["type"] != "cell" {
		return "", fail(400, "Unsupported signing payload.")
	}
	canonical, err = json.Marshal(value)
	if err != nil {
		return "", err
	}
	key, err = s.localWalletKey(user.ID)
	if err != nil {
		return "", err
	}
	digest = sha256.Sum256([]byte("greengram-sign-data:v1:" + domain + "\n" + strconv.Itoa(timestamp) + "\n" + string(canonical)))
	signature = ed25519.Sign(key, digest[:])
	result, err = json.Marshal(map[string]any{"signature": base64.StdEncoding.EncodeToString(signature), "timestamp": timestamp, "domain": domain, "payload": value, "network": "greengram", "algorithm": "ed25519"})
	return string(result), err
}

func (s *Service) SendWidgetTransaction(ctx context.Context, user account.User, requestID string, transaction WidgetTransaction, passcode string, enqueue func(pgx.Tx, string) error) (string, error) {
	var raw []byte
	var digest [32]byte
	var hash string
	var result string
	var err error
	var recipients []int64
	var amounts []*big.Int
	var members []int64
	var i int
	var amount *big.Int
	var valid bool
	var total = new(big.Int)
	var notifications []Transfer
	var notification Transfer
	if len(requestID) < 16 || len(requestID) > 64 || len(transaction.Messages) == 0 || len(transaction.Messages) > 255 {
		return "", fail(400, "Invalid local wallet transaction.")
	}
	if transaction.ValidUntil != 0 && transaction.ValidUntil <= s.unix() {
		return "", fail(400, "Transaction has expired.")
	}
	_, err = s.verify(ctx, user.ID, passcode)
	if err != nil {
		return "", err
	}
	raw, err = json.Marshal(transaction)
	if err != nil {
		return "", err
	}
	digest = sha256.Sum256(raw)
	hash = hex.EncodeToString(digest[:])
	recipients = make([]int64, len(transaction.Messages))
	amounts = make([]*big.Int, len(transaction.Messages))
	members = []int64{user.ID}
	for i = range transaction.Messages {
		amount, valid = new(big.Int).SetString(transaction.Messages[i].Amount, 10)
		if !valid || amount.Sign() <= 0 || !amount.IsInt64() || !strings.HasPrefix(transaction.Messages[i].Address, "gram:") {
			return "", fail(400, "Use a local GRAM wallet address and a positive amount in smallest units.")
		}
		amounts[i] = amount
		total.Add(total, amount)
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var i int
		var previousHash string
		var previousResult []byte
		var notification Transfer
		var err error
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", strconv.FormatInt(user.ID, 10)+":"+requestID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, "SELECT request_hash,result FROM widget_wallet_transactions WHERE user_id=$1 AND request_id=$2", user.ID, requestID).Scan(&previousHash, &previousResult)
		if err == nil {
			if previousHash != hash {
				return fail(409, "This transaction ID was already used for another payment.")
			}
			err = json.Unmarshal(previousResult, &result)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = s.requirePerson(ctx, tx, user.ID)
		if err != nil {
			return err
		}
		for i = range transaction.Messages {
			err = tx.QueryRow(ctx, `SELECT c.user_id FROM local_wallet_cards c JOIN users u ON u.id=c.user_id WHERE c.address=$1 AND NOT u.disabled AND NOT u.is_system`, transaction.Messages[i].Address).Scan(&recipients[i])
			if err != nil {
				return fail(400, "Recipient local wallet is unavailable.")
			}
			if recipients[i] == user.ID {
				return fail(400, "Sending to your own wallet is not supported.")
			}
			members = append(members, recipients[i])
		}
		err = lock(ctx, tx, members...)
		if err != nil {
			return err
		}
		_, err = change(ctx, tx, user.ID, "TON", new(big.Int).Neg(total))
		if err != nil {
			return err
		}
		for i = range transaction.Messages {
			_, err = change(ctx, tx, recipients[i], "TON", amounts[i])
			if err != nil {
				return err
			}
			err = s.event(ctx, tx, "widget:"+strconv.FormatInt(user.ID, 10)+":"+requestID+":"+strconv.Itoa(i)+":out", user.ID, "TON", new(big.Int).Neg(amounts[i]), "internal_transfer", recipients[i])
			if err != nil {
				return err
			}
			err = s.event(ctx, tx, "widget:"+strconv.FormatInt(user.ID, 10)+":"+requestID+":"+strconv.Itoa(i)+":in", recipients[i], "TON", amounts[i], "internal_transfer", user.ID)
			if err != nil {
				return err
			}
			notification = Transfer{From: user.ID, To: recipients[i], Asset: "TON", Nanos: amounts[i].Int64(), ID: requestID + ":" + strconv.Itoa(i), Text: user.Name + " sent " + FormatAmount(amounts[i], "TON") + " GRAM via a widget."}
			notifications = append(notifications, notification)
		}
		result = "gram:" + requestID
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO widget_wallet_transactions(user_id,request_id,request_hash,result) VALUES($1,$2,$3,$4)", user.ID, requestID, hash, raw)
		if err != nil {
			return err
		}
		if enqueue != nil {
			return enqueue(tx, result)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if s.notify != nil {
		for _, notification = range notifications {
			err = s.notify(ctx, notification)
			if err != nil {
				continue
			}
		}
	}
	return result, nil
}
