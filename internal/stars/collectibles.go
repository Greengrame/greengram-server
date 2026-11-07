package stars

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Collectible gifts (core.telegram.org/api/gifts#collectible-gifts). A
// catalog gift marked upgradable turns into a numbered piece of an imported
// collection: a model, a pattern and a backdrop drawn by their rarity. What
// an upgrade or a transfer costs goes to the gifts account, so the ledger
// adds up to the same with or without them.

var (
	GiftsAccount = System{"gift_upgrades", "instance_gifts", "Gifts"}

	errUpgradeUnavailable = tgerr.New(400, "STARGIFT_UPGRADE_UNAVAILABLE")
	errAlreadyUpgraded    = tgerr.New(400, "STARGIFT_ALREADY_UPGRADED")
	errUpgradePrepaid     = tgerr.New(400, "STARGIFT_UPGRADE_PREPAID")
	errPaymentRequired    = tgerr.New(400, "PAYMENT_REQUIRED")
	errNotUnique          = tgerr.New(400, "STARGIFT_NOT_UNIQUE")
	errUniqueNotFound     = tgerr.New(400, "STARGIFT_SLUG_INVALID")
	errTransferNoFee      = tgerr.New(400, "STARGIFT_TRANSFER_FREE")
	errSamePeer           = tgerr.New(400, "PEER_ID_INVALID")
)

// ErrUniqueNotFound is getUniqueStarGift's answer for a slug nobody has.
var ErrUniqueNotFound = errUniqueNotFound

type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Set is an imported collection. Supply is how many may ever be issued; 0
// is no limit, numbers keep going up.
type Set struct {
	ID       int64
	Name     string
	Title    string
	Slug     string
	Original int64 // the gift's own art before upgrading; 0 for none
	Supply   int
	Issued   int
	// what the admin panel shows: how many of each there are
	Models, Patterns, Backdrops int
	GiftID                      int64 // the catalog gift that upgrades into it; 0 for none
}

// Attribute is a model or pattern: a sticker and how rare it is.
type Attribute struct {
	Name     string
	Document int64
	Permille int
}

type Backdrop struct {
	Name     string
	ID       int
	Center   int
	Edge     int
	Pattern  int
	Text     int
	Permille int
}

// Unique is one collectible.
type Unique struct {
	ID       int64
	SetID    int64
	GiftID   int64
	Title    string
	Num      int
	Slug     string
	Supply   int // 0: unlimited
	Issued   int
	Model    Attribute
	Pattern  Attribute
	Backdrop Backdrop
	// the gift as it was sent, when the upgrade kept it; Sender is 0 when
	// they hid their name
	Original          bool
	OriginalSender    int64
	OriginalRecipient int64
	// the channel it was given to, when that's who got it (OriginalRecipient is 0)
	OriginalChannel int64
	OriginalMessage string
	OriginalDate    int
	Created         int

	ReceiptID     int64
	Holder        Holder
	TransferStars int64 // its catalog gift's fee now
	PremiumOnly   bool
	// the model and pattern as custom emoji, for an emoji status
	ModelEmoji, PatternEmoji int64
	// listed for sale by its holder: the price, 0 when it isn't
	Resale      int64
	ResaleSince int
	// one of the instance's market offers: not minted, no number or holder
	Offer bool
}

const uniqueCols = `u.id, u.set_id, u.gift_id, s.title, u.num, u.slug, s.supply, s.issued,
	u.model, m.document_id, m.rarity_permille, u.pattern, p.document_id, p.rarity_permille,
	u.backdrop, b.backdrop_id, b.center_color, b.edge_color, b.pattern_color, b.text_color, b.rarity_permille,
	u.original, coalesce(u.original_sender_id, 0), coalesce(u.original_recipient_id, 0), coalesce(u.original_recipient_channel_id, 0), u.original_message, u.original_date, u.created_at,
	coalesce(r.id, 0), coalesce(r.owner_id, 0), coalesce(r.owner_channel_id, 0), g.transfer_stars, g.premium_only, m.emoji_document_id, p.emoji_document_id,
	coalesce(u.resale_stars, 0), u.resale_since
	FROM unique_gifts u JOIN collectible_sets s ON s.id = u.set_id
	JOIN collectible_models m ON m.set_id = u.set_id AND m.name = u.model
	JOIN collectible_patterns p ON p.set_id = u.set_id AND p.name = u.pattern
	JOIN collectible_backdrops b ON b.set_id = u.set_id AND b.name = u.backdrop
	JOIN gift_catalog g ON g.id = u.gift_id
	LEFT JOIN gift_receipts r ON r.unique_id = u.id`

func scanUnique(row pgx.Row) (Unique, error) {
	var u Unique
	b := &u.Backdrop
	err := row.Scan(&u.ID, &u.SetID, &u.GiftID, &u.Title, &u.Num, &u.Slug, &u.Supply, &u.Issued,
		&u.Model.Name, &u.Model.Document, &u.Model.Permille, &u.Pattern.Name, &u.Pattern.Document, &u.Pattern.Permille,
		&b.Name, &b.ID, &b.Center, &b.Edge, &b.Pattern, &b.Text, &b.Permille,
		&u.Original, &u.OriginalSender, &u.OriginalRecipient, &u.OriginalChannel, &u.OriginalMessage, &u.OriginalDate, &u.Created,
		&u.ReceiptID, &u.Holder.User, &u.Holder.Channel, &u.TransferStars, &u.PremiumOnly, &u.ModelEmoji, &u.PatternEmoji,
		&u.Resale, &u.ResaleSince)
	return u, err
}

func (s *Service) uniques(ctx context.Context, q rowsQuerier, where string, args ...any) ([]Unique, error) {
	rows, err := q.Query(ctx, "SELECT "+uniqueCols+" WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Unique, error) { return scanUnique(r) })
}

// UniqueBySlug is getUniqueStarGift: anyone may look a collectible up.
func (s *Service) UniqueBySlug(ctx context.Context, slug string) (Unique, error) {
	list, err := s.uniques(ctx, s.db, "lower(u.slug) = lower($1)", slug)
	if err != nil {
		return Unique{}, err
	}
	if len(list) == 0 {
		return Unique{}, errUniqueNotFound
	}
	return list[0], nil
}

// UniqueByID finds a collectible by its id (an emoji status names it so).
func (s *Service) UniqueByID(ctx context.Context, id int64) (Unique, error) {
	list, err := s.uniques(ctx, s.db, "u.id = $1", id)
	if err != nil {
		return Unique{}, err
	}
	if len(list) == 0 {
		return Unique{}, errUniqueNotFound
	}
	return list[0], nil
}

// UniquesHeldBy are the collectibles a person holds, newest first.
func (s *Service) UniquesHeldBy(ctx context.Context, user int64) ([]Unique, error) {
	return s.uniques(ctx, s.db, "r.owner_id = $1 AND NOT r.converted ORDER BY r.id DESC", user)
}

// ReceiptBySlug is the receipt holding the collectible with this slug.
func (s *Service) ReceiptBySlug(ctx context.Context, slug string) (Receipt, error) {
	u, err := s.UniqueBySlug(ctx, slug)
	if err != nil {
		return Receipt{}, errSavedGift
	}
	if u.ReceiptID == 0 {
		return Receipt{}, errSavedGift
	}
	return s.HeldReceipt(ctx, u.Holder, u.ReceiptID, 0)
}

// Sets are the imported collections, with what upgrades into each.
func (s *Service) Sets(ctx context.Context) ([]Set, error) {
	rows, err := s.db.Query(ctx, `SELECT s.id, s.name, s.title, s.slug, coalesce(s.original_document_id, 0), s.supply, s.issued,
		(SELECT count(*) FROM collectible_models x WHERE x.set_id = s.id),
		(SELECT count(*) FROM collectible_patterns x WHERE x.set_id = s.id),
		(SELECT count(*) FROM collectible_backdrops x WHERE x.set_id = s.id),
		coalesce((SELECT g.id FROM gift_catalog g WHERE g.upgrade_set_id = s.id), 0)
		FROM collectible_sets s ORDER BY s.title, s.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Set, error) {
		var x Set
		err := r.Scan(&x.ID, &x.Name, &x.Title, &x.Slug, &x.Original, &x.Supply, &x.Issued, &x.Models, &x.Patterns, &x.Backdrops, &x.GiftID)
		return x, err
	})
}

// Attributes are a collection's models, patterns and backdrops, rarest first.
func (s *Service) Attributes(ctx context.Context, set int64) ([]Attribute, []Attribute, []Backdrop, error) {
	return attributes(ctx, s.db, set)
}

func attributes(ctx context.Context, q rowsQuerier, set int64) ([]Attribute, []Attribute, []Backdrop, error) {
	read := func(table string) ([]Attribute, error) {
		rows, err := q.Query(ctx, "SELECT name, document_id, rarity_permille FROM "+table+" WHERE set_id=$1 ORDER BY rarity_permille, name", set)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Attribute, error) {
			var a Attribute
			err := r.Scan(&a.Name, &a.Document, &a.Permille)
			return a, err
		})
	}
	models, err := read("collectible_models")
	if err != nil {
		return nil, nil, nil, err
	}
	patterns, err := read("collectible_patterns")
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := q.Query(ctx, `SELECT name, backdrop_id, center_color, edge_color, pattern_color, text_color, rarity_permille
		FROM collectible_backdrops WHERE set_id=$1 ORDER BY rarity_permille, name`, set)
	if err != nil {
		return nil, nil, nil, err
	}
	backdrops, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Backdrop, error) {
		var b Backdrop
		err := r.Scan(&b.Name, &b.ID, &b.Center, &b.Edge, &b.Pattern, &b.Text, &b.Permille)
		return b, err
	})
	return models, patterns, backdrops, err
}

// UpgradeSettings is how the admin panel makes a gift upgradable: Set 0
// makes it plain again. Supply 0 takes the collection's limit off.
type UpgradeSettings struct {
	Set           int64
	Stars         int64
	TransferStars int64
	Supply        int
}

var (
	errSetTaken    = &account.Error{Status: 400, Message: "Another gift already upgrades into that collection."}
	errSetNotFound = &account.Error{Status: 404, Message: "No such collection."}
	errSupplyLow   = &account.Error{Status: 400, Message: "The supply can't be less than what's issued plus the upgrades already paid for."}
	errPrepaidWait = &account.Error{Status: 400, Message: "Upgrades already paid for are waiting on this collection."}
)

// SetGiftUpgrade saves a gift's upgrade settings and its collection's supply.
func (s *Service) SetGiftUpgrade(ctx context.Context, gift int64, u UpgradeSettings) error {
	if u.Set != 0 && (u.Stars < 1 || u.Stars > MaxBalance || u.TransferStars < 0 || u.TransferStars > MaxBalance || u.Supply < 0) {
		return errGiftInvalid
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var old *int64
		err := tx.QueryRow(ctx, "SELECT upgrade_set_id FROM gift_catalog WHERE id=$1 FOR UPDATE", gift).Scan(&old)
		if errors.Is(err, pgx.ErrNoRows) {
			return errGiftNotFound
		}
		if err != nil {
			return err
		}
		var waiting int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM gift_receipts WHERE gift_id=$1 AND upgrade_prepaid > 0 AND unique_id IS NULL AND NOT converted", gift).Scan(&waiting); err != nil {
			return err
		}
		// a prepaid upgrade was promised this collection
		if waiting > 0 && old != nil && *old != u.Set {
			return errPrepaidWait
		}
		if u.Set == 0 {
			_, err := tx.Exec(ctx, "UPDATE gift_catalog SET upgrade_set_id=NULL, upgrade_stars=NULL, transfer_stars=0 WHERE id=$1", gift)
			return err
		}
		var issued int
		var other *int64
		err = tx.QueryRow(ctx, "SELECT issued, (SELECT id FROM gift_catalog WHERE upgrade_set_id=$1 AND id<>$2) FROM collectible_sets WHERE id=$1 FOR UPDATE", u.Set, gift).Scan(&issued, &other)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errSetNotFound
		case err != nil:
			return err
		case other != nil:
			return errSetTaken
		case u.Supply != 0 && u.Supply < issued+waiting:
			return errSupplyLow
		}
		if _, err := tx.Exec(ctx, "UPDATE collectible_sets SET supply=$2 WHERE id=$1", u.Set, u.Supply); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE gift_catalog SET upgrade_set_id=$2, upgrade_stars=$3, transfer_stars=$4 WHERE id=$1", gift, u.Set, u.Stars, u.TransferStars)
		return err
	})
}

// lockSet takes g's collection row and reads g's supply left under it, so
// two buyers or upgraders can't both take the last number. The lock also
// keeps an unlimited collection's numbers in order.
func (s *Service) lockSet(ctx context.Context, tx pgx.Tx, g Gift) (Gift, error) {
	var supply, issued, waiting int
	err := tx.QueryRow(ctx, "SELECT supply, issued FROM collectible_sets WHERE id=$1 FOR UPDATE", g.UpgradeSet).Scan(&supply, &issued)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, errUpgradeUnavailable
	}
	if err != nil {
		return g, err
	}
	g.UpgradeUnlimited = supply == 0
	if g.UpgradeUnlimited {
		g.UpgradeLeft = 0
		return g, nil
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM gift_receipts WHERE gift_id=$1 AND upgrade_prepaid > 0 AND unique_id IS NULL AND NOT converted", g.ID).Scan(&waiting); err != nil {
		return g, err
	}
	g.UpgradeLeft = max(supply-issued-waiting, 0)
	return g, nil
}

// move takes amount from user to the gifts account: two rows, one
// reference each, so a retried payment hits the unique index.
func move(ctx context.Context, tx pgx.Tx, user, sys, amount int64, reason, ref string, g Gift, now int) error {
	if _, err := Post(ctx, tx, Entry{User: user, Amount: -amount, Reason: reason, Reference: ref, CreatedBy: user, Peer: sys, Title: g.Title, GiftID: g.ID}, now); err != nil {
		return err
	}
	_, err := Post(ctx, tx, Entry{User: sys, Amount: amount, Reason: reason, Reference: ref + ":in", CreatedBy: user, Peer: user, Title: g.Title, GiftID: g.ID}, now)
	return err
}

// heldForUpdate locks a receipt with its gift row read.
func heldForUpdate(ctx context.Context, tx pgx.Tx, id int64) (Receipt, error) {
	r, err := scanReceipt(tx.QueryRow(ctx, "SELECT "+receiptCols+receiptFrom+" WHERE r.id=$1 FOR UPDATE OF r", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errSavedGift
	}
	return r, err
}

// UpgradeForm prices upgrading one of h's gifts, paid by payer: the owner,
// or an admin for a channel's.
func (s *Service) UpgradeForm(ctx context.Context, payer int64, h Holder, receipt int64, keep bool) (Form, Gift, error) {
	r, err := s.HeldReceipt(ctx, h, receipt, 0)
	switch {
	case err != nil:
		return Form{}, r.Gift, err
	case r.UniqueID != 0:
		return Form{}, r.Gift, errAlreadyUpgraded
	case r.UpgradePrepaid > 0:
		return Form{}, r.Gift, errUpgradePrepaid
	case r.Gift.UpgradeSet == 0:
		return Form{}, r.Gift, errUpgradeUnavailable
	case !r.Gift.Upgradable():
		return Form{}, r.Gift, errGiftSoldOut
	}
	f, err := s.issue(ctx, Form{Kind: KindGiftUpgrade, User: payer, Recipient: payer, Channel: h.Channel, GiftID: r.Gift.ID, Stars: r.Gift.UpgradeStars,
		ReceiptID: r.ID, KeepDetails: keep})
	return f, r.Gift, err
}

// UpgradeResult is a gift that became a collectible.
type UpgradeResult struct {
	Form    Form // empty for an upgrade paid in advance
	Receipt Receipt
	Unique  Unique
	Balance int64
	Paid    int64
}

// PayUpgrade is sendStarsForm for an upgrade form. h is who the payer may
// act for (themselves, or a channel they manage); the gift has to be h's
// and the form for h.
func (s *Service) PayUpgrade(ctx context.Context, payer, formID int64, h Holder) (UpgradeResult, error) {
	var out UpgradeResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, payer, formID, KindGiftUpgrade)
		if err != nil {
			return err
		}
		out.Form = f
		if f.Channel != h.Channel || h.Channel == 0 && h.User != payer {
			return errSavedGift
		}
		return s.upgrade(ctx, tx, payer, h, f.ReceiptID, f.KeepDetails, f.Stars, &out)
	})
	return out, err
}

// Upgrade is payments.upgradeStarGift: only for an upgrade someone already
// paid for, the rest goes through a form. actor acts for h.
func (s *Service) Upgrade(ctx context.Context, actor int64, h Holder, receipt int64, keep bool) (UpgradeResult, error) {
	var out UpgradeResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.upgrade(ctx, tx, actor, h, receipt, keep, 0, &out)
	})
	return out, err
}

// upgrade turns h's gift into a collectible; payer pays price for it.
func (s *Service) upgrade(ctx context.Context, tx pgx.Tx, payer int64, h Holder, receipt int64, keep bool, price int64, out *UpgradeResult) error {
	r, err := heldForUpdate(ctx, tx, receipt)
	switch {
	case err != nil:
		return err
	case h.IsZero() || r.Holder() != h || r.Gift.ID == 0:
		return errSavedGift
	case r.UniqueID != 0:
		return errAlreadyUpgraded
	case r.Gift.UpgradeSet == 0:
		return errUpgradeUnavailable
	case price > 0 && r.UpgradePrepaid > 0:
		return errUpgradePrepaid
	case price == 0 && r.UpgradePrepaid == 0:
		return errPaymentRequired
	}
	// converted gifts are gone from HeldReceipt but not from a lock
	var converted bool
	if err := tx.QueryRow(ctx, "SELECT converted FROM gift_receipts WHERE id=$1", r.ID).Scan(&converted); err != nil || converted {
		return errSavedGift
	}
	g, err := s.lockSet(ctx, tx, r.Gift)
	if err != nil {
		return err
	}
	// a prepaid upgrade has its number held; anyone else needs one free
	if price > 0 && !g.Upgradable() {
		return errGiftSoldOut
	}
	var set Set
	err = tx.QueryRow(ctx, "UPDATE collectible_sets SET issued = issued + 1 WHERE id=$1 AND (supply = 0 OR issued < supply) RETURNING issued, slug, title",
		g.UpgradeSet).Scan(&set.Issued, &set.Slug, &set.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return errGiftSoldOut
	}
	if err != nil {
		return err
	}
	models, patterns, backdrops, err := attributes(ctx, tx, g.UpgradeSet)
	if err != nil {
		return err
	}
	if len(models) == 0 || len(patterns) == 0 || len(backdrops) == 0 {
		return errUpgradeUnavailable
	}
	now := s.unix()
	if price > 0 {
		sys, err := Account(ctx, tx, GiftsAccount)
		if err != nil {
			return err
		}
		if err := Lock(ctx, tx, payer, sys); err != nil {
			return err
		}
		err = move(ctx, tx, payer, sys, price, "gift_upgrade", fmt.Sprintf("gift-upgrade:%d", r.ID), g, now)
		if errors.Is(err, ErrDuplicate) {
			return errAlreadyUpgraded
		}
		if err != nil {
			return err
		}
		out.Paid = price
	}
	model := models[pick(len(models), func(i int) int { return models[i].Permille })]
	pattern := patterns[pick(len(patterns), func(i int) int { return patterns[i].Permille })]
	backdrop := backdrops[pick(len(backdrops), func(i int) int { return backdrops[i].Permille })]
	id := randomID()
	slug := set.Slug + "-" + strconv.Itoa(set.Issued)
	var sender, recipient, channel *int64
	msg, date := "", 0
	if keep {
		if !r.HideName {
			sender = &r.Sender
		}
		if r.OwnerChannel != 0 {
			channel = &r.OwnerChannel
		} else {
			recipient = &r.Owner
		}
		msg, date = r.Body, r.Date
	}
	if _, err := tx.Exec(ctx, `INSERT INTO unique_gifts(id, set_id, gift_id, num, slug, model, pattern, backdrop, original,
			original_sender_id, original_recipient_id, original_recipient_channel_id, original_message, original_date, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		id, g.UpgradeSet, g.ID, set.Issued, slug, model.Name, pattern.Name, backdrop.Name, keep, sender, recipient, channel, msg, date, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE gift_receipts SET unique_id=$2 WHERE id=$1", r.ID, id); err != nil {
		return err
	}
	list, err := s.uniques(ctx, tx, "u.id = $1", id)
	if err != nil {
		return err
	}
	if len(list) != 1 {
		return errors.New("stars: upgraded gift vanished")
	}
	out.Unique = list[0]
	r.UniqueID, r.Unique = id, &out.Unique
	out.Receipt = r
	out.Balance, err = Balance(ctx, tx, payer)
	return err
}

// pick draws an index by weight. Rarity is in permille, so the draw is too.
func pick(n int, weight func(int) int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += weight(i)
	}
	if total <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		panic(err)
	}
	x := int(v.Int64())
	for i := 0; i < n; i++ {
		if x -= weight(i); x < 0 {
			return i
		}
	}
	return n - 1
}

func randomID() int64 {
	var b [8]byte
	rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:])>>1) | 1<<40
}

// TransferForm prices moving a collectible from h to to. The caller has
// checked that the payer may act for h and that to may receive it.
func (s *Service) TransferForm(ctx context.Context, payer int64, h Holder, receipt int64, to Holder) (Form, Gift, error) {
	r, err := s.HeldReceipt(ctx, h, receipt, 0)
	switch {
	case err != nil:
		return Form{}, r.Gift, err
	case r.UniqueID == 0:
		return Form{}, r.Gift, errNotUnique
	case to == h || to.IsZero():
		return Form{}, r.Gift, errSamePeer
	case r.Gift.TransferStars == 0:
		return Form{}, r.Gift, errTransferNoFee
	case r.Unique != nil && r.Unique.Resale > 0:
		return Form{}, r.Gift, errListed
	}
	f := Form{Kind: KindGiftTransfer, User: payer, Recipient: to.User, Channel: to.Channel, GiftID: r.Gift.ID, Stars: r.Gift.TransferStars, ReceiptID: r.ID}
	if to.Channel != 0 {
		f.Recipient = payer
	}
	f, err = s.issue(ctx, f)
	return f, r.Gift, err
}

// GiftTransfer is a collectible that changed hands.
type GiftTransfer struct {
	Form    Form
	From    Holder
	To      Holder
	Receipt Receipt
	Unique  Unique
	Balance int64
	Paid    int64
	// the old owner wore it as their emoji status, which is gone now
	StatusCleared bool
}

// PayTransfer is sendStarsForm for a transfer form. from is who the payer
// may act for; the gift has to still be theirs.
func (s *Service) PayTransfer(ctx context.Context, payer, formID int64, from Holder) (GiftTransfer, error) {
	var out GiftTransfer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, payer, formID, KindGiftTransfer)
		if err != nil {
			return err
		}
		out.Form = f
		to := Holder{User: f.Recipient}
		if f.Channel != 0 {
			to = Holder{Channel: f.Channel}
		}
		return s.transfer(ctx, tx, payer, from, f.ReceiptID, to, f.Stars, f.ID, &out)
	})
	return out, err
}

// TransferUnique is payments.transferStarGift for a collectible: free ones
// only, a fee goes through a form.
func (s *Service) TransferUnique(ctx context.Context, actor int64, from Holder, receipt int64, to Holder) (GiftTransfer, error) {
	var out GiftTransfer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.transfer(ctx, tx, actor, from, receipt, to, 0, 0, &out)
	})
	return out, err
}

func (s *Service) transfer(ctx context.Context, tx pgx.Tx, actor int64, from Holder, receipt int64, to Holder, fee, form int64, out *GiftTransfer) error {
	r, err := heldForUpdate(ctx, tx, receipt)
	switch {
	case err != nil:
		return err
	case r.Holder() != from:
		return errSavedGift
	case r.UniqueID == 0:
		return errNotUnique
	case to == from || to.IsZero():
		return errSamePeer
	case fee == 0 && r.Gift.TransferStars > 0:
		return errPaymentRequired
	}
	var listed bool
	if err := tx.QueryRow(ctx, "SELECT resale_stars IS NOT NULL FROM unique_gifts WHERE id=$1 FOR UPDATE", r.UniqueID).Scan(&listed); err != nil {
		return err
	}
	if listed {
		return errListed
	}
	if to.User != 0 {
		if _, err := person(ctx, tx, to.User); err != nil {
			return err
		}
	} else {
		var broadcast bool
		err := tx.QueryRow(ctx, "SELECT NOT megagroup FROM channels WHERE id=$1 AND NOT deleted", to.Channel).Scan(&broadcast)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !broadcast {
			return errSamePeer
		}
		if err != nil {
			return err
		}
	}
	now := s.unix()
	if fee > 0 {
		sys, err := Account(ctx, tx, GiftsAccount)
		if err != nil {
			return err
		}
		if err := Lock(ctx, tx, actor, sys); err != nil {
			return err
		}
		if err := move(ctx, tx, actor, sys, fee, "gift_transfer", fmt.Sprintf("gift-transfer:%d", form), r.Gift, now); err != nil {
			return err
		}
		out.Paid = fee
	}
	var owner, channel *int64
	if to.User != 0 {
		owner = &to.User
	} else {
		channel = &to.Channel
	}
	if _, err := tx.Exec(ctx, `UPDATE gift_receipts SET owner_id=$2, owner_channel_id=$3, sender_id=$4, hide_name=false, body='', displayed=true,
		message_id=NULL, created_at=$5, pinned=NULL WHERE id=$1`, r.ID, owner, channel, actor, now); err != nil {
		return err
	}
	if from.User != 0 {
		tag, err := tx.Exec(ctx, "UPDATE users SET emoji_status_collectible=NULL WHERE id=$1 AND emoji_status_collectible=$2", from.User, r.UniqueID)
		if err != nil {
			return err
		}
		out.StatusCleared = tag.RowsAffected() > 0
	}
	list, err := s.uniques(ctx, tx, "u.id = $1", r.UniqueID)
	if err != nil {
		return err
	}
	if len(list) != 1 {
		return errors.New("stars: transferred gift vanished")
	}
	out.From, out.To, out.Unique = from, to, list[0]
	r.Owner, r.OwnerChannel, r.Sender, r.HideName, r.Body, r.Displayed, r.MessageID, r.Date = to.User, to.Channel, actor, false, "", true, 0, now
	r.Unique = &out.Unique
	out.Receipt = r
	out.Balance, err = Balance(ctx, tx, actor)
	return err
}

// FormReceipt is the gift an unpaid upgrade or transfer form of user's is for.
func (s *Service) FormReceipt(ctx context.Context, user, id int64) (int64, error) {
	var receipt *int64
	err := s.db.QueryRow(ctx, "SELECT receipt_id FROM payment_forms WHERE id=$1 AND user_id=$2", id, user).Scan(&receipt)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && receipt == nil {
		return 0, errFormInvalid
	}
	if err != nil {
		return 0, err
	}
	return *receipt, nil
}

// ReceiptHolder says who holds a gift now.
func (s *Service) ReceiptHolder(ctx context.Context, id int64) (Holder, error) {
	var h Holder
	err := s.db.QueryRow(ctx, "SELECT coalesce(owner_id, 0), coalesce(owner_channel_id, 0) FROM gift_receipts WHERE id=$1 AND NOT converted", id).Scan(&h.User, &h.Channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, errSavedGift
	}
	return h, err
}

// SetGiftDefaults is what a catalog gift made for a collection starts with
// (GiftsForSets). Supply limits a collection that has no limit yet; 0
// leaves it unlimited. OffSale keeps the gifts out of the catalog until an
// admin turns them on.
type SetGiftDefaults struct {
	Stars, ConvertStars, UpgradeStars, TransferStars int64
	Supply                                           int
	OffSale                                          bool
	NoVariants                                       bool // plain gifts, no "sell as variants"
}

// SetGifts is what GiftsForSets did, by collection title.
type SetGifts struct {
	Created []string
	Skipped []string // a gift with the title is already in the catalog
}

// GiftsForSets makes a catalog gift for every imported collection no gift
// upgrades into yet: the collection's own art and title, upgradable into it
// and on sale. A collection with a supply limit gets a gift with as many in
// stock as it has numbers left; one without gets an unlimited gift.
// Running it again changes nothing.
func (s *Service) GiftsForSets(ctx context.Context, d SetGiftDefaults) (SetGifts, error) {
	var out SetGifts
	if d.Stars < 1 || d.Stars > MaxBalance || d.ConvertStars < 0 || d.ConvertStars > MaxBalance || d.UpgradeStars < 1 || d.UpgradeStars > MaxBalance ||
		d.TransferStars < 0 || d.TransferStars > MaxBalance || d.Supply < 0 || d.ConvertStars > d.Stars {
		return out, errGiftInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM collectible_sets s WHERE NOT EXISTS (SELECT 1 FROM gift_catalog g WHERE g.upgrade_set_id = s.id) ORDER BY title, id`)
	if err != nil {
		return out, err
	}
	sets, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return out, err
	}
	for _, id := range sets {
		err := s.tx(ctx, func(tx pgx.Tx) error {
			var title string
			var art *int64
			var supply, issued int
			err := tx.QueryRow(ctx, "SELECT title, original_document_id, supply, issued FROM collectible_sets WHERE id=$1 FOR UPDATE", id).Scan(&title, &art, &supply, &issued)
			if err != nil {
				return err
			}
			var taken bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM gift_catalog WHERE upgrade_set_id=$1 OR lower(title)=lower($2))", id, title).Scan(&taken); err != nil {
				return err
			}
			if taken {
				out.Skipped = append(out.Skipped, title)
				return nil
			}
			if supply == 0 && d.Supply > 0 {
				supply = max(d.Supply, issued)
				if _, err := tx.Exec(ctx, "UPDATE collectible_sets SET supply=$2 WHERE id=$1", id, supply); err != nil {
					return err
				}
			}
			var stock, total *int
			if supply > 0 {
				left := max(supply-issued, 0)
				stock, total = &left, &supply
			}
			var giftID int64
			if err := tx.QueryRow(ctx, `INSERT INTO gift_catalog(title, emoji, stars, convert_stars, stock, stock_total, enabled, sticker_id,
					upgrade_set_id, upgrade_stars, transfer_stars)
				VALUES ($1, '🎁', $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
				title, d.Stars, d.ConvertStars, stock, total, !d.OffSale, art, id, d.UpgradeStars, d.TransferStars).Scan(&giftID); err != nil {
				return err
			}
			// collection gifts are bought as a picked variant unless asked otherwise
			if !d.NoVariants {
				if _, err := tx.Exec(ctx, `INSERT INTO gift_market(gift_id, enabled) VALUES ($1, true) ON CONFLICT (gift_id) DO NOTHING`, giftID); err != nil {
					return err
				}
			}
			out.Created = append(out.Created, title)
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
