-- What the sweep reads: the deliveries that are done with, oldest first. A
-- partial index, as the one for what is due is, so that the deliveries kept
-- pending for a disabled endpoint are not walked past every round.
create index webhook_deliveries_sweepable on webhook_deliveries (created_at) where state <> 'pending';
