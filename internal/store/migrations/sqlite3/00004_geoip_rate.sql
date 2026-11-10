-- +goose Up
CREATE TABLE analytics_geoip_api_limiter (
 id INTEGER PRIMARY KEY CHECK(id=1),
 calls TEXT NOT NULL DEFAULT '[]',
 blocked_until DATETIME
);
INSERT INTO analytics_geoip_api_limiter(id) VALUES(1);

-- +goose Down
DROP TABLE analytics_geoip_api_limiter;
