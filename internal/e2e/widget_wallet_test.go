package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

func TestWidgetLocalWallet(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "walletsender")
	var bob = s.signUp(t, "walletrecipient")
	var ctx = context.Background()
	var service = wallet.New(s.db.Pool, s.settings, nil, nil).WithSigningKey(s.secret)
	var user account.User
	var users map[int64]account.User
	var card wallet.LocalWalletCard
	var public *wallet.LocalWalletCard
	var transaction wallet.WidgetTransaction
	var result, again, signature string
	var err error
	var balancesender, balancereceiver string
	var decoded map[string]any
	var signaturebytes, publickey []byte
	var payload []byte
	var hash [32]byte
	var enqueueCount int
	var browser = s.browser(t)
	var sessionHash string
	users, err = s.accounts.Users(ctx, []int64{ada.id})
	if err != nil {
		t.Fatal(err)
	}
	user = users[ada.id]
	browser.must("/api/login", map[string]any{"username": "walletsender", "password": "correct horse walletsender"})
	_, sessionHash, err = s.accounts.SessionUser(ctx, browser.session())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Passcode(ctx, user, sessionHash, "POST", "/local/passcode/create", map[string]any{"passcode": "123456", "confirmation": "123456"})
	if err != nil {
		t.Fatal(err)
	}
	public, err = service.PublicLocalCard(ctx, ada.id)
	if err != nil || public != nil {
		t.Fatal("wallet card must be private until published")
	}
	card, err = service.LocalCard(ctx, bob.id, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Pool.Exec(ctx, "INSERT INTO crypto_accounts(user_id,asset,units) VALUES($1,'TON',2000000000)", ada.id)
	if err != nil {
		t.Fatal(err)
	}
	transaction = wallet.WidgetTransaction{Messages: []wallet.WidgetTransferMessage{{Address: card.Address, Amount: "500000000"}}}
	result, err = service.SendWidgetTransaction(ctx, user, "test-widget-wallet-request-1", transaction, "123456", func(tx pgx.Tx, result string) error { enqueueCount++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	again, err = service.SendWidgetTransaction(ctx, user, "test-widget-wallet-request-1", transaction, "123456", func(tx pgx.Tx, result string) error { enqueueCount++; return nil })
	if err != nil || again != result || enqueueCount != 1 {
		t.Fatalf("transaction replay: %q %q %d %v", result, again, enqueueCount, err)
	}
	err = s.db.Pool.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", ada.id).Scan(&balancesender)
	if err != nil {
		t.Fatal(err)
	}
	err = s.db.Pool.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", bob.id).Scan(&balancereceiver)
	if err != nil || balancesender != "1500000000" || balancereceiver != "500000000" {
		t.Fatalf("unbalanced transfer: %s %s %v", balancesender, balancereceiver, err)
	}
	_, err = service.SendWidgetTransaction(ctx, user, "test-widget-wallet-rollback", transaction, "123456", func(tx pgx.Tx, result string) error { return errors.New("queue unavailable") })
	if err == nil {
		t.Fatal("payment committed without its bot event")
	}
	err = s.db.Pool.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", ada.id).Scan(&balancesender)
	if err != nil || balancesender != "1500000000" {
		t.Fatal("failed event enqueue did not roll back payment")
	}
	signature, err = service.SignWidgetData(ctx, user, 123, json.RawMessage(`{"type":"text","text":"hello"}`), "123456")
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal([]byte(signature), &decoded)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(decoded["payload"])
	hash = sha256.Sum256([]byte("greengram-sign-data:v1:bot123\n" + strconv.Itoa(int(decoded["timestamp"].(float64))) + "\n" + string(payload)))
	signaturebytes, err = base64.StdEncoding.DecodeString(decoded["signature"].(string))
	if err != nil {
		t.Fatal(err)
	}
	card, err = service.LocalCard(ctx, ada.id, false)
	if err != nil {
		t.Fatal(err)
	}
	publickey, _ = hex.DecodeString(card.PublicKey)
	if !ed25519.Verify(ed25519.PublicKey(publickey), hash[:], signaturebytes) {
		t.Fatal("local wallet signature is invalid")
	}
}
