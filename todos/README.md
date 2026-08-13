# Heikou ideas

This folder holds product and architecture ideas that are intentionally outside
the current implementation. One idea gets one document so the main design does
not become an undifferentiated backlog.

| Idea | Status |
| --- | --- |
| [Code-quality and reliability audit](code-quality-audit.md) | Active backlog |
| [Heikou pilot](heikou-pilot.md) | CLI verbs and agent instructions shipped; pilot loop and UI deferred |
| [Session history](session-history.md) | Proposed; the first authoritative runner signal |
| [Composable composer modules](composer-modules.md) | Proposed |
| [Configurable brief sources](brief-sources.md) | Shipped |
| [Session status, titles, and recency](session-status-titles.md) | Durable titles shipped in V0.3.4; semantic status deferred |
| [Session ordering within a workstream](session-ordering.md) | Proposed; needs a durable order field and a migration |

An idea should move into `docs/DESIGN.md` only when it becomes part of the
committed architecture or an active implementation.
