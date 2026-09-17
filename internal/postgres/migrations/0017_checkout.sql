-- What suco Checkout adds to a payment: the token its page is reached by,
-- kept as a hash with the key that derived it; where the payer is sent back
-- to; and when the payment ended, which is what the token's life is counted
-- from.
alter table payments
    add column checkout_hash   bytea,
    add column checkout_key_id text,
    add column return_url      text,
    add column closed_at       timestamptz,
    add constraint payments_checkout_names_its_key check (
        (checkout_hash is null) = (checkout_key_id is null)
    );

-- The token is what a payer's request carries, and this is how the payment
-- is found from it.
create unique index payments_by_checkout_hash on payments (checkout_hash) where checkout_hash is not null;
