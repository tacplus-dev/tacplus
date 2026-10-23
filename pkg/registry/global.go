// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"sort"
	"strings"
)

// LegalID reports whether id may appear in any registry: a legacy per-repo id
// (1-99) or a registered one (101-2047, global or private). 100 is reserved.
func LegalID(id int32) bool {
	return (id >= 1 && id <= LegacyIDMax) || (id >= GlobalIDMin && id <= PrivateIDMax)
}

// IsLock reports whether this registry.yaml names authorities and so never
// allocates.
func (r *Registry) IsLock() bool { return len(r.Registries) > 0 }

// allocRange is the range this authority allocates from, by scope.
func (r *Registry) allocRange() (int32, int32, error) {
	switch r.Scope {
	case ScopeGlobal:
		return GlobalIDMin, GlobalIDMax, nil
	case ScopePrivate:
		return PrivateIDMin, PrivateIDMax, nil
	case "":
		return 0, 0, fmt.Errorf("this registry.yaml declares no scope; an authority is `scope: global` (ids %d-%d) or `scope: private` (ids %d-%d)", GlobalIDMin, GlobalIDMax, PrivateIDMin, PrivateIDMax)
	default:
		return 0, 0, fmt.Errorf("unknown scope %q; use %q or %q", r.Scope, ScopeGlobal, ScopePrivate)
	}
}

// Register adds name to this AUTHORITY and returns its id, allocated from the
// authority's scope range: never from the frozen legacy range, never reused,
// next = max(existing id in range)+1. A name already registered is an error,
// not a silent return, because registration is a deliberate act recorded in
// one MR. contract is the home of the service's proto; it need not be public.
func (r *Registry) Register(kind, name, contract, visibility string) (int32, error) {
	if name == "" || contract == "" {
		return 0, fmt.Errorf("register: name and contract (the proto repo) are required")
	}
	if r.IsLock() {
		return 0, fmt.Errorf("register: this registry.yaml is a lock of %s, not an authority", strings.Join(r.Registries, ", "))
	}
	lo, hi, err := r.allocRange()
	if err != nil {
		return 0, fmt.Errorf("register: %w", err)
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
		id = r.nextIDIn(lo, hi)
		if id == 0 {
			return 0, fmt.Errorf("registry ID overflow: all %s ids %d-%d are taken", r.Scope, lo, hi)
		}
		e := &ServiceEntry{Name: name, ID: id, Contract: contract, Visibility: visibility}
		r.Services = append(r.Services, e)
		r.byName[name] = e
	case "vservice":
		if e, ok := r.vsByName[name]; ok {
			return 0, fmt.Errorf("vservice %q is already registered as field_id %d", name, e.FieldID)
		}
		id = r.nextIDIn(lo, hi)
		if id == 0 {
			return 0, fmt.Errorf("registry ID overflow: all %s ids %d-%d are taken", r.Scope, lo, hi)
		}
		e := &VServiceEntry{Name: name, FieldID: id, Contract: contract, Visibility: visibility}
		r.VServices = append(r.VServices, e)
		r.vsByName[name] = e
	default:
		return 0, fmt.Errorf("register: kind must be service or vservice, got %q", kind)
	}
	r.dirty = true
	return id, nil
}

// nextIDIn returns max(ids within [lo,hi])+1, starting at lo, or 0 when the
// range is exhausted. Ids outside the range (legacy ones, or the other scope's)
// are ignored: they are frozen or someone else's.
func (r *Registry) nextIDIn(lo, hi int32) int32 {
	max := lo - 1
	for _, e := range r.Services {
		if e.ID >= lo && e.ID <= hi && e.ID > max {
			max = e.ID
		}
	}
	for _, e := range r.VServices {
		if e.FieldID >= lo && e.FieldID <= hi && e.FieldID > max {
			max = e.FieldID
		}
	}
	if max+1 > hi {
		return 0
	}
	return max + 1
}

// Union combines several authorities into one read-only view for a lock or a
// fleet build. A name or an id that appears in more than one registry is an
// error, never a precedence call: ids are unique across every registry a
// fleet loads, and which implementation runs is the fleet manifest's choice,
// not a registry's. labels name the registries for the error text.
func Union(regs []*Registry, labels []string) (*Registry, error) {
	u := &Registry{byName: map[string]*ServiceEntry{}, vsByName: map[string]*VServiceEntry{}}
	owner := map[int32]string{}
	nameOwner := map[string]string{}
	label := func(i int) string {
		if i < len(labels) {
			return labels[i]
		}
		return fmt.Sprintf("registry #%d", i+1)
	}
	var errs []string
	for i, r := range regs {
		for _, e := range r.Services {
			if prev, dup := nameOwner[e.Name]; dup {
				errs = append(errs, fmt.Sprintf("name %q is in both %s and %s", e.Name, prev, label(i)))
				continue
			}
			if prev, dup := owner[e.ID]; dup {
				errs = append(errs, fmt.Sprintf("id %d is %s and service %q in %s", e.ID, prev, e.Name, label(i)))
				continue
			}
			nameOwner[e.Name] = label(i)
			owner[e.ID] = fmt.Sprintf("service %q (%s)", e.Name, label(i))
			c := *e
			u.Services = append(u.Services, &c)
			u.byName[e.Name] = &c
		}
		for _, e := range r.VServices {
			if prev, dup := nameOwner[e.Name]; dup {
				errs = append(errs, fmt.Sprintf("name %q is in both %s and %s", e.Name, prev, label(i)))
				continue
			}
			if prev, dup := owner[e.FieldID]; dup {
				errs = append(errs, fmt.Sprintf("id %d is %s and vservice %q in %s", e.FieldID, prev, e.Name, label(i)))
				continue
			}
			nameOwner[e.Name] = label(i)
			owner[e.FieldID] = fmt.Sprintf("vservice %q (%s)", e.Name, label(i))
			c := *e
			u.VServices = append(u.VServices, &c)
			u.vsByName[e.Name] = &c
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("registries collide (a name or id may be in only one of them):\n  %s", strings.Join(errs, "\n  "))
	}
	return u, nil
}

// SyncFromGlobal makes this lock agree with its authorities (passed as their
// Union) for the declared names. A declared name missing from the union is an
// error that lists every such name and the registration step. A lock entry
// whose id differs is an error too: a lock never renumbers, since the id is
// already baked into tokens and kernel state.
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
			if l.Contract != g.Contract || l.Visibility != g.Visibility || l.TenantSliceField != g.TenantSliceField || l.ProjectSliceField != g.ProjectSliceField {
				l.Contract, l.Visibility = g.Contract, g.Visibility
				l.TenantSliceField, l.ProjectSliceField = g.TenantSliceField, g.ProjectSliceField
				r.dirty = true
			}
			continue
		}
		e := &ServiceEntry{Name: name, ID: g.ID, Contract: g.Contract, Visibility: g.Visibility, TenantSliceField: g.TenantSliceField, ProjectSliceField: g.ProjectSliceField}
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
			if l.Contract != g.Contract || l.Visibility != g.Visibility {
				l.Contract, l.Visibility = g.Contract, g.Visibility
				r.dirty = true
			}
			continue
		}
		e := &VServiceEntry{Name: name, FieldID: g.FieldID, Contract: g.Contract, Visibility: g.Visibility}
		r.VServices = append(r.VServices, e)
		r.vsByName[name] = e
		r.dirty = true
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("registry lock disagrees with its authorities %s (a lock never renumbers; fix the one that is wrong):\n  %s", strings.Join(r.Registries, ", "), strings.Join(conflicts, "\n  "))
	}
	if len(unregistered) > 0 {
		sort.Strings(unregistered)
		return fmt.Errorf("not in any registry this lock names (%s):\n  %s\n\nIn a checkout of the right authority run\n  tacplus registry register NAME --kind service|vservice --contract <this proto repo's URL> --visibility public|private\nand merge it, then re-run tacplus registry here", strings.Join(r.Registries, ", "), strings.Join(unregistered, "\n  "))
	}
	return nil
}

func (r *Registry) notInLock(kind, name string) error {
	return fmt.Errorf("%s %q is not in the registry lock (authorities %s); run `tacplus registry --proto-dir %s` to sync it, registering it first if needed", kind, name, strings.Join(r.Registries, ", "), r.inputDir)
}

// ContractOf returns the contract home registered for name (a service or a
// vservice) and whether the name is registered at all.
func (r *Registry) ContractOf(name string) (string, bool) {
	if e, ok := r.byName[name]; ok {
		return e.Contract, true
	}
	if e, ok := r.vsByName[name]; ok {
		return e.Contract, true
	}
	return "", false
}

// LoadAuthorities resolves each registry reference (a git URL or local path)
// with resolve, loads it, and returns their Union plus a cleanup for the
// resolved checkouts. It is what a lock sync and a dependency resolution both
// do with a lock's `registries:` list.
func LoadAuthorities(refs []string, resolve func(ref string) (dir string, cleanup func(), err error)) (*Registry, func(), error) {
	var regs []*Registry
	var cleanups []func()
	cleanup := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, ref := range refs {
		dir, c, err := resolve(ref)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("resolving registry %s: %w", ref, err)
		}
		if c != nil {
			cleanups = append(cleanups, c)
		}
		r, err := Load(dir)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("loading registry %s: %w", ref, err)
		}
		if r.IsLock() {
			cleanup()
			return nil, nil, fmt.Errorf("registry %s is itself a lock (registries:), not an authority", ref)
		}
		regs = append(regs, r)
	}
	u, err := Union(regs, refs)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return u, cleanup, nil
}
