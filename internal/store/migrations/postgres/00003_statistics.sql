-- +goose Up
ALTER TABLE analytics.request_events
 ADD COLUMN method TEXT,
 ADD COLUMN status_code SMALLINT,
 ADD COLUMN duration_us BIGINT CHECK(duration_us >= 0),
 ADD COLUMN request_bytes BIGINT CHECK(request_bytes >= 0),
 ADD COLUMN response_bytes BIGINT CHECK(response_bytes >= 0),
 ADD COLUMN host TEXT,
 ADD COLUMN scheme TEXT,
 ADD COLUMN http_version TEXT,
 ADD COLUMN tls_version TEXT,
 ADD COLUMN content_type TEXT,
 ADD COLUMN referer_host TEXT,
 ADD COLUMN app_cache TEXT,
 ADD COLUMN error_code TEXT,
 ADD COLUMN ip_version SMALLINT,
 ADD COLUMN country TEXT,
 ADD COLUMN continent TEXT,
 ADD COLUMN region TEXT,
 ADD COLUMN city TEXT,
 ADD COLUMN latitude DOUBLE PRECISION,
 ADD COLUMN longitude DOUBLE PRECISION,
 ADD COLUMN asn BIGINT,
 ADD COLUMN asn_org TEXT;
ALTER TABLE analytics.user_agents
 ADD COLUMN browser TEXT,
 ADD COLUMN browser_version TEXT,
 ADD COLUMN os TEXT,
 ADD COLUMN os_version TEXT,
 ADD COLUMN device_type TEXT,
 ADD COLUMN is_bot BOOLEAN;
CREATE VIEW analytics.requests_v2 AS
 SELECT e.occurred_at,e.id,host(e.client_ip) AS client_ip,u.user_agent,e.route,e.channel,
 e.client_version,e.platform,e.target_version,e.include_updater,e.include_revoked,
 e.method,e.status_code,e.duration_us,e.request_bytes,e.response_bytes,e.host,e.scheme,
 e.http_version,e.tls_version,e.content_type,e.referer_host,e.app_cache,e.error_code,
 e.ip_version,e.country,e.continent,e.region,e.city,e.latitude,e.longitude,e.asn,e.asn_org,
 u.browser,u.browser_version,u.os,u.os_version,u.device_type,u.is_bot
 FROM analytics.request_events e JOIN analytics.user_agents u ON u.id=e.user_agent_id
 WHERE e.occurred_at >= CURRENT_TIMESTAMP - INTERVAL '14 days';

-- +goose Down
DROP VIEW analytics.requests_v2;
ALTER TABLE analytics.user_agents DROP COLUMN browser,DROP COLUMN browser_version,
 DROP COLUMN os,DROP COLUMN os_version,DROP COLUMN device_type,DROP COLUMN is_bot;
ALTER TABLE analytics.request_events DROP COLUMN method,DROP COLUMN status_code,
 DROP COLUMN duration_us,DROP COLUMN request_bytes,DROP COLUMN response_bytes,
 DROP COLUMN host,DROP COLUMN scheme,DROP COLUMN http_version,DROP COLUMN tls_version,
 DROP COLUMN content_type,DROP COLUMN referer_host,DROP COLUMN app_cache,DROP COLUMN error_code,
 DROP COLUMN ip_version,DROP COLUMN country,DROP COLUMN continent,DROP COLUMN region,
 DROP COLUMN city,DROP COLUMN latitude,DROP COLUMN longitude,DROP COLUMN asn,DROP COLUMN asn_org;
