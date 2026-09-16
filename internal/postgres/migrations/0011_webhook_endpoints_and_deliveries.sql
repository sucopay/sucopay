-- Where a merchant is told that a payment changed, and what was told.
--
-- A webhook endpoint is a URL the deployment sends to. It is of one account,
-- or of the whole deployment for an operator who wants every account's
-- events in one place; the two are told apart by scope, the way a
-- credential's are, and a row that names an account under the wider scope,
-- or none under the narrower, fails on insert rather than being read one way
-- or the other.
--
-- The secret is stored encrypted rather than hashed: signing needs the value
-- back, where checking a token needs only its hash. The key it is encrypted
-- under is derived from the deployment's credentials key, so a table read on
-- its own presents nothing.
create table webhook_endpoints (
    id          text primary key,
    scope       text not null,
    account_id  uuid references accounts (id),
    url         text not null,
    description text not null,

    -- The event types this endpoint receives. Null is every type: an
    -- endpoint registered with no list is one that wants everything, and a
    -- type added later reaches it.
    events text[],

    enabled boolean not null,

    -- The secret in force, and the one it replaced while both are: a
    -- receiver takes a day to move its verification to the new one, and a
    -- delivery in that day is signed under both.
    secret          bytea       not null,
    previous_secret bytea,
    previous_until  timestamptz,

    -- Addresses the operator allowed this endpoint to be delivered to
    -- although they are inside the deployment, as CIDR text. Bound to the
    -- endpoint, and to an address rather than the name, so that whoever
    -- controls the name's DNS cannot move it elsewhere on the network.
    allowed text[] not null default '{}',

    created_at timestamptz not null,
    deleted_at timestamptz,

    constraint webhook_endpoints_scope_names_its_account check (
        (scope = 'account' and account_id is not null)
        or (scope = 'deployment' and account_id is null)
    ),
    constraint webhook_endpoints_description_is_bounded check (octet_length(description) <= 200)
);

-- The endpoints of one account, which is what its list reads.
create index webhook_endpoints_by_account on webhook_endpoints (account_id) where deleted_at is null;

-- One delivery is one event to one endpoint. Its id is the webhook-id the
-- receiver sees, and is the same on every attempt at it, resends included,
-- so that a receiver can use it to refuse a repeat.
--
-- The body is copied here from the outbox, whose row is removed once its
-- deliveries exist: this is then the one place the body lives.
create table webhook_deliveries (
    id          text primary key,
    endpoint_id text not null references webhook_endpoints (id),
    account_id  uuid not null references accounts (id),
    -- Null for an event about no payment, which endpoint.test is.
    payment_id  text,

    type        text        not null,
    occurred_at timestamptz not null,
    payload     jsonb       not null,

    state    text not null,
    attempts integer not null default 0,
    -- When the next attempt is due, while the delivery is pending.
    next_at      timestamptz,
    delivered_at timestamptz,
    created_at   timestamptz not null,

    foreign key (account_id, payment_id) references payments (account_id, id),
    constraint webhook_deliveries_state_is_known check (state in ('pending', 'delivered', 'failed'))
);

-- What a round reads: the deliveries whose time has come, oldest first.
create index webhook_deliveries_due on webhook_deliveries (next_at) where state = 'pending';

-- The deliveries of one endpoint, newest first, which is what its list reads.
create index webhook_deliveries_by_endpoint on webhook_deliveries (endpoint_id, created_at desc);

-- The deliveries of one payment to one endpoint, in the order the events
-- happened, which is the order they are sent in.
create index webhook_deliveries_by_payment on webhook_deliveries (endpoint_id, payment_id, created_at);

-- One attempt at one delivery: when, what the receiver answered, and how
-- long it took. What the receiver answered is cut short and cleaned before
-- it is written, since it is somebody else's text.
create table webhook_attempts (
    id          bigint generated always as identity primary key,
    delivery_id text not null references webhook_deliveries (id),
    at          timestamptz not null,
    -- The HTTP status the receiver answered, or null when there was none.
    status   integer,
    -- Why there was no status, in a word: a connection refused, a timeout,
    -- a destination that did not pass its check. Empty when there was one.
    reason   text not null default '',
    response text not null default '',
    took_ms  integer not null
);

create index webhook_attempts_by_delivery on webhook_attempts (delivery_id, at);
