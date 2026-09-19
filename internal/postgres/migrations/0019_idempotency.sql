-- What an idempotency key adds to a payment: the key a merchant sent with
-- the request that opened it, and the SHA-256 of the body that key arrived
-- with. The body itself is not kept; the hash is what says a retry is the
-- same request. No second table: a key belongs to one payment and has no
-- reason to outlive it.
alter table payments
    add column idempotency_key       text,
    add column idempotency_body_hash bytea,
    add constraint payments_idempotency_names_its_body check (
        (idempotency_key is null) = (idempotency_body_hash is null)
    );

-- What makes a retry find the first payment rather than open a second. The
-- account is part of it because the key is a value the merchant chooses, and
-- two accounts may choose the same one.
create unique index payments_by_idempotency_key on payments (account_id, idempotency_key)
    where idempotency_key is not null;
