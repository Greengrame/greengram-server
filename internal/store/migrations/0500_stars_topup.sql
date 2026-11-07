-- Buying Stars from an app's top-up screen: payments.getPaymentForm makes a
-- stars_topup form whose page (/pay/stars/<pay_token>) quotes the price in
-- a Wallet asset, and payments.sendPaymentForm pays that quote. A quote made
-- for a form can only pay that form.
ALTER TABLE payment_forms DROP CONSTRAINT payment_forms_kind_check;
ALTER TABLE payment_forms ADD CONSTRAINT payment_forms_kind_check CHECK (kind IN ('premium', 'star_gift', 'stars_gift', 'giveaway', 'stars_giveaway',
    'star_gift_upgrade', 'star_gift_transfer', 'star_gift_resale', 'stars_topup'));
ALTER TABLE payment_forms ADD COLUMN pay_token text UNIQUE;

ALTER TABLE crypto_stars_quotes ADD COLUMN form_id bigint REFERENCES payment_forms(id) ON DELETE CASCADE;
CREATE INDEX crypto_stars_quotes_form ON crypto_stars_quotes(form_id) WHERE form_id IS NOT NULL;
