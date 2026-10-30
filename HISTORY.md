# History: tacplus

Append-only: add entries at the end, never edit or remove one (CI enforces it). One line each:
`- <YYYY-MM-DD> <who: looper|agent|jody> <kind: issue|decision|release|deploy> <one line> (<links>)`

- 2026-10-28 looper issue Route tenant-owned RPCs by a declared target-tenant field (#170, 6a8849fc)
- 2026-10-28 looper issue Records: generated write surface, entry/map locks, durability levels, Watch (#163, 8d1da6d1)
- 2026-10-28 looper issue Release tacplus v2.5.0 (routing #170, records #163) once #163 closes (#198, 9d2ee926)
- 2026-10-28 looper release v2.5.0 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.5.0)
- 2026-10-28 looper issue diskstore archive: stable member id across restarts (#143, c4d585e3)
- 2026-10-28 looper issue diskstore archive: retention of old segments and checkpoints (#144, cc67e297)
- 2026-10-29 looper issue Retire the linked claim from the generated namespace backstop (#171, c83f4dc1)
- 2026-10-29 looper issue fetch layer: authenticate GitHub clones with GH_TOKEN (private registry), only to github.com (#191, d80e8fad)
- 2026-10-29 looper issue Fleets pin services by release line (MAJOR.MINOR → latest patch), not commit (#180, 7e11aa5b)
- 2026-10-29 looper issue Generated Rust chassis: rsa (RUSTSEC-2023-0071) and crypto-bigint 0.5 (RUSTSEC-2026-0387) advisories (#186, 507cda7e)
- 2026-10-30 looper release v2.0.12 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.0.12)
- 2026-10-30 looper release v2.1.5 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.1.5)
- 2026-10-30 looper release v2.2.2 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.2.2)
- 2026-10-30 looper release v2.3.4 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.3.4)
- 2026-10-30 looper release v2.4.3 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.4.3)
- 2026-10-30 looper release v2.5.2 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.5.2)
- 2026-10-30 looper issue Record plane on a tenant-owned record store: the generated S2S client reaches any tenant's Stratum (no tenant routing) (#207, 31ea0d55)
- 2026-10-30 looper release v2.5.3 (https://github.com/tacplus-dev/tacplus/releases/tag/v2.5.3)
