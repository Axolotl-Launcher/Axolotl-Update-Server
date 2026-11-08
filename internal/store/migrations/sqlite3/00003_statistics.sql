-- +goose Up
ALTER TABLE analytics_request_events ADD COLUMN method TEXT;
ALTER TABLE analytics_request_events ADD COLUMN status_code INTEGER;
ALTER TABLE analytics_request_events ADD COLUMN duration_us BIGINT CHECK(duration_us >= 0);
ALTER TABLE analytics_request_events ADD COLUMN request_bytes BIGINT CHECK(request_bytes >= 0);
ALTER TABLE analytics_request_events ADD COLUMN response_bytes BIGINT CHECK(response_bytes >= 0);
ALTER TABLE analytics_request_events ADD COLUMN host TEXT;
ALTER TABLE analytics_request_events ADD COLUMN scheme TEXT;
ALTER TABLE analytics_request_events ADD COLUMN http_version TEXT;
ALTER TABLE analytics_request_events ADD COLUMN tls_version TEXT;
ALTER TABLE analytics_request_events ADD COLUMN content_type TEXT;
ALTER TABLE analytics_request_events ADD COLUMN referer_host TEXT;
ALTER TABLE analytics_request_events ADD COLUMN app_cache TEXT;
ALTER TABLE analytics_request_events ADD COLUMN error_code TEXT;
ALTER TABLE analytics_request_events ADD COLUMN ip_version INTEGER;
ALTER TABLE analytics_request_events ADD COLUMN country TEXT;
ALTER TABLE analytics_request_events ADD COLUMN continent TEXT;
ALTER TABLE analytics_request_events ADD COLUMN region TEXT;
ALTER TABLE analytics_request_events ADD COLUMN city TEXT;
ALTER TABLE analytics_request_events ADD COLUMN latitude REAL;
ALTER TABLE analytics_request_events ADD COLUMN longitude REAL;
ALTER TABLE analytics_request_events ADD COLUMN asn BIGINT;
ALTER TABLE analytics_request_events ADD COLUMN asn_org TEXT;
ALTER TABLE analytics_user_agents ADD COLUMN browser TEXT;
ALTER TABLE analytics_user_agents ADD COLUMN browser_version TEXT;
ALTER TABLE analytics_user_agents ADD COLUMN os TEXT;
ALTER TABLE analytics_user_agents ADD COLUMN os_version TEXT;
ALTER TABLE analytics_user_agents ADD COLUMN device_type TEXT;
ALTER TABLE analytics_user_agents ADD COLUMN is_bot BOOLEAN;
CREATE VIEW analytics_requests_v2 AS
 SELECT e.occurred_at,e.id,e.client_ip,u.user_agent,e.route,e.channel,
 e.client_version,e.platform,e.target_version,e.include_updater,e.include_revoked,
 e.method,e.status_code,e.duration_us,e.request_bytes,e.response_bytes,e.host,e.scheme,
 e.http_version,e.tls_version,e.content_type,e.referer_host,e.app_cache,e.error_code,
 e.ip_version,e.country,e.continent,e.region,e.city,e.latitude,e.longitude,e.asn,e.asn_org,
 u.browser,u.browser_version,u.os,u.os_version,u.device_type,u.is_bot
 FROM analytics_request_events e JOIN analytics_user_agents u ON u.id=e.user_agent_id
 WHERE julianday(e.occurred_at) >= julianday('now','-14 days');

-- +goose Down
DROP VIEW analytics_requests_v2;
ALTER TABLE analytics_user_agents DROP COLUMN browser;
ALTER TABLE analytics_user_agents DROP COLUMN browser_version;
ALTER TABLE analytics_user_agents DROP COLUMN os;
ALTER TABLE analytics_user_agents DROP COLUMN os_version;
ALTER TABLE analytics_user_agents DROP COLUMN device_type;
ALTER TABLE analytics_user_agents DROP COLUMN is_bot;
ALTER TABLE analytics_request_events DROP COLUMN method;
ALTER TABLE analytics_request_events DROP COLUMN status_code;
ALTER TABLE analytics_request_events DROP COLUMN duration_us;
ALTER TABLE analytics_request_events DROP COLUMN request_bytes;
ALTER TABLE analytics_request_events DROP COLUMN response_bytes;
ALTER TABLE analytics_request_events DROP COLUMN host;
ALTER TABLE analytics_request_events DROP COLUMN scheme;
ALTER TABLE analytics_request_events DROP COLUMN http_version;
ALTER TABLE analytics_request_events DROP COLUMN tls_version;
ALTER TABLE analytics_request_events DROP COLUMN content_type;
ALTER TABLE analytics_request_events DROP COLUMN referer_host;
ALTER TABLE analytics_request_events DROP COLUMN app_cache;
ALTER TABLE analytics_request_events DROP COLUMN error_code;
ALTER TABLE analytics_request_events DROP COLUMN ip_version;
ALTER TABLE analytics_request_events DROP COLUMN country;
ALTER TABLE analytics_request_events DROP COLUMN continent;
ALTER TABLE analytics_request_events DROP COLUMN region;
ALTER TABLE analytics_request_events DROP COLUMN city;
ALTER TABLE analytics_request_events DROP COLUMN latitude;
ALTER TABLE analytics_request_events DROP COLUMN longitude;
ALTER TABLE analytics_request_events DROP COLUMN asn;
ALTER TABLE analytics_request_events DROP COLUMN asn_org;
