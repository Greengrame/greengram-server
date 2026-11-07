package stars

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Gift is a catalog entry. Stock is what a limited gift has left.
type Gift struct {
	ID           int64
	Title        string
	Emoji        string
	Stars        int64
	ConvertStars int64
	PremiumOnly  bool
	Limited      bool
	Stock        int
	StockTotal   int
	Enabled      bool
	StickerID    int64 // 0 until the artwork is uploaded

	// UpgradeSet is the collection it upgrades into, 0 when it doesn't.
	// UpgradeLeft is what that collection has left once every upgrade
	// already paid for is taken out; it means nothing when the collection
	// has no supply limit (UpgradeUnlimited).
	UpgradeSet       int64
	UpgradeStars     int64
	TransferStars    int64
	UpgradeLeft      int
	UpgradeUnlimited bool
}

// Upgradable says one more of it can be upgraded, for someone who hasn't
// paid yet.
func (g Gift) Upgradable() bool {
	return g.UpgradeSet != 0 && (g.UpgradeUnlimited || g.UpgradeLeft > 0)
}

// giftColumns is the catalog row under alias p ("gift_catalog." or "g.":
// the subqueries need it). The supply left counts prepaid upgrades as
// taken: they were promised a number.
func giftColumns(p string) string {
	return p + "id, " + p + "title, " + p + "emoji, " + p + "stars, " + p + "convert_stars, " + p + "premium_only, " + p + "stock IS NOT NULL, coalesce(" + p + "stock, 0), " +
		"coalesce(" + p + "stock_total, " + p + "stock, 0), " + p + "enabled, coalesce(" + p + "sticker_id, 0), " +
		"coalesce(" + p + "upgrade_set_id, 0), coalesce(" + p + "upgrade_stars, 0), " + p + "transfer_stars, " +
		"coalesce((SELECT cs.supply - cs.issued FROM collectible_sets cs WHERE cs.id = " + p + "upgrade_set_id), 0) - " +
		"(SELECT count(*) FROM gift_receipts pr WHERE pr.gift_id = " + p + "id AND pr.upgrade_prepaid > 0 AND pr.unique_id IS NULL AND NOT pr.converted), " +
		"coalesce((SELECT cs.supply = 0 FROM collectible_sets cs WHERE cs.id = " + p + "upgrade_set_id), false)"
}

var giftCols = giftColumns("gift_catalog.")

func giftDest(g *Gift) []any {
	return []any{&g.ID, &g.Title, &g.Emoji, &g.Stars, &g.ConvertStars, &g.PremiumOnly, &g.Limited, &g.Stock, &g.StockTotal, &g.Enabled, &g.StickerID,
		&g.UpgradeSet, &g.UpgradeStars, &g.TransferStars, &g.UpgradeLeft, &g.UpgradeUnlimited}
}

func scanGift(row pgx.Row) (Gift, error) {
	var g Gift
	err := row.Scan(giftDest(&g)...)
	if g.UpgradeLeft < 0 {
		g.UpgradeLeft = 0
	}
	return g, err
}

// MaxGiftMessage is gifts.js's 255 characters.
const MaxGiftMessage = 255

var (
	errGiftInvalid     = tgerr.New(400, "STARGIFT_INVALID")
	errGiftSoldOut     = tgerr.New(400, "STARGIFT_USAGE_LIMITED")
	errPremiumRequired = tgerr.New(403, "PREMIUM_ACCOUNT_REQUIRED")
	errGiftMessage     = tgerr.New(400, "STARGIFT_MESSAGE_INVALID")
	errSavedGift       = tgerr.New(400, "SAVED_ID_EMPTY")
)

// Catalog is what can be bought now, cheapest first.
func (s *Service) Catalog(ctx context.Context) ([]Gift, error) {
	rows, err := s.db.Query(ctx, "SELECT "+giftCols+" FROM gift_catalog WHERE enabled ORDER BY stars, id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Gift, error) { return scanGift(r) })
}

func (s *Service) gift(ctx context.Context, q querier, id int64) (Gift, error) {
	g, err := scanGift(q.QueryRow(ctx, "SELECT "+giftCols+" FROM gift_catalog WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, errGiftInvalid
	}
	return g, err
}

// AddGift puts a gift in the catalog; the admin panel is its real caller.
// stock < 0 is unlimited.
func (s *Service) AddGift(ctx context.Context, g Gift) (Gift, error) {
	if g.Stars < 1 || g.Stars > MaxBalance || g.ConvertStars < 0 || g.ConvertStars > MaxBalance {
		return g, errGiftInvalid
	}
	var stock *int
	if g.Limited {
		stock = &g.Stock
	}
	var sticker *int64
	if g.StickerID != 0 {
		sticker = &g.StickerID
	}
	err := s.db.QueryRow(ctx, `INSERT INTO gift_catalog(title, emoji, stars, convert_stars, premium_only, stock, stock_total, sticker_id)
		VALUES ($1, $2, $3, $4, $5, $6, $6, $7) RETURNING id`, g.Title, g.Emoji, g.Stars, g.ConvertStars, g.PremiumOnly, stock, sticker).Scan(&g.ID)
	g.Enabled, g.StockTotal = true, g.Stock
	return g, err
}

// CatalogGift is a gift as the admin panel sees it, however many are sold.
type CatalogGift struct {
	Gift
	Sold int
}

var errGiftNotFound = &account.Error{Status: 404, Message: "Gift not found."}

var catalogQuery = "SELECT " + giftColumns("g.") + ", (SELECT count(*) FROM gift_receipts r WHERE r.gift_id = g.id) FROM gift_catalog g"

func scanCatalogGift(row pgx.Row) (CatalogGift, error) {
	var c CatalogGift
	err := row.Scan(append(giftDest(&c.Gift), &c.Sold)...)
	if c.UpgradeLeft < 0 {
		c.UpgradeLeft = 0
	}
	return c, err
}

// AllGifts is the whole catalog, disabled ones too.
func (s *Service) AllGifts(ctx context.Context) ([]CatalogGift, error) {
	rows, err := s.db.Query(ctx, catalogQuery+" ORDER BY g.stars, g.id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CatalogGift, error) { return scanCatalogGift(r) })
}

func (s *Service) CatalogGift(ctx context.Context, id int64) (CatalogGift, error) {
	c, err := scanCatalogGift(s.db.QueryRow(ctx, catalogQuery+" WHERE g.id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errGiftNotFound
	}
	return c, err
}

// UpdateGift saves an admin's edit. A receipt keeps what the gift was worth
// when it was bought; only new sales see the new numbers.
func (s *Service) UpdateGift(ctx context.Context, g Gift) error {
	var stock, total *int
	if g.Limited {
		stock, total = &g.Stock, &g.StockTotal
	}
	var sticker *int64
	if g.StickerID != 0 {
		sticker = &g.StickerID
	}
	tag, err := s.db.Exec(ctx, `UPDATE gift_catalog SET title=$2, emoji=$3, stars=$4, convert_stars=$5, premium_only=$6, stock=$7, stock_total=$8, enabled=$9, sticker_id=$10 WHERE id=$1`,
		g.ID, g.Title, g.Emoji, g.Stars, g.ConvertStars, g.PremiumOnly, stock, total, g.Enabled, sticker)
	if err == nil && tag.RowsAffected() == 0 {
		err = errGiftNotFound
	}
	return err
}

// RemoveGift deletes a gift nobody ever bought. One with receipts has to stay
// for them, so it's only taken off sale; deleted says which happened. An
// unpaid form for a deleted gift fails at payment as STARGIFT_INVALID.
func (s *Service) RemoveGift(ctx context.Context, id int64) (bool, error) {
	var deleted bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var sold bool
		err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM gift_receipts WHERE gift_id=$1) FROM gift_catalog WHERE id=$1 FOR UPDATE", id).Scan(&sold)
		if errors.Is(err, pgx.ErrNoRows) {
			return errGiftNotFound
		}
		if err != nil {
			return err
		}
		if sold {
			_, err = tx.Exec(ctx, "UPDATE gift_catalog SET enabled=false WHERE id=$1", id)
			return err
		}
		deleted = true
		_, err = tx.Exec(ctx, "DELETE FROM gift_catalog WHERE id=$1", id)
		return err
	})
	return deleted, err
}

// CheckGift is quoteGift()'s checks that don't depend on the recipient.
func (s *Service) CheckGift(ctx context.Context, id int64, buyerPremium bool) (Gift, error) {
	g, err := s.gift(ctx, s.db, id)
	switch {
	case err != nil:
		return g, err
	case !g.Enabled:
		return g, errGiftInvalid
	case g.PremiumOnly && !buyerPremium:
		return g, errPremiumRequired
	case g.Limited && g.Stock == 0:
		return g, errGiftSoldOut
	}
	// sold as variants, through its market only
	if on, err := sellsAsVariants(ctx, s.db, id); err != nil {
		return g, err
	} else if on {
		return g, errGiftSoldOut
	}
	return g, nil
}

// GiftForm prices a catalog gift for a person or a broadcast channel. With
// upgrade the buyer pays for the recipient's upgrade too. A channel's form
// names the buyer as recipient and the channel in Channel, like a
// transfer's; the caller checked the buyer can see the channel.
func (s *Service) GiftForm(ctx context.Context, buyer int64, to Holder, giftID int64, message string, hideName, buyerPremium, upgrade bool) (Form, Gift, error) {
	g, err := s.CheckGift(ctx, giftID, buyerPremium)
	if err != nil {
		return Form{}, g, err
	}
	if utf8.RuneCountInString(message) > MaxGiftMessage {
		return Form{}, g, errGiftMessage
	}
	f := Form{Kind: KindStarGift, User: buyer, Recipient: to.User, Channel: to.Channel, GiftID: g.ID, Stars: g.Stars, Message: message, HideName: hideName}
	if to.Channel != 0 {
		if err := giftChannel(ctx, s.db, to.Channel); err != nil {
			return Form{}, g, err
		}
		f.Recipient = buyer
	} else if _, err := person(ctx, s.db, to.User); err != nil {
		return Form{}, g, err
	}
	if upgrade {
		if !g.Upgradable() {
			return Form{}, g, errUpgradeUnavailable
		}
		f.UpgradeStars = g.UpgradeStars
		f.Stars += g.UpgradeStars
	}
	f, err = s.issue(ctx, f)
	return f, g, err
}

// errGiftChannel: gifts go to broadcast channels only, not groups.
var errGiftChannel = tgerr.New(400, "PEER_ID_INVALID")

// giftChannel checks a channel can be given gifts (locking its row in a
// transaction, so it can't go away under the payment).
func giftChannel(ctx context.Context, q querier, channel int64) error {
	var broadcast bool
	err := q.QueryRow(ctx, "SELECT NOT megagroup FROM channels WHERE id=$1 AND NOT deleted FOR SHARE", channel).Scan(&broadcast)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !broadcast {
		return errGiftChannel
	}
	return err
}

type GiftResult struct {
	Form          Form
	Gift          Gift
	ReceiptID     int64
	Balance       int64
	TransactionID string
	// a channel's gift: its service post, made with the payment
	PostID int
}

// GiftPost posts a channel's gift message inside the payment's transaction
// and returns its id. res has the receipt.
type GiftPost func(ctx context.Context, tx pgx.Tx, res GiftResult) (int, error)

// PayGift is buyGift(): charge, take one from stock, record the receipt.
// For a person the caller sends the gift message and calls SetGiftMessage;
// a channel's gift message is posted by post, with the money. An upgrade
// bought with it goes to the gifts account now and holds a number in the
// collection until the recipient uses it.
func (s *Service) PayGift(ctx context.Context, buyer, formID int64, buyerPremium bool, post GiftPost) (GiftResult, error) {
	var out GiftResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, buyer, formID, KindStarGift)
		if err != nil {
			return err
		}
		out.Form = f
		g, err := scanGift(tx.QueryRow(ctx, "SELECT "+giftCols+" FROM gift_catalog WHERE id=$1 FOR UPDATE", f.GiftID))
		switch {
		case errors.Is(err, pgx.ErrNoRows) || err == nil && !g.Enabled:
			return errGiftInvalid
		case err != nil:
			return err
		case g.Limited && g.Stock == 0:
			return errGiftSoldOut
		case g.PremiumOnly && !buyerPremium:
			return errPremiumRequired
		}
		if on, err := sellsAsVariants(ctx, tx, g.ID); err != nil {
			return err
		} else if on {
			return errGiftSoldOut
		}
		if f.UpgradeStars > 0 {
			// under the collection's lock, so two buyers can't both take the last number
			if g.UpgradeSet == 0 {
				return errUpgradeUnavailable
			}
			if g, err = s.lockSet(ctx, tx, g); err != nil {
				return err
			}
			if !g.Upgradable() {
				return errGiftSoldOut
			}
		}
		out.Gift = g
		sys := int64(0)
		if f.UpgradeStars > 0 {
			if sys, err = Account(ctx, tx, GiftsAccount); err != nil {
				return err
			}
		}
		if err := Lock(ctx, tx, nonZero(buyer, f.Recipient, sys)...); err != nil {
			return err
		}
		peer, owner, channel := f.Recipient, &f.Recipient, (*int64)(nil)
		if f.Channel != 0 {
			if post == nil {
				return errGiftChannel
			}
			if err := giftChannel(ctx, tx, f.Channel); err != nil {
				return err
			}
			peer, owner, channel = 0, nil, &f.Channel
		} else if _, err := person(ctx, tx, f.Recipient); err != nil {
			return err
		}
		now := s.unix()
		// the price is the form's; the conversion value is the catalog's now
		convert := g.ConvertStars
		out.TransactionID, err = Post(ctx, tx, Entry{User: buyer, Amount: -(f.Stars - f.UpgradeStars), Reason: "gift_purchase",
			Reference: fmt.Sprintf("gift:%d:%d", buyer, f.ID), CreatedBy: buyer, Peer: peer, Channel: f.Channel, Title: g.Title, GiftID: g.ID}, now)
		if err != nil {
			return err
		}
		if f.UpgradeStars > 0 {
			if err := move(ctx, tx, buyer, sys, f.UpgradeStars, "gift_upgrade", fmt.Sprintf("gift-upgrade-prepaid:%d", f.ID), g, now); err != nil {
				return err
			}
		}
		if g.Limited {
			if _, err := tx.Exec(ctx, "UPDATE gift_catalog SET stock = stock - 1 WHERE id=$1", g.ID); err != nil {
				return err
			}
			// the gift message carries the count left, this one taken
			out.Gift.Stock--
		}
		err = tx.QueryRow(ctx, `INSERT INTO gift_receipts(gift_id, sender_id, owner_id, owner_channel_id, cost, convert_stars, body, hide_name, created_at, upgrade_prepaid)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`, g.ID, buyer, owner, channel, f.Stars-f.UpgradeStars, convert, f.Message, f.HideName, now, f.UpgradeStars).Scan(&out.ReceiptID)
		if err != nil {
			return err
		}
		if f.Channel != 0 {
			if out.PostID, err = post(ctx, tx, out); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE gift_receipts SET message_id=$2 WHERE id=$1", out.ReceiptID, out.PostID); err != nil {
				return err
			}
		}
		out.Balance, err = Balance(ctx, tx, buyer)
		return err
	})
	return out, err
}

func nonZero(list ...int64) []int64 {
	out := list[:0:0]
	for _, v := range list {
		if v != 0 {
			out = append(out, v)
		}
	}
	return out
}

// GiftText is the message a gift arrives as: emoji, title, then the note. A
// custom emoji from outside the packs leaves the emoji empty.
func GiftText(g Gift, message string) string {
	t := strings.TrimSpace(g.Emoji + " " + g.Title)
	if message != "" {
		t += "\n" + message
	}
	return t
}

// SetGiftMessage links a receipt to the gift message in its owner's box.
func (s *Service) SetGiftMessage(ctx context.Context, receipt int64, msgID int) error {
	_, err := s.db.Exec(ctx, "UPDATE gift_receipts SET message_id=$2 WHERE id=$1", receipt, msgID)
	return err
}

// Holder is who has a gift: a person, or a channel it was given to.
type Holder struct{ User, Channel int64 }

func (h Holder) IsZero() bool { return h.User == 0 && h.Channel == 0 }

// where is the receipt column and value that match h.
func (h Holder) where() (string, int64) {
	if h.Channel != 0 {
		return "r.owner_channel_id", h.Channel
	}
	return "r.owner_id", h.User
}

// Receipt is a gift someone holds. A collectible is a receipt with Unique.
type Receipt struct {
	ID             int64
	Gift           Gift
	Sender         int64
	Owner          int64 // the person holding it; 0 when a channel does
	OwnerChannel   int64
	MessageID      int
	ConvertStars   int64
	Body           string
	HideName       bool
	Displayed      bool
	Date           int
	UpgradePrepaid int64
	UniqueID       int64
	Unique         *Unique
	// its place among the profile's pinned gifts, from 1; 0: not pinned
	Pinned int
}

func (r Receipt) Holder() Holder { return Holder{User: r.Owner, Channel: r.OwnerChannel} }

// CanUpgrade is whether its holder may upgrade it now: what someone paid for
// up front has its number waiting.
func (r Receipt) CanUpgrade() bool {
	return r.UniqueID == 0 && r.Gift.UpgradeSet != 0 && (r.UpgradePrepaid > 0 || r.Gift.Upgradable())
}

var receiptCols = "r.id, r.sender_id, coalesce(r.owner_id, 0), coalesce(r.owner_channel_id, 0), coalesce(r.message_id, 0), r.convert_stars, r.body, r.hide_name, r.displayed, " +
	"r.created_at, r.upgrade_prepaid, coalesce(r.unique_id, 0), coalesce(r.pinned, 0), " + giftColumns("g.")

func scanReceipt(row pgx.Row) (Receipt, error) {
	var r Receipt
	err := row.Scan(append([]any{&r.ID, &r.Sender, &r.Owner, &r.OwnerChannel, &r.MessageID, &r.ConvertStars, &r.Body, &r.HideName, &r.Displayed,
		&r.Date, &r.UpgradePrepaid, &r.UniqueID, &r.Pinned}, giftDest(&r.Gift)...)...)
	if r.Gift.UpgradeLeft < 0 {
		r.Gift.UpgradeLeft = 0
	}
	return r, err
}

const receiptFrom = " FROM gift_receipts r JOIN gift_catalog g ON g.id = r.gift_id"

// withUniques fills in the collectibles among list.
func (s *Service) withUniques(ctx context.Context, q rowsQuerier, list []Receipt) error {
	var ids []int64
	for _, r := range list {
		if r.UniqueID != 0 {
			ids = append(ids, r.UniqueID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := s.uniques(ctx, q, "u.id = ANY($1)", ids)
	if err != nil {
		return err
	}
	byID := map[int64]*Unique{}
	for i := range found {
		byID[found[i].ID] = &found[i]
	}
	for i := range list {
		if list[i].UniqueID != 0 {
			list[i].Unique = byID[list[i].UniqueID]
		}
	}
	return nil
}

type SavedQuery struct {
	ExcludeSaved   bool // displayed on the profile
	ExcludeUnsaved bool
	// what kind: a collectible, a limited or unlimited gift, one that can
	// or can't be upgraded
	ExcludeUnique        bool
	ExcludeUnlimited     bool
	ExcludeUpgradable    bool
	ExcludeNotUpgradable bool
	Offset               string
	Limit                int
}

// SavedGifts is savedGifts(): whoever manages the holder sees everything not
// converted, anyone else only what's on the profile. Newest first.
func (s *Service) SavedGifts(ctx context.Context, h Holder, manage bool, q SavedQuery) ([]Receipt, int, string, error) {
	col, id := h.where()
	where := col + "=$1 AND NOT r.converted AND ($2 OR r.displayed)"
	if q.ExcludeSaved {
		where += " AND NOT r.displayed"
	}
	if q.ExcludeUnsaved {
		where += " AND r.displayed"
	}
	if q.ExcludeUnique {
		where += " AND r.unique_id IS NULL"
	}
	if q.ExcludeUnlimited {
		where += " AND (r.unique_id IS NOT NULL OR g.stock_total IS NOT NULL OR g.stock IS NOT NULL)"
	}
	if q.ExcludeUpgradable {
		where += " AND (r.unique_id IS NOT NULL OR g.upgrade_set_id IS NULL)"
	}
	if q.ExcludeNotUpgradable {
		where += " AND (r.unique_id IS NOT NULL OR g.upgrade_set_id IS NOT NULL)"
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+receiptFrom+" WHERE "+where, id, manage).Scan(&count); err != nil {
		return nil, 0, "", err
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	before := int64(1<<63 - 1)
	if q.Offset != "" {
		n, err := strconv.ParseInt(q.Offset, 10, 64)
		if err != nil {
			return nil, 0, "", tgerr.New(400, "OFFSET_INVALID")
		}
		before = n
	}
	// pinned gifts lead the first page in their order; the pages after it
	// are the rest, newest first
	var list []Receipt
	if q.Offset == "" {
		rows, err := s.db.Query(ctx, "SELECT "+receiptCols+receiptFrom+" WHERE "+where+" AND r.pinned IS NOT NULL ORDER BY r.pinned, r.id DESC", id, manage)
		if err != nil {
			return nil, 0, "", err
		}
		if list, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Receipt, error) { return scanReceipt(r) }); err != nil {
			return nil, 0, "", err
		}
	}
	pinned := len(list)
	rows, err := s.db.Query(ctx, "SELECT "+receiptCols+receiptFrom+" WHERE "+where+
		" AND r.pinned IS NULL AND r.id < $3 ORDER BY r.id DESC LIMIT $4", id, manage, before, max(limit-pinned, 1)+1)
	if err != nil {
		return nil, 0, "", err
	}
	rest, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Receipt, error) { return scanReceipt(r) })
	if err != nil {
		return nil, 0, "", err
	}
	list = append(list, rest...)
	limit = pinned + max(limit-pinned, 1)
	next := ""
	if len(list) > limit {
		list = list[:limit]
		next = strconv.FormatInt(list[limit-1].ID, 10)
	}
	if err := s.withUniques(ctx, s.db, list); err != nil {
		return nil, 0, "", err
	}
	return list, count, next, nil
}

// HeldReceipt finds one of h's unconverted gifts by its receipt id or, with
// id 0, by the gift message in a person's box.
func (s *Service) HeldReceipt(ctx context.Context, h Holder, id int64, msgID int) (Receipt, error) {
	col, owner := h.where()
	r, err := scanReceipt(s.db.QueryRow(ctx, "SELECT "+receiptCols+receiptFrom+`
		WHERE `+col+`=$1 AND NOT r.converted AND (r.id=$2 OR $2 = 0 AND r.message_id=$3)`, owner, id, msgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errSavedGift
	}
	if err != nil {
		return r, err
	}
	list := []Receipt{r}
	err = s.withUniques(ctx, s.db, list)
	return list[0], err
}

// OwnedReceipt is HeldReceipt for a person.
func (s *Service) OwnedReceipt(ctx context.Context, owner, id int64, msgID int) (Receipt, error) {
	return s.HeldReceipt(ctx, Holder{User: owner}, id, msgID)
}

func (s *Service) DisplayGift(ctx context.Context, h Holder, receipt int64, displayed bool) error {
	col, owner := h.where()
	tag, err := s.db.Exec(ctx, "UPDATE gift_receipts r SET displayed=$3, pinned=CASE WHEN $3 THEN pinned END WHERE r.id=$1 AND "+col+"=$2 AND NOT r.converted", receipt, owner, displayed)
	if err == nil && tag.RowsAffected() == 0 {
		err = errSavedGift
	}
	return err
}

// PinLimit is how many gifts a profile pins to the top
// (stargifts_pinned_to_top_limit).
const PinLimit = 6

// PinGifts makes receipts, in order, h's pinned gifts, and unpins the rest.
// Each has to be h's, shown and a collectible, as only those get a pin.
func (s *Service) PinGifts(ctx context.Context, h Holder, receipts []int64) error {
	if len(receipts) > PinLimit {
		return tgerr.New(400, "STARGIFT_PIN_LIMIT_EXCEEDED")
	}
	col, owner := h.where()
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE gift_receipts r SET pinned=NULL WHERE "+col+"=$1 AND r.pinned IS NOT NULL", owner); err != nil {
			return err
		}
		for i, id := range receipts {
			tag, err := tx.Exec(ctx, "UPDATE gift_receipts r SET pinned=$3 WHERE r.id=$2 AND "+col+`=$1 AND NOT r.converted AND r.displayed
				AND r.unique_id IS NOT NULL`, owner, id, i+1)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errSavedGift
			}
		}
		return nil
	})
}

// ConvertGift trades the gift for its conversion value. The receipt id is
// the ledger reference, so converting twice can't pay twice. A collectible
// has no conversion value.
func (s *Service) ConvertGift(ctx context.Context, owner, receipt int64) (int64, error) {
	var bal int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var convert, giftID int64
		err := tx.QueryRow(ctx, "UPDATE gift_receipts SET converted=true WHERE id=$1 AND owner_id=$2 AND NOT converted AND unique_id IS NULL RETURNING convert_stars, gift_id", receipt, owner).Scan(&convert, &giftID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errSavedGift
		}
		if err != nil {
			return err
		}
		if convert > 0 {
			if err := Lock(ctx, tx, owner); err != nil {
				return err
			}
			sys, err := Account(ctx, tx, StarsAccount)
			if err != nil {
				return err
			}
			_, err = Post(ctx, tx, Entry{User: owner, Amount: convert, Reason: "gift_conversion", Reference: fmt.Sprintf("gift-conversion:%d", receipt),
				CreatedBy: owner, Peer: sys, Title: "Gift conversion", GiftID: giftID}, s.unix())
			if err != nil {
				return err
			}
		}
		bal, err = Balance(ctx, tx, owner)
		return err
	})
	return bal, err
}

// TransferGift hands a gift that isn't a collectible to another person; it
// lands hidden from their profile until they choose to show it.
func (s *Service) TransferGift(ctx context.Context, owner, receipt, to int64) error {
	if to == owner {
		return errUserInvalid
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := person(ctx, tx, to); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "UPDATE gift_receipts SET owner_id=$3, displayed=false, message_id=NULL WHERE id=$1 AND owner_id=$2 AND NOT converted AND unique_id IS NULL", receipt, owner, to)
		if err == nil && tag.RowsAffected() == 0 {
			err = errSavedGift
		}
		return err
	})
}

// GiftCount is how many gifts a profile shows.
func (s *Service) GiftCount(ctx context.Context, h Holder) (int, error) {
	col, owner := h.where()
	var n int
	err := s.db.QueryRow(ctx, "SELECT count(*) FROM gift_receipts r WHERE "+col+"=$1 AND r.displayed AND NOT r.converted", owner).Scan(&n)
	return n, err
}
