-- Every domain row carries an account from the first migration. A deployment
-- serves one merchant and nothing exposes the column, but tenancy is a
-- predicate on every query and a boundary in every authorization check, and
-- adding it to a live payment database means migrating every table and
-- re-auditing every query at once.
create table accounts (
    id         uuid primary key,
    name       text        not null,
    created_at timestamptz not null default now()
);

-- The one account a deployment has. It is written here rather than by a
-- command, so that a database with the schema is a database that can hold a
-- payment.
insert into accounts (id, name) values ('00000000-0000-0000-0000-000000000001', 'default');

create table payments (
    id         text        not null,
    account_id uuid        not null references accounts (id),

    -- What identifies the token, as the domain identifies it: the network and
    -- whatever that network calls it. The symbol and the decimals describe it
    -- and are kept so that a row still renders when a registry entry is gone.
    asset_network   text     not null,
    asset_reference text     not null,
    asset_symbol    text     not null,
    asset_decimals  smallint not null,

    -- The amount in the asset's smallest unit. numeric because JPYC has 18
    -- decimals and an amount does not fit in bigint, and (78, 0) because that
    -- is what the domain will parse.
    amount numeric(78, 0) not null,

    destination text        not null,
    status      text        not null,
    metadata    jsonb       not null default '{}',
    created_at  timestamptz not null,
    expires_at  timestamptz not null,

    -- Concurrent updates to one payment are resolved here rather than with a
    -- cluster-wide lock.
    version bigint not null default 1,

    primary key (account_id, id),
    constraint payments_amount_is_not_negative check (amount >= 0),
    constraint payments_expires_after_creation check (expires_at > created_at),

    -- The aggregate refuses to load a row holding anything else, so without
    -- this a bad status is found when someone reads the payment rather than
    -- when something wrote it. The list is the one the domain defines, and a
    -- test compares the two.
    constraint payments_status_is_one_of_the_lifecycle check (
        status in ('created', 'awaiting_payment', 'succeeded', 'failed', 'expired')
    )
);

-- Every query filters on the account, and the ones that do not are the bug
-- this index will not hide.
create index payments_by_status on payments (account_id, status, expires_at);
