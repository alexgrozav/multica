-- Fallback runtimes for agents (one main runtime + ordered fallbacks).
--
-- An agent stays pinned to its primary `agent.runtime_id`; this table adds an
-- ordered list of same-provider runtimes that dispatch may use when the
-- primary is offline (or when a fallback lives on the computer the requesting
-- user is currently working at — the "current computer wins" rule).
--
-- Rows are user-curated allow-list entries, not a scheduler pool: dispatch
-- never routes to a runtime that is not the agent's main runtime or listed
-- here. Provider compatibility (fallback provider == main runtime provider)
-- is enforced at the API layer on write; the DB only guarantees identity
-- integrity and cleanup:
--   * agent delete cascades its fallback list,
--   * runtime delete (incl. the 7-day offline GC in the runtime sweeper)
--     silently drops the fallback entries that pointed at it.
CREATE TABLE agent_fallback_runtime (
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL REFERENCES agent_runtime(id) ON DELETE CASCADE,
    -- 0-based order among fallbacks; the main runtime is implicitly ahead of
    -- every row. Rewritten wholesale on agent update, so gaps are harmless.
    position INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, runtime_id)
);

-- Runtime-side lookup for delete-plan/cascade UX ("N agents list this
-- computer as a fallback") and for the FK cascade path.
CREATE INDEX idx_agent_fallback_runtime_runtime ON agent_fallback_runtime(runtime_id);
