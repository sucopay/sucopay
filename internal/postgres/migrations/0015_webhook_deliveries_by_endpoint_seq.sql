-- The deliveries of one endpoint, newest first, which is what its list
-- reads. By seq rather than created_at: seq is the order deliveries were
-- made in, and created_at is the one time an expansion ran for all it made.
-- The index by created_at served nothing once the list read by seq.
drop index webhook_deliveries_by_endpoint;
create index webhook_deliveries_by_endpoint on webhook_deliveries (endpoint_id, seq desc);
