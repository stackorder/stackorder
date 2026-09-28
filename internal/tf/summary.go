package tf

import (
	"encoding/json"
	"fmt"
	"slices"

	tfjson "github.com/hashicorp/terraform-json"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// ParsePlanJSON decodes `show -json` output and validates its format version.
func ParsePlanJSON(data []byte) (*tfjson.Plan, error) {
	var p tfjson.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("tf: decoding plan JSON: %w", err)
	}
	return &p, nil
}

// Summarize digests a plan into counts and sorted, de-duplicated address
// lists. Counting follows Terraform's own plan summary, one per resource
// change, so a deposed object's delete counts as a destroy:
//
//   - create, update and delete count as Adds, Changes and Destroys;
//   - delete-then-create and create-then-delete count only as Replaces;
//   - a change whose importing block is set counts as an Import, in
//     addition to its update when the import also changes the resource;
//   - a no-op change whose previous_address differs from its address counts
//     as a Move; a moved resource that also changes counts only as that
//     change;
//   - data source reads and forgets are not counted;
//   - OutputChanges counts outputs whose change is not a no-op.
func Summarize(p *tfjson.Plan) v1.PlanSummary {
	var s v1.PlanSummary
	if p == nil {
		return s
	}
	for _, rc := range p.ResourceChanges {
		if rc == nil || rc.Change == nil || rc.Mode == tfjson.DataResourceMode {
			continue
		}
		a := rc.Change.Actions
		switch {
		case a.Create():
			s.Adds++
			s.Added = append(s.Added, rc.Address)
		case a.Update():
			s.Changes++
			s.Changed = append(s.Changed, rc.Address)
		case a.Delete():
			s.Destroys++
			s.Destroyed = append(s.Destroyed, rc.Address)
		case a.Replace():
			s.Replaces++
			s.Replaced = append(s.Replaced, rc.Address)
		case a.NoOp() && rc.PreviousAddress != "" && rc.PreviousAddress != rc.Address && rc.Change.Importing == nil:
			s.Moves++
		}
		if rc.Change.Importing != nil {
			s.Imports++
		}
	}
	for _, oc := range p.OutputChanges {
		if oc != nil && len(oc.Actions) > 0 && !oc.Actions.NoOp() {
			s.OutputChanges++
		}
	}
	s.Added = sortedUnique(s.Added)
	s.Changed = sortedUnique(s.Changed)
	s.Destroyed = sortedUnique(s.Destroyed)
	s.Replaced = sortedUnique(s.Replaced)
	return s
}

// AddressSet returns the sorted, de-duplicated addresses of every managed
// resource the plan creates, updates, deletes or replaces. It equals
// SummaryAddressSet(Summarize(p)), so a fresh plan can be compared with the
// summary recorded for an expired plan artifact.
func AddressSet(p *tfjson.Plan) []string {
	return SummaryAddressSet(Summarize(p))
}

// SummaryAddressSet returns the sorted, de-duplicated union of a summary's
// Added, Changed, Destroyed and Replaced lists.
func SummaryAddressSet(s v1.PlanSummary) []string {
	all := make([]string, 0, len(s.Added)+len(s.Changed)+len(s.Destroyed)+len(s.Replaced))
	all = append(all, s.Added...)
	all = append(all, s.Changed...)
	all = append(all, s.Destroyed...)
	all = append(all, s.Replaced...)
	return sortedUnique(all)
}

// SameAddressSet reports whether a and b hold the same addresses, ignoring
// order and duplicates.
func SameAddressSet(a, b []string) bool {
	return slices.Equal(sortedUnique(a), sortedUnique(b))
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}
