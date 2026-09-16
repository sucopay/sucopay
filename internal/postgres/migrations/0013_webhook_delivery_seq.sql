-- The order deliveries were made in. Their ids are random, so that a receiver
-- learns nothing from them, and the order the events of one payment happened
-- in has to be kept somewhere else: a delivery is sent only after every
-- earlier one to the same endpoint about the same payment is done with.
-- Not created_at: the deliveries one expansion makes are written at the one
-- time it ran, and two events of one payment can be among them.
alter table webhook_deliveries add column seq bigint generated always as identity;
create unique index webhook_deliveries_seq on webhook_deliveries (seq);
