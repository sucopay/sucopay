-- One pending endpoint.test per endpoint, held by the schema: a test is the
-- one delivery a caller makes at will, and two calls at once would each
-- find none pending and both write one. The index refuses the second.
create unique index webhook_deliveries_one_pending_test
    on webhook_deliveries (endpoint_id) where type = 'endpoint.test' and state = 'pending';
