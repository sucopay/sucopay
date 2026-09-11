-- Indexes for the worker that decides what settled and what ran out of time.
--
-- None of them begins with the account. The worker runs for the deployment and
-- not for one merchant, and a read that begins with the account is an API read,
-- which payments_by_status already serves.

-- The payments whose clock has passed, on one network. One worker reads one
-- network, so the network leads; the status splits the two sweeps apart, and
-- the deadline is the range each of them asks for.
create index payments_by_clock on payments (asset_network, status, expires_at);

-- The transfers that would settle a payment: matched, and seen where the chain
-- said it will not replace them. Partial on final_at because a transfer seen
-- ahead of that is not one to ask an endpoint about, and while a range is being
-- read those are most of what arrives. The block and the transaction are here
-- so that a pass taking them in that order reads the index rather than sorting
-- what it found.
create index observations_to_settle on observations (network, reason, block_height, tx)
    where final_at is not null;

-- The transfers recorded against one payment. Taking back what a payment says
-- arrived counts them first, and the sweep asks whether anything is still
-- matched before it gives up on a payment. Both read the whole table without
-- this, and both run every round.
create index observations_by_payment on observations (account_id, payment_id, reason);
