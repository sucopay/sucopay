-- A refund is a payment with the direction reversed: the merchant signs a
-- transfer of what arrived back to the wallet it arrived from, and the same
-- observation of the chain closes it. The row holds the amount, where it
-- goes, the key the authorisation spends, and the page's token.
create table refunds (
    account_id uuid not null,
    payment_id text not null,
    id         text not null,

    -- What to send back, in the asset's smallest unit, and where. The
    -- destination is the from of the transfer that paid the payment, copied
    -- here when the refund is made so that a later reading of the chain
    -- cannot move where a signature sends money.
    amount      numeric(78, 0) not null,
    destination text           not null,

    -- How a transfer is recognised as this refund's, and the network it is
    -- spent on, the way an attempt holds both.
    scheme  text not null,
    network text not null,
    key     text not null,

    -- The moment the authorisation stops being good, and the state the
    -- refund is in.
    expires_at timestamptz not null,
    status     text        not null,

    -- The page the merchant signs on is reached by a token, kept as a hash
    -- with the key that derived it, as a payment's checkout page is.
    token_hash   bytea not null,
    token_key_id text  not null,

    -- The idempotency key the request carried, and the hash of the body it
    -- arrived with. Both or neither, as a payment's are.
    idempotency_key       text,
    idempotency_body_hash bytea,

    created_at timestamptz not null,
    closed_at  timestamptz,
    -- Concurrent writes to one refund are resolved here, as a payment's
    -- version resolves its own.
    version bigint not null default 1,

    primary key (account_id, payment_id, id),

    -- The refund belongs to the same account as the payment it is against.
    foreign key (account_id, payment_id) references payments (account_id, id),

    constraint refunds_amount_is_positive check (amount > 0),
    constraint refunds_idempotency_names_its_body check (
        (idempotency_key is null) = (idempotency_body_hash is null)
    )
);

-- One key spends one authorisation, across attempts and refunds alike. This
-- holds the refund half; a key that reached both tables is a collision
-- nothing here writes.
create unique index refunds_by_key on refunds (network, key);

-- What the ceiling is counted from, and what a payment's refunds are read by.
create index refunds_by_payment on refunds (account_id, payment_id, status);

-- The token a merchant's page is reached by.
create unique index refunds_by_token_hash on refunds (token_hash);

-- A retry of one request opens one refund, as it opens one payment.
create unique index refunds_by_idempotency_key on refunds (account_id, idempotency_key)
    where idempotency_key is not null;

-- An observation now stands against an attempt or against a refund, and the
-- same reading of the chain settles both. A composite foreign key with a null
-- column is not checked, so an attempt_id of null leaves the attempts key
-- unenforced for a refund's row, and the other way round.
alter table observations
    alter column attempt_id drop not null,
    add column refund_id text,
    add constraint observations_stand_against_one_thing check (
        num_nonnulls(attempt_id, refund_id) = 1
    ),
    add foreign key (account_id, payment_id, refund_id)
        references refunds (account_id, payment_id, id);
