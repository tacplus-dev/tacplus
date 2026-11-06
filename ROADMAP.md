# Roadmap: tacplus

Owned by Jody; changed only by an agent at Jody's request. Loopers propose in PROPOSALS.md.

## Now
tacplus compiles annotated proto definitions into complete services (Go, Rust, Java) and clients (TypeScript, Python, Swift, C#).
Latest release: v2.4.1 (2026-10-27). 2.4 is the line with the global registry and the version floor (#161).
Release lines with branches: 2.0 through 2.4.

## Next
- Formal verification out of the gates: nightly (and on release tags), not per land (#275) (Jody, 2026-11-02)
- Redact for any type: annotated sensitive fields, deterministic generated Redact<T>, Secrets redacted by construction, a gate for unannotated sensitive-looking fields; the uni-config's redacted view for LLM review (#274) (Jody, 2026-11-01)
- Accounting: typed outcome (permitted/denied/failed) and typed metrics (e.g. records returned), no data ever (f (#273) (Jody, 2026-11-01)
- Event trace revisited: owner-bound trace sessions, generated propagation, ephemeral never-recorded events, WatchTrace; the fleet CLI with trace start / --trace (#265) (Jody, 2026-11-01)
- Chassis reads config, not env: token file and audience and config path from config; the ge (#262) (Jody, 2026-11-01)
- Sign-in config in proto: providers, tenant OIDC IdP and domains, join mode, durations (#224)
- Release v2.6.0 (build image #193, record-plane tenant routing #207) once #207 closes (#213)
- Generated member port: Hazelcast member traffic over server TLS plus framed bearer tokens, no mTLS (#212); moves onto gRPC with #259
- macOS build image per release line (`macos-build:X.Y`, pinned like `build:X.Y`: Rust, Go, just, protoc, tacplus), built on the Mac runner, for #195's darwin builds (#261) (Jody, 2026-10-31)
- Long-lived streams survive token expiry by make-before-break rotation: acquire token, open, send, acquire new token, open new stream, cut over, close old; the member port moves onto gRPC (#259, before #236/#237) (Jody, 2026-10-31)
- Generated S2S clients to a peer service in another cluster or deployment: redo site-to-site (redo#1 option A, Jody 2026-10-28) and flock gossip (#204; design needs Jody's OK)
- Release v2.5.0 (routing #170, records #163) once #163 closes (#198)
- Kitchensink: formal verification and fuzzing once per release on a covering proto set; coverage check in every service build; no harnesses in services (#196, after #193)
- Move CI, images, releases and nightlies from GitHub Actions to the GitLab build pipeline (#194, after looper#17)
- No tacplus image: `build:X.Y` per line with a `tacplus` script that fetches and verifies the newest binary; verify (Kani, JBMC) and fuzzing run there; Go not formally verified (#193)
- Fetch layer: authenticate GitHub clones with GH_TOKEN, only to github.com (#191)
- Images: bump moby/go-archive to 0.3.0 (CVE-2026-17106) and patch the release lines (#190)
- Generated Rust chassis: rsa and crypto-bigint advisories (#186)
- Fleets pin services by release line (MAJOR.MINOR to latest patch), not commit (#180)
- JWK verification: infer a missing alg from the token within the key's family (#179)
- Route tenant-owned RPCs by a declared target-tenant field (#170)
- Records: generated write surface, entry/map locks, durability levels, Watch, for 2.5 (#163) (needs Jody's decision)
- diskstore archive: public wiring helper for hand-opened RedoLogs (#145) (needs Jody's decision)
- Make the three well-known platform names proto-declared instead of baked into the generator (#1) (needs Jody's decision)
- **Parallel gates:** run independent gates and cross-test legs concurrently, bounded by the machine, with a deterministic verdict; lands go from hours to under one (#300). Agent, at Jody's request, 2026-11-03 ("I just want stuff to go faster … much of this can be done in parallel").
- Build image: rootless BuildKit for image builds inside the container, shared build caches, cold and warm identical (#315). Agent, at Jody's request, 2026-11-03 (`tacplus-dev/roadmap:decisions/looper-job-per-issue-2026-11-03.md`).
- Member port onto gRPC: a bidi byte stream with make-before-break session rotation, Java (#321). Agent, at Jody's request, 2026-11-05 (#259's split, approved).
- Generated S2S streams survive token expiry by make-before-break rotation in Go, Java and Rust: the additive Rotating sibling service (#322). Agent, at Jody's request, 2026-11-05 (#259's split).
- Make-before-break rotation for the client-only targets and C2S through the gateway (#323). Agent, at Jody's request, 2026-11-05 (#259's split).

## Later
- flock state: `(spec.service_state)` labelled `flock`, field merge rules, generated merge, duplicate detection and deletion marks; first user the people table (people#1); kernel stays in-cluster, flock spans clusters (#206; tacplus-dev/roadmap:items/flock-design-2026-10-28.md; no hurry)
- Fleet builder gathers service webapp pieces and clients, builds the merged webapp and the fleet's gateway (#195, after #180; contract needs Jody's OK)
- Split the build image by language (build-go, build-rust, build-java, …), each with its toolchain and verifier (tacplus-dev/roadmap:decisions/tacplus-images-2026-10-27.md)
- Retire the linked claim from the generated namespace backstop (#171), blocked on #170 and tacplus-dev/kernel#4
- diskstore archive: stable member id across restarts (#143), blocked on #163
- diskstore archive: retention of old segments and checkpoints (#144), blocked on #163
- **Known-bad secret values:** extend the required-secret check (#274) with a list of known-bad values beyond its fixed placeholders. Agent, at Jody's request, 2026-11-02: "looks random is better then no check at all. we could expand this later to check for known bad values".

## Not doing
- Nothing recorded yet.
