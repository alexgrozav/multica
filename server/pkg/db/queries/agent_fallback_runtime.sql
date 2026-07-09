-- name: DeleteAgentFallbackRuntimes :exec
-- The fallback list is rewritten wholesale on agent update (delete + insert
-- inside the caller's transaction), so partial-update ordering bugs cannot
-- leave a stale mix of old and new entries.
DELETE FROM agent_fallback_runtime WHERE agent_id = $1;

-- name: InsertAgentFallbackRuntimes :exec
-- Inserts the ordered fallback list in one statement. Position is the array
-- index of runtime_ids, so the caller's slice order is the fallback order.
INSERT INTO agent_fallback_runtime (agent_id, runtime_id, position)
SELECT sqlc.arg(agent_id), u.runtime_id, u.ord - 1
FROM unnest(sqlc.arg(runtime_ids)::uuid[]) WITH ORDINALITY AS u(runtime_id, ord);

-- name: ListAgentFallbackRuntimes :many
-- Ordered fallback list for a single agent, with the runtime fields the API
-- serializer and validators need (provider for the same-provider rule,
-- status/daemon_id for availability display).
SELECT afr.runtime_id, afr.position, ar.name, ar.provider, ar.status, ar.daemon_id
FROM agent_fallback_runtime afr
JOIN agent_runtime ar ON ar.id = afr.runtime_id
WHERE afr.agent_id = $1
ORDER BY afr.position ASC, afr.created_at ASC;

-- name: ListAgentFallbackRuntimesByAgentIDs :many
-- Batch variant for agent list serialization: one query for the whole page
-- instead of one per agent.
SELECT afr.agent_id, afr.runtime_id, afr.position, ar.name, ar.provider, ar.status
FROM agent_fallback_runtime afr
JOIN agent_runtime ar ON ar.id = afr.runtime_id
WHERE afr.agent_id = ANY($1::uuid[])
ORDER BY afr.agent_id, afr.position ASC, afr.created_at ASC;

-- name: ListAgentDispatchRuntimes :many
-- Dispatch candidates for an agent in preference order: the main runtime
-- first (position -1), then fallbacks by position. The service layer walks
-- this list to pick the runtime a new task is pinned to ("current computer"
-- daemon match wins, then first online). Rows for archived/deleted runtimes
-- cannot appear: fallback rows cascade away with the runtime and
-- agent.runtime_id is ON DELETE RESTRICT.
SELECT ar.id, ar.daemon_id, ar.status, ar.provider, cand.position
FROM (
    SELECT a.runtime_id, -1 AS position
    FROM agent a
    WHERE a.id = $1
    UNION ALL
    SELECT afr.runtime_id, afr.position
    FROM agent_fallback_runtime afr
    WHERE afr.agent_id = $1
) cand
JOIN agent_runtime ar ON ar.id = cand.runtime_id
ORDER BY cand.position ASC;

-- name: CountAgentFallbackReferencesByRuntime :one
-- How many agents list this runtime as a fallback. Feeds the runtime delete
-- plan so the UI can warn "N agents use this computer as a fallback" (the
-- rows themselves are cascade-dropped on delete, so this is advisory only).
SELECT COUNT(*) FROM agent_fallback_runtime WHERE runtime_id = $1;
