-- +goose Up
CREATE TABLE analytics_user_agents (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 sha256 TEXT NOT NULL UNIQUE,
 user_agent TEXT NOT NULL
);
CREATE TABLE analytics_request_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 occurred_at DATETIME NOT NULL,
 client_ip TEXT,
 user_agent_id BIGINT NOT NULL REFERENCES analytics_user_agents(id),
 route TEXT NOT NULL,
 channel TEXT,
 client_version TEXT,
 platform TEXT,
 target_version TEXT,
 include_updater BOOLEAN,
 include_revoked BOOLEAN
);
CREATE INDEX analytics_events_time ON analytics_request_events(occurred_at,id);
CREATE INDEX analytics_events_ua ON analytics_request_events(user_agent_id);
CREATE VIEW analytics_requests_v1 AS
 SELECT e.occurred_at,e.id,e.client_ip,u.user_agent,e.route,e.channel,
 e.client_version,e.platform,e.target_version,e.include_updater,e.include_revoked
 FROM analytics_request_events e JOIN analytics_user_agents u ON u.id=e.user_agent_id
 WHERE julianday(e.occurred_at) >= julianday('now','-14 days');

-- +goose Down
DROP VIEW analytics_requests_v1;
DROP TABLE analytics_request_events;
DROP TABLE analytics_user_agents;

