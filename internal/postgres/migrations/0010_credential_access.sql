-- What a credential may do is its access, read or write.
--
-- The column was called capability. That word is kept for what an account
-- may use once a deployment serves more than one, which is a different
-- question from what one credential may do, and a document that used one
-- word for both would be read wrong by whoever met the other first. Renamed
-- rather than rewritten in the migration that made it, because a database
-- that applied that one holds its checksum.
alter table credentials rename column capability to access;
alter table credentials rename constraint credentials_capability_is_read_or_write
    to credentials_access_is_read_or_write;
