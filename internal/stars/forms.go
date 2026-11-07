package stars

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

const (
	KindPremium   = "premium"
	KindStarGift  = "star_gift"
	KindStarsGift = "stars_gift"
	// a collectible: upgrading a gift, moving one to someone else
	KindGiftUpgrade  = "star_gift_upgrade"
	KindGiftTransfer = "star_gift_transfer"
	// a collectible off the market: a listing or one of the instance's offers
	KindGiftResale = "star_gift_resale"
)

// formTTL is how long the web's quotes held a price.
const formTTL = 300

// Form is a priced purchase waiting to be paid: the quote of the old API,
// the form_id of payments.getPaymentForm.
type Form struct {
	ID        int64
	Kind      string
	User      int64
	Recipient int64
	Months    int
	Days      int
	GiftID    int64
	Stars     int64
	Message   string
	HideName  bool
	ExpiresAt int

	// upgrades and transfers: the gift, and where a transfer goes when it's
	// a channel (Recipient is the payer then)
	ReceiptID   int64
	Channel     int64
	KeepDetails bool
	// a gift bought with its upgrade: the part of Stars that pays for it
	UpgradeStars int64

	// a resale: the collectible's slug, and for a listing the gift and who
	// was selling it when the form was made
	Slug     string
	UniqueID int64
	Seller   int64

	// a top-up: the secret in its page's address
	PayToken string
}

var (
	errFormInvalid   = tgerr.New(400, "FORM_ID_EMPTY")
	errFormExpired   = tgerr.New(400, "FORM_EXPIRED")
	errFormDuplicate = tgerr.New(400, "FORM_SUBMIT_DUPLICATE")
)

func (s *Service) issue(ctx context.Context, f Form) (Form, error) {
	var b [8]byte
	rand.Read(b[:])
	f.ID = int64(binary.LittleEndian.Uint64(b[:]) >> 1)
	f.ExpiresAt = s.unix() + formTTL
	var gift, receipt, channel, unique, seller *int64
	if f.GiftID != 0 {
		gift = &f.GiftID
	}
	if f.ReceiptID != 0 {
		receipt = &f.ReceiptID
	}
	if f.Channel != 0 {
		channel = &f.Channel
	}
	if f.UniqueID != 0 {
		unique = &f.UniqueID
	}
	if f.Seller != 0 {
		seller = &f.Seller
	}
	var token *string
	if f.PayToken != "" {
		token = &f.PayToken
	}
	// paid forms stay as receipts; only unpaid ones are litter
	if _, err := s.db.Exec(ctx, "DELETE FROM payment_forms WHERE NOT paid AND expires_at <= $1", s.unix()); err != nil {
		return f, err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO payment_forms(id, user_id, kind, recipient_id, months, days, gift_id, stars, message, hide_name, expires_at,
			receipt_id, channel_id, keep_details, upgrade_stars, slug, unique_id, seller_id, pay_token)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
		f.ID, f.User, f.Kind, f.Recipient, f.Months, f.Days, gift, f.Stars, f.Message, f.HideName, f.ExpiresAt,
		receipt, channel, f.KeepDetails, f.UpgradeStars, f.Slug, unique, seller, token)
	return f, err
}

// claim locks the buyer's unpaid form of this kind and marks it paid; the
// caller's transaction rolls that back if the payment fails.
func (s *Service) claim(ctx context.Context, tx pgx.Tx, user, id int64, kind string) (Form, error) {
	return ClaimForm(ctx, tx, user, id, kind, s.unix())
}

// ClaimForm is claim for a form another package settles in its own
// transaction (the wallet pays top-ups); now is that package's clock.
func ClaimForm(ctx context.Context, tx pgx.Tx, user, id int64, kind string, now int) (Form, error) {
	var f Form
	var gift *int64
	var paid bool
	err := tx.QueryRow(ctx, `SELECT id, kind, user_id, recipient_id, months, days, gift_id, stars, message, hide_name, expires_at, paid,
			coalesce(receipt_id, 0), coalesce(channel_id, 0), keep_details, upgrade_stars, slug, coalesce(unique_id, 0), coalesce(seller_id, 0)
		FROM payment_forms WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).
		Scan(&f.ID, &f.Kind, &f.User, &f.Recipient, &f.Months, &f.Days, &gift, &f.Stars, &f.Message, &f.HideName, &f.ExpiresAt, &paid,
			&f.ReceiptID, &f.Channel, &f.KeepDetails, &f.UpgradeStars, &f.Slug, &f.UniqueID, &f.Seller)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return f, errFormInvalid
	case err != nil:
		return f, err
	case f.Kind != kind:
		return f, errFormInvalid
	case paid:
		return f, errFormDuplicate
	case f.ExpiresAt <= now:
		return f, errFormExpired
	}
	if gift != nil {
		f.GiftID = *gift
	}
	_, err = tx.Exec(ctx, "UPDATE payment_forms SET paid=true WHERE id=$1", f.ID)
	return f, err
}

// SystemUser is Account outside a transaction, for rendering a peer.
func (s *Service) SystemUser(ctx context.Context, sys System) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		id, err = Account(ctx, tx, sys)
		return err
	})
	return id, err
}

// FormKind says which payment a form id belongs to, for sendStarsForm
// calls whose invoice doesn't match what getPaymentForm was asked.
func (s *Service) FormKind(ctx context.Context, user, id int64) (string, error) {
	var kind string
	err := s.db.QueryRow(ctx, "SELECT kind FROM payment_forms WHERE id=$1 AND user_id=$2", id, user).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errFormInvalid
	}
	return kind, err
}
