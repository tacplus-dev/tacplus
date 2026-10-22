```protobuf
syntax = "proto3";
package notesapi;
import "sys/spec.proto";

option (spec.service) = {name: "notes"};

message Note {
  string id = 1 [(spec.assert_required) = true, (spec.indexed) = true];
  string tenant_namespace = 2 [(spec.assert_required) = true];
  string text = 3 [(spec.assert_required) = true, (spec.assert_max_size) = 1024];
}

message CreateNoteRequest {
  string tenant_namespace = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 1];
  string text = 2 [(spec.assert_required) = true, (spec.assert_max_size) = 1024];
}
message CreateNoteResponse { Note note = 1; }

// Called by authenticated users through the gateway.
service NotesApi {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // Create a note in the caller's tenant namespace.
  rpc CreateNote(CreateNoteRequest) returns (CreateNoteResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL,
                         flags: [RPC_FLAG_AUTH_ENTITLED_USERS], target_namespace_field: "tenant_namespace"};
  }
  // ... GetNote, and a paged ListNotes
}

// A state root: a partition of distributed state this service owns.
message NotesState {
  option (spec.service_state) = {name: "NotesState", label: ["notesapi"], export: true};
  repeated Note notes = 1 [(spec.indexed) = true];
}
```
