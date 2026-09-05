-- awaiting_finality sits between "no longer payable" and "expired". Why it
-- has to exist is in internal/payment/status.go, beside the states themselves.
alter table payments
    drop constraint payments_status_is_one_of_the_lifecycle,
    add constraint payments_status_is_one_of_the_lifecycle check (
        status in ('created', 'awaiting_payment', 'awaiting_finality', 'succeeded', 'failed', 'expired')
    );

-- What was asked for and what arrived are separate columns. Held in one, a
-- payment that came up short is indistinguishable from one billed for less, and
-- the shortfall is what the merchant needs in order to decide anything.
alter table payments
    add column received numeric(78, 0),
    add constraint payments_received_is_not_negative check (received is null or received >= 0);

-- A payment cannot be succeeded on less than it billed. The aggregate refuses
-- it, and so does the row, because a row that says paid without the money is
-- the one thing nothing downstream can recover from.
--
-- received is new, so it is null on every row written before this. A database
-- already holding a succeeded payment will therefore refuse this migration.
-- That is the right answer rather than a problem to work around: a payment
-- recorded as paid with no record of what arrived cannot satisfy the rule, and
-- somebody has to decide what it should say before it can.
alter table payments
    add constraint payments_succeeded_only_when_covered check (
        status <> 'succeeded' or (received is not null and received >= amount)
    );
