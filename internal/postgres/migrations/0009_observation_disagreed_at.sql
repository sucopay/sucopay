-- When the endpoints were first found to disagree about a recorded transfer.
--
-- Nothing is settled from a disagreement, and it does not resolve itself, so
-- an operator needs to know it is there: doctor counts these rows. The worker
-- writes the column the first time the endpoints disagree and clears it the
-- first time they agree again. Null is "not disagreed about", which every row
-- from before this column is.
alter table observations add column disagreed_at timestamptz;
