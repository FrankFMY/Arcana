package arcana

import (
	"fmt"
	"reflect"
)

// DiffFields computes a JSON Patch (RFC 6902) between two field maps
// of the same row. Only top-level keys are compared.
func DiffFields(old, new map[string]any) []PatchOp {
	var ops []PatchOp

	// Check for removed and changed fields
	for k, oldVal := range old {
		newVal, exists := new[k]
		if !exists {
			ops = append(ops, PatchOp{Op: "remove", Path: "/" + k})
			continue
		}
		if !reflect.DeepEqual(oldVal, newVal) {
			ops = append(ops, PatchOp{Op: "replace", Path: "/" + k, Value: newVal})
		}
	}

	// Check for added fields
	for k, newVal := range new {
		if _, exists := old[k]; !exists {
			ops = append(ops, PatchOp{Op: "add", Path: "/" + k, Value: newVal})
		}
	}

	return ops
}

// DiffRefs computes a JSON Patch between two ref lists.
// Matching is done by table:id, not by position.
func DiffRefs(old, new []Ref) []PatchOp {
	var ops []PatchOp

	type indexedRef struct {
		ref   Ref
		index int
	}

	oldByKey := make(map[string]indexedRef, len(old))
	for i, r := range old {
		key := refKey(r)
		oldByKey[key] = indexedRef{ref: r, index: i}
	}

	newByKey := make(map[string]indexedRef, len(new))
	for i, r := range new {
		key := refKey(r)
		newByKey[key] = indexedRef{ref: r, index: i}
	}

	// Removed refs (in old, not in new) — soft-delete via replace with null
	for key, ir := range oldByKey {
		if _, exists := newByKey[key]; !exists {
			ops = append(ops, PatchOp{
				Op:    "replace",
				Path:  fmt.Sprintf("/%d", ir.index),
				Value: nil,
			})
		}
	}

	// Added and changed refs
	for key, ir := range newByKey {
		oldIR, existed := oldByKey[key]
		if !existed {
			ops = append(ops, PatchOp{
				Op:    "add",
				Path:  fmt.Sprintf("/%d", ir.index),
				Value: ir.ref,
			})
			continue
		}

		// Check if the ref itself changed (fields or nested)
		if !refsEqual(oldIR.ref, ir.ref) {
			ops = append(ops, PatchOp{
				Op:    "replace",
				Path:  fmt.Sprintf("/%d", ir.index),
				Value: ir.ref,
			})
		}
	}

	return ops
}

func refKey(r Ref) string {
	return r.Table + ":" + r.ID
}

func refsEqual(a, b Ref) bool {
	if a.Table != b.Table || a.ID != b.ID {
		return false
	}
	if !reflect.DeepEqual(a.Fields, b.Fields) {
		return false
	}
	if len(a.Nested) != len(b.Nested) {
		return false
	}
	for k, av := range a.Nested {
		bv, ok := b.Nested[k]
		if !ok {
			return false
		}
		if !refsEqual(av, bv) {
			return false
		}
	}
	return true
}
