package botapi

import (
	"math/big"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

func (s *Server) widgetWalletQuery(r *http.Request, user account.User, message bots.WidgetMessage, p params, payload map[string]any) (int64, error) {
	var card wallet.LocalWalletCard
	var transaction wallet.WidgetTransaction
	var result string
	var id int64
	var err error
	var action = p.str("action")
	if s.Wallet == nil {
		return 0, bad("local wallet is unavailable")
	}
	if action != "card" && action != "sign_data" && action != "send_transaction" {
		return 0, bad("invalid wallet action")
	}
	if action == "sign_data" {
		result, err = s.Wallet.SignWidgetData(r.Context(), user, message.Widget.Bot, []byte(p.str("payload")), p.str("passcode"))
		if err != nil {
			return 0, err
		}
	}
	card, err = s.Wallet.LocalCard(r.Context(), user.ID, action == "card")
	if err != nil {
		return 0, err
	}
	switch action {
	case "card":
		card.PublicKey, card.StateInit = "", ""
		payload["type"] = map[string]any{"type": "data", "wallet_card": card}
	case "sign_data":
		payload["type"] = map[string]any{"type": "sign_data", "wallet": widgetQueryWallet(card), "sign_data": result}
	case "send_transaction":
		_, err = p.json("payload", &transaction)
		if err != nil {
			return 0, err
		}
		result, err = s.Wallet.SendWidgetTransaction(r.Context(), user, p.str("request_id"), transaction, p.str("passcode"), func(tx pgx.Tx, result string) error {
			var err error
			var balance *big.Int
			balance, err = wallet.Balance(r.Context(), tx, user.ID, "TON")
			if err != nil {
				return err
			}
			card.Amount = balance.String()
			payload["type"] = map[string]any{"type": "sent_transaction", "wallet": widgetQueryWallet(card), "msg_hash": result}
			id, err = s.Bots.QueueWidgetQueryTx(r.Context(), tx, message, user.ID, payload)
			return err
		})
		if err != nil {
			return 0, err
		}
		if id != 0 {
			return id, nil
		}
		payload["type"] = map[string]any{"type": "sent_transaction", "wallet": widgetQueryWallet(card), "msg_hash": result}
	}
	return s.Bots.QueueWidgetQuery(r.Context(), message, user.ID, payload)
}

func widgetQueryWallet(card wallet.LocalWalletCard) map[string]any {
	return map[string]any{"address": card.Address, "public_key": card.PublicKey, "state_init": card.StateInit}
}
