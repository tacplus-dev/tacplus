# History: tacplus

Append-only: add entries at the end, never edit or remove one (CI enforces it). One line each:
`- <YYYY-MM-DD> <who: looper|agent|jody> <kind: issue|decision|release|deploy> <one line> (<links>)`

- 2026-10-28 looper issue Route tenant-owned RPCs by a declared target-tenant field (#170, 6a8849fc)
- 2026-10-28 looper issue Records: generated write surface, entry/map locks, durability levels, Watch (#163, 8d1da6d1)
- 2026-10-28 looper issue Release tacplus v2.5.0 (routing #170, records #163) once #163 closes (#198, 9d2ee926)
- 2026-10-28 looper release v2.5.0 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.5.0)
- 2026-10-28 looper issue diskstore archive: stable member id across restarts (#143, c4d585e3)
- 2026-10-28 looper issue diskstore archive: retention of old segments and checkpoints (#144, cc67e297)
