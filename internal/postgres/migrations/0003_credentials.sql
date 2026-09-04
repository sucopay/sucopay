-- A credential says which account a request is from. Nothing in the request
-- says it, because an account named in a request is one the caller chose.
create table credentials (
    -- Independent of the token and of its hash. This is what an operator
    -- revokes by, so it is typed into shells and shows up in ps and in
    -- records, and a prefix of the hash here would be a fragment of the
    -- credential there.
    id uuid primary key,

    -- The account, for a credential of one account. Null for a credential
    -- of the deployment.
    account_id uuid references accounts (id),

    -- Which of the two the credential is for, as a word, rather than as
    -- whether account_id is null. Null arrives by omission too: a column left
    -- out, a default, an insert written by hand. Where null means the wider
    -- grant, that ordinary mistake promotes a credential silently. Here it
    -- fails on insert. No default, for the same reason.
    scope text not null,

    -- What the credential may do. Two values and no system of permissions:
    -- an operator who needs finer grants defines them on their side.
    capability text not null,

    -- HMAC-SHA256 of the token under the key key_id names. The token is
    -- stored nowhere, and the hash alone tests nothing without the key.
    hash bytea not null unique,

    -- Which key the hash was made under. Not a secret, so it is stored as
    -- it is.
    key_id text not null,

    created_at   timestamptz not null,
    revoked_at   timestamptz,
    last_used_at timestamptz,

    -- Which scopes there are, and what account each names: a credential of
    -- one account names it, and a credential of the deployment names none.
    -- The list and the rule are one constraint, so that a third scope has
    -- to say what its account is before a row of it can exist.
    constraint credentials_scope_names_its_account check (
        (scope = 'account' and account_id is not null)
        or (scope = 'deployment' and account_id is null)
    ),
    constraint credentials_capability_is_read_or_write check (
        capability in ('read', 'write')
    )
);
