package e2e

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
)

func TestBotDeleteBusinessAccountGift(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "businessowner")
	var ctx = context.Background()
	var service = bots.New(s.db.Pool)
	var bot bots.Bot
	var token string
	var err error
	var connection bots.BusinessConnection
	var found bool
	var gift, receipt, before, after int64
	var count, status int
	bot, token, err = service.Create(ctx, ada.id, "Business", "business_gifts_bot")
	if err != nil {
		t.Fatal(err)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		var request = &tg.AccountUpdateConnectedBotRequest{Bot: &tg.InputUser{UserID: ids.Client(bot.UserID)}, Recipients: tg.InputBusinessBotRecipients{ExistingChats: true}}
		var err error
		request.SetRights(tg.BusinessBotRights{SellGifts: true})
		_, err = c.AccountUpdateConnectedBot(ctx, request)
		return err
	})
	connection, found, err = service.ConnectedBusiness(ctx, ada.id)
	if err != nil || !found {
		t.Fatalf("connection: %v %v", connection, err)
	}
	s.bot(t, token, "getBusinessConnection", map[string]any{"business_connection_id": connection.ID})
	err = s.db.Pool.QueryRow(ctx, `INSERT INTO gift_catalog(title,emoji,stars,convert_stars) VALUES('Regular','🎁',10,5) RETURNING id`).Scan(&gift)
	if err != nil {
		t.Fatal(err)
	}
	err = s.db.Pool.QueryRow(ctx, `INSERT INTO gift_receipts(gift_id,sender_id,owner_id,cost,convert_stars,created_at) VALUES($1,$2,$2,10,5,$3) RETURNING id`, gift, ada.id, int(time.Now().Unix())).Scan(&receipt)
	if err != nil {
		t.Fatal(err)
	}
	before, err = s.stars.Balance(ctx, ada.id)
	if err != nil {
		t.Fatal(err)
	}
	s.bot(t, token, "deleteBusinessAccountGift", map[string]any{"business_connection_id": connection.ID, "owned_gift_id": strconv.FormatInt(receipt, 10)})
	after, err = s.stars.Balance(ctx, ada.id)
	if err != nil || after != before {
		t.Fatalf("gift deletion changed Stars balance: %d %d %v", before, after, err)
	}
	err = s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM gift_receipts WHERE id=$1", receipt).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("gift was not deleted")
	}
	err = service.DisableBusiness(ctx, ada.id)
	if err != nil {
		t.Fatal(err)
	}
	_, status = s.botCall(t, token, "deleteBusinessAccountGift", map[string]any{"business_connection_id": connection.ID, "owned_gift_id": strconv.FormatInt(receipt, 10)})
	if status == 200 {
		t.Fatal("disconnected bot deleted a gift")
	}
}
