-- +goose Up
ALTER TABLE notes ADD COLUMN tag TEXT;
-- +goose Down
ALTER TABLE notes DROP COLUMN tag;
