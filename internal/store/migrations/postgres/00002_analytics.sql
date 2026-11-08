-- +goose Up
CREATE SCHEMA analytics;
CREATE TABLE analytics.user_agents (
 id BIGSERIAL PRIMARY KEY,
 sha256 CHAR(64) NOT NULL UNIQUE,
 user_agent TEXT NOT NULL
);
CREATE TABLE analytics.request_events (
 occurred_at TIMESTAMPTZ NOT NULL,
 id BIGINT GENERATED ALWAYS AS IDENTITY,
 client_ip INET,
 user_agent_id BIGINT NOT NULL REFERENCES analytics.user_agents(id),
 route TEXT NOT NULL,
 channel TEXT,
 client_version TEXT,
 platform TEXT,
 target_version TEXT,
 include_updater BOOLEAN,
 include_revoked BOOLEAN,
 PRIMARY KEY(occurred_at,id)
) PARTITION BY RANGE (occurred_at);
CREATE INDEX request_events_user_agent ON analytics.request_events(user_agent_id);
CREATE VIEW analytics.requests_v1 AS
 SELECT e.occurred_at,e.id,host(e.client_ip) AS client_ip,u.user_agent,e.route,e.channel,
 e.client_version,e.platform,e.target_version,e.include_updater,e.include_revoked
 FROM analytics.request_events e JOIN analytics.user_agents u ON u.id=e.user_agent_id
 WHERE e.occurred_at >= CURRENT_TIMESTAMP - INTERVAL '14 days';

-- +goose Down
DROP VIEW analytics.requests_v1;
DROP TABLE analytics.request_events;
DROP TABLE analytics.user_agents;
DROP SCHEMA analytics;

