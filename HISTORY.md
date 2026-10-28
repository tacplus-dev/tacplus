# History: tacplus

Append-only: add entries at the end, never edit or remove one (CI enforces it). One line each:
`- <YYYY-MM-DD> <who: looper|agent|jody> <kind: issue|decision|release|deploy> <one line> (<links>)`

- 2026-10-28 looper issue Route tenant-owned RPCs by a declared target-tenant field (#170, 6a8849fc)
- 2026-10-28 looper issue Records: generated write surface, entry/map locks, durability levels, Watch (#163, 8d1da6d1)
