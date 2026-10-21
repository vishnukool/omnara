-- +goose Up

-- Claude binds replayed thinking to the request prefix that produced it. The
-- digest of that prefix lets later requests replay thinking only while the
-- prefix is unchanged.
ALTER TABLE model_outputs
    ADD COLUMN provider_replay_prefix_digest text,
    ADD CONSTRAINT model_outputs_provider_replay_prefix_digest CHECK (
        provider_replay_prefix_digest IS NULL OR (
            provider_replay IS NOT NULL
            AND provider_replay_prefix_digest ~ '^[0-9a-f]{64}$'
        )
    );
