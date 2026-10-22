// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The example client: the generated TypeScript C2S ServiceManager talking to
// the Go server. The address arrives in NOTES_ADDR.

import * as grpc from '@grpc/grpc-js';
import { create } from '@bufbuild/protobuf';

import { ServiceManager } from '../model/dist/c2sm/index.js';
import {
  CreateNoteRequestSchema,
  GetNoteRequestSchema,
  ListNotesRequestSchema,
} from '../model/dist/types/services/notes/services/index.js';

const addr = process.env.NOTES_ADDR;
if (!addr) {
  throw new Error('NOTES_ADDR is not set');
}

// The third argument is the token provider: the generated client attaches the
// bearer token it returns to every authenticated call.
const sm = new ServiceManager(addr, grpc.credentials.createInsecure(), async () => 'demo-token');
const api = sm.grpc().notesapi().notes_api_client();

const tenantNamespace = 'tenant.alpha';

const created = await api.createNote(create(CreateNoteRequestSchema, { tenantNamespace, text: 'hello from typescript' }));
console.log('created', created.note.id, JSON.stringify(created.note.text));

const fetched = await api.getNote(create(GetNoteRequestSchema, { tenantNamespace, id: created.note.id }));
console.log('fetched', fetched.note.id, JSON.stringify(fetched.note.text));

await api.createNote(create(CreateNoteRequestSchema, { tenantNamespace, text: 'second note' }));
// A PAGE collection: the generated client walks the pages for you.
const ids = [];
for await (const note of await api.listNotes(create(ListNotesRequestSchema, { tenantNamespace, pageSize: 1 }))) {
  ids.push(note.id);
}
console.log('listed', ids.join(', '));

// Validation runs in the generated client before anything goes on the wire.
try {
  await api.createNote(create(CreateNoteRequestSchema, { tenantNamespace, text: '' }));
  throw new Error('empty text was accepted');
} catch (err) {
  console.log('rejected empty text:', err.message.split('\n')[0]);
}

sm.close?.();
