```text
model/
├── s2sm/
│   ├── s2sm.go                              # ServiceManager: identity, TLS, health, state, clients
│   ├── grpc/servers/notes/notesapi/
│   │   ├── notes_api.go                     # NotesApiHandler interface + validating, authorizing wrapper
│   │   ├── notes_api_mcp.go                 # the same rpcs as MCP tools
│   │   └── notes_state_service.go           # state service: Get / Set / Watch / Delete
│   └── state/notes/notes_state/             # typed accessors for NotesState
├── types/notes/notesapi/                    # protobuf types with validation helpers
├── chassis/                                 # runtime libraries the generated code calls
├── registry.json                            # the whole model as data, tool catalogue included
└── AGENT.md                                 # the architectural norms, for agents
```
