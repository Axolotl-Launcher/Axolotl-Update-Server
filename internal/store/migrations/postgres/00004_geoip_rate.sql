-- +goose Up
CREATE TABLE analytics.geoip_api_limiter (
 id INTEGER PRIMARY KEY CHECK(id=1),
 calls TEXT NOT NULL DEFAULT '[]',
 blocked_until TIMESTAMPTZ
);
INSERT INTO analytics.geoip_api_limiter(id) VALUES(1);

-- +goose Down
DROP TABLE analytics.geoip_api_limiter;
