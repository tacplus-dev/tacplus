# tacplus

A compiler that turns annotated Protocol Buffer definitions into service infrastructure from a single source of truth.

Full-service generation is supported for Go, Java, and Rust. Swift, C#, Python,
and TypeScript are client-only targets.

Go, Java and Rust are the server backends, and they are at parity on the record
plane: all three generate the `(spec.store_namespace)` / `(spec.store_principal)`
/ `(spec.record).private` egress row filters on every record-bearing seam.
Python, TypeScript, Swift and C# generate no service wrapper at all, so a record
read surface cannot be hosted in any of them.

## Documentation

- [docs/THEORY.md](docs/THEORY.md) — What tacplus is and why it exists. Start here.
- [docs/USER.md](docs/USER.md) — User manual: proto annotations, `tacplus.yaml` reference, generated output, implementer rules.
- [docs/API.md](docs/API.md) — Generated API contracts and behavioral guarantees.
- [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) — Compiler developer guide: architecture, internals, code quality rules, checklists.
- [roadmap/README.md](roadmap/README.md) — Future plans and design tracks.
