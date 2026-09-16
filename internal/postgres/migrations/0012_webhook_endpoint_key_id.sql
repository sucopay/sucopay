-- Which key an endpoint's secret is sealed under, as a credential's row says
-- which key its hash was made under. A deployment whose key was swapped
-- holds secrets nothing can open, and the rows say which, rather than every
-- delivery discovering it one at a time.
alter table webhook_endpoints add column key_id text not null default '';
alter table webhook_endpoints alter column key_id drop default;
