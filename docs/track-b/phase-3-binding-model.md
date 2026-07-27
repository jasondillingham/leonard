# Phase 3 — the binding model

**Status:** sketch. Heavily gated. **Do not spec further until Phases 1 and 2 have run.**

This is the general form of the idea. It is written down so the seam is named, not because it
should be built. If Phase 1 shows `facts.yaml` already covers most drift, and Phase 2's three
built-in resolvers cover the rest, **this phase may never be justified** — and that would be the
correct outcome, not a failure.

## The generalization

All the mechanically-checkable drift cases share one shape: **two representations of a single
truth, able to diverge silently.**

```
binding := (subject, expected, resolver, resolver_args, last_checked, ttl, verdict)
```

`resolver` is anything that can produce the current truth: a file's contents, a Go constant, a
`git` query, a checksum of an installed binary, an HTTP endpoint.

Phase 0 already ships `last_checked` and `ttl` on claims. A binding is a claim that knows how to
re-check itself.

## Where it lives

Mirror the split the project already uses:

- **Declared** in the truth tree — operator-authored, reviewable, in git, like `facts.yaml`.
- **Cached** in the store — `last_checked`, last verdict, like every other derived artifact.

That is the existing `facts.yaml`↔store philosophy, not a new architecture. Any design that puts
binding *declarations* in the database is fighting the project's grain.

## The two constraints found while grounding this

Both are real and both were verified.

**1. Sync plugins give less than they appear to.**

`internal/adapters/groundtruth/sync/doc.go` defines a JSON-stdin/stdout contract returning
`changes: [{path, old, new, reason}]`. That *is* a drift report — so `leonard sync --dry-run`
would surface drift essentially for free.

The catch: it only covers facts a plugin already owns. This repo currently declares **zero** facts
and configures **zero** sync plugins, so dry-run would report on an empty set. It is an
opportunistic win once Phase 1 creates a truth tree, not a phase of its own, and not a shortcut
past the work.

**2. Automatic checking crosses a trust boundary that does not exist yet.**

`sync/doc.go` states plainly that explicit `leonard sync` invocation **is** the trust boundary —
plugins are never invoked automatically — and flags SHA-256 fingerprinting of plugin paths (the
protection `[post_edit.verify].command` already has) as unbuilt.

So any SessionStart-triggered drift check that runs *external* resolvers requires that trust work
first. Built-in resolvers (Phase 2) sidestep it entirely. **This is the strongest argument for
keeping resolvers built-in as long as possible.**

## What would justify building this

One of:

- Phase 1 shows hand-maintained `facts.yaml` drifts as badly as the README did, making resolvers
  the only way to keep the fact table honest. This is the most likely trigger and the most
  compelling.
- A fourth or fifth resolver appears that Leonard should not ship itself (org-specific, credentialed).
- Another project adopts the ground-truth adapter and needs resolvers Leonard cannot anticipate.

Absent one of those, three built-ins are cheaper, safer, and require no trust work.

## What would kill it

Phase 1 showing that declaring facts catches the drift, plus Phase 2's built-ins covering the
rest. Then the general model is architecture for its own sake, and the right move is to write that
conclusion down and stop.
