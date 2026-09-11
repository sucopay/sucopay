-- outbox holds an event that a change to a payment produced, for whatever
-- delivers events to a merchant. A row is written in the transaction that
-- changed the payment, so a merchant is never told of a change that did not
-- happen and never left unaware of one that did.
--
-- Delivery removes the row. How often a delivery is tried and how long it waits
-- between tries belong to what reads this table, and nothing here records an
-- attempt.
--
-- The only index is the key. What delivers reads in the key's order and removes
-- what it delivered, so the table holds what has not been delivered rather than
-- everything that ever happened. An index on the account and the payment would
-- serve a read nothing here does.
create table outbox (
    -- Supplied by the database rather than by the caller, so that the order
    -- rows were written in is the order they are read in. A payment's events
    -- are written one after another by the one worker that moves it, so that
    -- order is the payment's own.
    id bigint generated always as identity primary key,

    -- Whose payment this is about. What delivers an event has to know whose
    -- endpoint to deliver it to, and that is not derivable from the body.
    account_id uuid not null,
    payment_id text not null,

    -- The word a merchant matches on, such as payment.succeeded.
    event text not null,

    -- What a merchant is handed. Its shape belongs to whatever delivers it,
    -- and nothing here reads inside it.
    payload jsonb not null,

    created_at timestamptz not null default now(),

    -- The event belongs to the same account as the payment it is about. A
    -- composite key rather than one on payment_id alone, so that no row can
    -- name a payment of another account.
    foreign key (account_id, payment_id) references payments (account_id, id),

    constraint outbox_event_is_not_empty check (event <> '')
);
