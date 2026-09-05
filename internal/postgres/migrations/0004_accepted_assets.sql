-- What an account accepts payment in, and where each is paid to. A request
-- names an asset; whether the account takes it, and the address on its
-- network the payment goes to, are the account's to say, and are said here
-- once rather than in every request.
create table accepted_assets (
    account_id uuid not null references accounts (id),

    -- The asset as payments identifies one: the network, and whatever that
    -- network calls the token. Not the name a document lists it under, which
    -- is one deployment's to choose and to change.
    asset_network   text not null,
    asset_reference text not null,

    -- The address on that network a payment of the asset is paid to.
    destination text not null,

    created_at timestamptz not null,
    updated_at timestamptz not null,

    primary key (account_id, asset_network, asset_reference)
);
