// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"sort"
	"strings"
)

// LegalID reports whether id may appear in any registry: a legacy per-repo id
// (1-99) or a globally registered one (101-2047). 100 is reserved for azp.
func LegalID(id int32) bool {
	return (id >= 1 && id <= LegacyIDMax) || (id >= GlobalIDMin && id <= GlobalIDMax)
}

// Register adds name to the GLOBAL registry and returns its id. Ids are
// allocated upward from GlobalIDMin, never from the frozen legacy range and
// never reused: the next id is max(existing global id)+1. A name already
// registered is an error, not a silent return, because registration is a
// deliberate act recorded in one MR.
func (r *Registry) Register(kind, name, repo, visibility string) (int32, error) {
	if name == "" || repo == "" {
		return 0, fmt.Errorf("register: name and repo are required")
	}
	if visibility != VisibilityPublic && visibility != VisibilityPrivate {
		return 0, fmt.Errorf("register: visibility must be %q or %q", VisibilityPublic, VisibilityPrivate)
	}
	var id int32
	switch kind {
	case "service":
		if e, ok := r.byName[name]; ok {
			return 0, fmt.Errorf("service %q is already registered as id %d", name, e.ID)
		}
		id = r.nextGlobalID()
		if id == 0 {
			return 0, fmt.Errorf("registry ID overflow: all global ids %d-%d are taken", GlobalIDMin, GlobalIDMax)
		}
		e := &ServiceEntry{Name: name, ID: id, Repo: repo, Visibility: visibility}
		r.Services = append(r.Services, e)
		r.byName[name] = e
	case "vservice":
		if e, ok := r.vsByName[name]; ok {
			return 0, fmt.Errorf("vservice %q is already registered as field_id %d", name, e.FieldID)
		}
		id = r.nextGlobalID()
		if id == 0 {
			return 0, fmt.Errorf("registry ID overflow: all global ids %d-%d are taken", GlobalIDMin, GlobalIDMax)
		}
		e := &VServiceEntry{Name: name, FieldID: id, Repo: repo, Visibility: visibility}
		r.VServices = append(r.VServices, e)
		r.vsByName[name] = e
	default:
		return 0, fmt.Errorf("register: kind must be service or vservice, got %q", kind)
	}
	r.dirty = true
	return id, nil
}

// nextGlobalID returns max(global ids)+1, starting at GlobalIDMin, or 0 when
// the global range is exhausted. Legacy ids below GlobalIDMin are ignored: that
// range is frozen and never allocated from again.
func (r *Registry) nextGlobalID() int32 {
	max := int32(GlobalIDMin - 1)
	for _, e := range r.Services {
		if e.ID > max {
			max = e.ID
		}
	}
	for _, e := range r.VServices {
		if e.FieldID > max {
			max = e.FieldID
		}
	}
	if max+1 > GlobalIDMax {
		return 0
	}
	return max + 1
}

// SyncFromGlobal makes this lock agree with the global registry for the
// declared names. A declared name missing from the global registry is an
// error that lists every such name and the registration step. A lock entry
// whose id differs from the global one is an error too: a lock never
// renumbers, since the id is already baked into tokens and kernel state.
func (r *Registry) SyncFromGlobal(global *Registry, serviceNames, vserviceNames []string) error {
	var unregistered, conflicts []string
	for _, name := range serviceNames {
		g, ok := global.byName[name]
		if !ok {
			unregistered = append(unregistered, fmt.Sprintf("service %q", name))
			continue
		}
		if l, ok := r.byName[name]; ok {
			if l.ID != g.ID {
				conflicts = append(conflicts, fmt.Sprintf("service %q is %d in this lock but %d in the global registry", name, l.ID, g.ID))
				continue
			}
			// Slice fields are wire numbers too (TenantConfig / ProjectConfig
			// field numbers); the global registry owns them like the id.
			if l.Repo != g.Repo || l.Visibility != g.Visibility || l.TenantSliceField != g.TenantSliceField || l.ProjectSliceField != g.ProjectSliceField {
				l.Repo, l.Visibility = g.Repo, g.Visibility
				l.TenantSliceField, l.ProjectSliceField = g.TenantSliceField, g.ProjectSliceField
				r.dirty = true
			}
			continue
		}
		e := &ServiceEntry{Name: name, ID: g.ID, Repo: g.Repo, Visibility: g.Visibility, TenantSliceField: g.TenantSliceField, ProjectSliceField: g.ProjectSliceField}
		r.Services = append(r.Services, e)
		r.byName[name] = e
		r.dirty = true
	}
	for _, name := range vserviceNames {
		g, ok := global.vsByName[name]
		if !ok {
			unregistered = append(unregistered, fmt.Sprintf("vservice %q", name))
			continue
		}
		if l, ok := r.vsByName[name]; ok {
			if l.FieldID != g.FieldID {
				conflicts = append(conflicts, fmt.Sprintf("vservice %q is %d in this lock but %d in the global registry", name, l.FieldID, g.FieldID))
				continue
			}
			if l.Repo != g.Repo || l.Visibility != g.Visibility {
				l.Repo, l.Visibility = g.Repo, g.Visibility
				r.dirty = true
			}
			continue
		}
		e := &VServiceEntry{Name: name, FieldID: g.FieldID, Repo: g.Repo, Visibility: g.Visibility}
		r.VServices = append(r.VServices, e)
		r.vsByName[name] = e
		r.dirty = true
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("registry lock disagrees with the global registry %s (a lock never renumbers; fix the one that is wrong):\n  %s", r.Global, strings.Join(conflicts, "\n  "))
	}
	if len(unregistered) > 0 {
		sort.Strings(unregistered)
		return fmt.Errorf("not in the global registry %s:\n  %s\n\nIn a checkout of the global registry run\n  tacplus registry register NAME --kind service|vservice --repo <this proto repo's URL> --visibility public|private\nand merge it, then re-run tacplus registry here", r.Global, strings.Join(unregistered, "\n  "))
	}
	return nil
}

func (r *Registry) notInLock(kind, name string) error {
	return fmt.Errorf("%s %q is not in the registry lock (authority %s); run `tacplus registry --proto-dir %s` to sync it, registering it first if needed", kind, name, r.Global, r.inputDir)
}
