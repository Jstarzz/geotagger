CREATE DATABASE IF NOT EXISTS geotagger;

CREATE TABLE IF NOT EXISTS geotagger.audit_events
(
    timestamp DateTime64(6, 'UTC'),
    request_id String,
    caller_id LowCardinality(String),
    ip_value String,
    ip_mode LowCardinality(String),
    country_code LowCardinality(String),
    country String,
    outcome LowCardinality(String),
    status_code UInt16,
    lookup_latency_us UInt64,
    mmdb_version LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toDate(timestamp)
ORDER BY (timestamp, caller_id, request_id)
TTL timestamp + INTERVAL 30 DAY DELETE;
