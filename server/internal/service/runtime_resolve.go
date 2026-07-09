package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrAgentDispatchRuntimeNotFound is returned when runtime resolution finds
// no candidate rows at all — i.e. the agent row itself no longer exists
// (agent.runtime_id is NOT NULL, so an existing agent always yields at least
// its main runtime).
var ErrAgentDispatchRuntimeNotFound = errors.New("agent has no dispatch runtime")

// HeaderLocalDaemonID is the request header first-party clients use to tell
// the server which daemon runs on the computer the request originates from.
// Today only the desktop app can know this (it supervises the local daemon
// and reads its identity over localhost); web requests simply omit it.
//
// The value is a routing HINT, never an authorization input: dispatch only
// ever picks from the agent's own bound runtimes (main + fallbacks), so a
// spoofed header can at most reorder work between machines the agent was
// already allowed to run on.
const HeaderLocalDaemonID = "X-Local-Daemon-ID"

type preferredDaemonKey struct{}

// WithPreferredDaemonID stashes the requesting client's local daemon ID so
// dispatch-time runtime resolution can prefer the computer the user is
// currently working at. Wired once in the router from HeaderLocalDaemonID.
func WithPreferredDaemonID(ctx context.Context, daemonID string) context.Context {
	if daemonID == "" {
		return ctx
	}
	return context.WithValue(ctx, preferredDaemonKey{}, daemonID)
}

// PreferredDaemonIDFromContext returns the daemon ID of the requester's
// current computer, or "" when the client did not (or cannot) report one.
func PreferredDaemonIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(preferredDaemonKey{}).(string)
	return v
}

// ResolvedRuntime is the outcome of picking the runtime a new task gets
// pinned to.
type ResolvedRuntime struct {
	RuntimeID pgtype.UUID
	// Online reports whether the chosen runtime is currently online. False
	// means no candidate (main or fallback) is online and RuntimeID is the
	// agent's main runtime: enqueue paths that tolerate offline runtimes
	// keep their queue-and-wait-for-reconnect behavior, while admission
	// gates (quick-create, autopilot, squad readiness) treat it as
	// "agent unavailable".
	Online bool
	// IsFallback is true when the chosen runtime is not the agent's main
	// runtime. Used for logging/observability so reroutes are traceable.
	IsFallback bool
	// DaemonMatch is true when the runtime was chosen because it lives on
	// the requester's current computer (X-Local-Daemon-ID hint).
	DaemonMatch bool
}

// ResolveDispatchRuntime picks the runtime a new task for this agent should
// be pinned to. Preference order:
//
//  1. an ONLINE candidate on the requester's current computer (the
//     X-Local-Daemon-ID hint), regardless of its position — the user is
//     sitting at that machine, so work lands where they can watch it and
//     open the checkout locally;
//  2. the first ONLINE candidate in bound order: main runtime first, then
//     fallbacks by position;
//  3. no candidate online: the MAIN runtime, with Online=false, preserving
//     the historical queue-and-wait behavior for paths that enqueue against
//     offline runtimes.
//
// Candidates are strictly the agent's main runtime plus its user-curated
// fallback list — resolution never expands to other runtimes in the
// workspace. The returned error is non-nil only on DB failure or when the
// agent row is missing entirely.
func ResolveDispatchRuntime(ctx context.Context, q *db.Queries, agentID pgtype.UUID) (ResolvedRuntime, error) {
	rows, err := q.ListAgentDispatchRuntimes(ctx, agentID)
	if err != nil {
		return ResolvedRuntime{}, err
	}
	if len(rows) == 0 {
		// agent.runtime_id is NOT NULL, so an empty candidate list means the
		// agent itself is gone. Surface it like a lookup failure.
		return ResolvedRuntime{}, ErrAgentDispatchRuntimeNotFound
	}

	if pref := PreferredDaemonIDFromContext(ctx); pref != "" {
		for _, r := range rows {
			if r.Status == "online" && r.DaemonID.Valid && r.DaemonID.String == pref {
				return ResolvedRuntime{
					RuntimeID:   r.ID,
					Online:      true,
					IsFallback:  r.Position != mainRuntimePosition,
					DaemonMatch: true,
				}, nil
			}
		}
	}

	for _, r := range rows {
		if r.Status == "online" {
			return ResolvedRuntime{
				RuntimeID:  r.ID,
				Online:     true,
				IsFallback: r.Position != mainRuntimePosition,
			}, nil
		}
	}

	// Nothing online: fall back to the main runtime (first row by ORDER BY
	// position, which places the main's sentinel position ahead of every
	// fallback).
	return ResolvedRuntime{RuntimeID: rows[0].ID}, nil
}

// mainRuntimePosition is the sentinel position ListAgentDispatchRuntimes
// assigns to the agent's main runtime so it sorts ahead of fallbacks
// (which start at 0).
const mainRuntimePosition = -1
