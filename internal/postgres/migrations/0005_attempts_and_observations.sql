-- An attempt is one payer's go at paying a payment: what they were given to
-- sign, and the key that authorisation spends. A payment can be attempted
-- more than once, so the attempt rather than the payment is what a transfer
-- seen on a chain is matched to.
create table attempts (
    account_id uuid not null,
    payment_id text not null,
    id         text not null,

    -- How the transfer is authorised, and where it is expected. The network is
    -- the payment's, kept here because every query about a transfer knows the
    -- network and not yet the account.
    scheme  text not null,
    network text not null,

    -- What the chain lets be spent once. Two attempts on one network cannot
    -- hold the same key, which is what makes a transfer's key name one
    -- attempt.
    key text not null,

    -- Who signed the transfer, once one has been seen. Empty until then, and
    -- not null, so that reading it needs no case for a payer nobody has met.
    authorizer text not null default '',

    -- The moment from which the authorisation is no longer good. Whole
    -- seconds, because that is what the chain compares.
    valid_before timestamptz not null,

    status text not null,

    -- Concurrent updates to one attempt are resolved here, the way a payment's
    -- version resolves its own.
    revision   bigint      not null default 1,
    created_at timestamptz not null,

    primary key (account_id, payment_id, id),

    -- The attempt belongs to the same account as the payment it is against.
    -- A composite key rather than one on payment_id alone, so that no row can
    -- name a payment of another account.
    foreign key (account_id, payment_id) references payments (account_id, id),

    -- The domain defines these two and a test compares the lists. Submitted,
    -- succeeded and failed arrive with submission and finality.
    constraint attempts_status_is_one_of_the_lifecycle check (
        status in ('issued', 'confirming')
    ),

    -- A transfer names its key and nothing else. Were two attempts to share
    -- one, the transfer would belong to both.
    constraint attempts_network_key_key unique (network, key)
);

-- A payment has at most one attempt that could still be paid. Two would mean
-- two keys a payer could spend against one payment, and a payment paid twice.
-- Partial, because attempts that are done accumulate. The read that asks for
-- the live attempt of a payment names the same statuses, so a status added to
-- one belongs in the other.
create unique index attempts_one_live_per_payment
    on attempts (account_id, payment_id)
    where status in ('issued', 'confirming');

-- Every transfer the observer saw against an attempt, and what the rules made
-- of it. A row is written whether or not the transfer paid the payment: what
-- arrived and was refused is what a merchant asks about when a payer says they
-- paid.
create table observations (
    account_id uuid not null,
    payment_id text not null,
    attempt_id text not null,

    -- The key is the attempt's, and the transaction is where it was spent. A
    -- transaction can carry more than one transfer, so the two together are
    -- what identifies a row, and a transfer that comes back in another block
    -- rewrites the row it already has.
    network  text    not null,
    key      text    not null,
    tx       text    not null,
    position integer not null,

    -- The block as it stood when the transfer was seen. block_time is what the
    -- chain stamped, not when this process looked.
    block_height bigint      not null,
    block_hash   text        not null,
    block_time   timestamptz not null,

    -- The transfer as the chain wrote it. sender and recipient are its from
    -- and to, named to stay out of SQL's way.
    asset      text           not null,
    authorizer text           not null,
    sender     text           not null,
    recipient  text           not null,
    value      numeric(78, 0) not null,

    -- What the rules made of the transfer, in one word, and the token
    -- implementation that was behind the asset when it was seen.
    reason         text not null,
    implementation text not null,

    -- When this process saw it, and when it saw it in the finalised range.
    -- final_at is empty for a transfer seen only ahead of finality.
    seen_at  timestamptz not null,
    final_at timestamptz,

    primary key (network, key, tx),
    foreign key (account_id, payment_id, attempt_id)
        references attempts (account_id, payment_id, id),
    constraint observations_value_is_not_negative check (value >= 0)
);

-- How far the observer has read each network, and the block it stopped at.
-- The hash is compared before reading on: a height whose block is no longer
-- the one that was there is a chain that moved under the reader.
create table observation_cursors (
    network    text primary key,
    height     bigint      not null,
    hash       text        not null,
    updated_at timestamptz not null
);

-- What one process holds while it observes a network. Instances of a
-- deployment take a lease rather than a lock, so that one dying does not stop
-- the rest until somebody notices.
create table leases (
    name       text primary key,
    holder     text        not null,
    expires_at timestamptz not null
);
