-- +goose Up
ALTER TABLE analytics_user_agents ADD COLUMN classification_version INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE analytics_user_agents DROP COLUMN classification_version;
