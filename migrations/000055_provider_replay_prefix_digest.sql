-- +goose Up

ALTER TABLE model_outputs
    ADD COLUMN provider_replay_prefix_digest text,
    ADD CONSTRAINT model_outputs_provider_replay_prefix_digest CHECK (
        provider_replay_prefix_digest IS NULL OR (
            provider_replay IS NOT NULL
            AND provider_replay_prefix_digest ~ '^[0-9a-f]{64}$'
        )
    );
