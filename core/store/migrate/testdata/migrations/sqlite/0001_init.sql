-- +goose Up
CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT NOT NULL);
-- +goose Down
DROP TABLE notes;
