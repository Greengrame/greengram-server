package botapi

import (
	"strconv"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

func (v *view) serviceBody(out *apiMessage, message messages.Msg) {
	var transfer *tg.MessageActionGiftTon
	var card wallet.LocalWalletCard
	var amount int64
	var peer int64
	var err error
	switch message.Action.(type) {
	case *tg.MessageActionTopicCreate:
		out.ForumTopicCreated = map[string]any{"name": message.Action.(*tg.MessageActionTopicCreate).Title, "icon_color": message.Action.(*tg.MessageActionTopicCreate).IconColor}
		out.Text, out.Entities = "", nil
	case *tg.MessageActionManagedBotCreated:
		out.ManagedBotCreated = map[string]any{"bot": v.user(ids.Server(message.Action.(*tg.MessageActionManagedBotCreated).BotID))}
		out.Text, out.Entities = "", nil
	case *tg.MessageActionNoPaidMessagesTokenUsed:
		out.PaidMessagesWaived = &struct{}{}
		out.Text, out.Entities = "", nil
	case *tg.MessageActionGiftTon:
		transfer = message.Action.(*tg.MessageActionGiftTon)
		if transfer.CryptoCurrency != "TON" || v.s.Wallet == nil {
			return
		}
		peer = message.From
		amount = transfer.CryptoAmount
		if message.Out {
			peer = message.Peer
			amount = -amount
		}
		card, err = v.s.Wallet.LocalCard(v.ctx, peer, false)
		if err != nil {
			return
		}
		out.TonWalletTransfer = map[string]any{"transaction_id": transfer.TransactionID, "peer_address": card.Address, "amount": strconv.FormatInt(amount, 10)}
		out.Text, out.Entities = "", nil
	}
}
