/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package composite

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// AggregateCompositionConditions computes the composite resource's Synced and
// Ready conditions from the resources the composition process observed this
// reconcile.
//
// Unlike the previous implementation, which only looked at the last
// reconciliation's list of composed resources, aggregation understands why a
// resource isn't ready. Dependencies that are still being created produce a
// WaitingForDependencies condition. Apply failures produce a
// ResourceCreationFailed condition. Resources rejected by policy produce a
// PolicyValidationFailed condition. When everything is synced and ready the
// Ready condition uses SyncComplete.
func AggregateCompositionConditions(res CompositionResult) (xpv2.Condition, xpv2.Condition) {
	var (
		unsynced      []string
		unready       []string
		waiting       []ComposedResource
		creationFails []ComposedResource
		policyFails   []ComposedResource
		all           []string
	)

	for i, cd := range res.Composed {
		id := string(cd.ResourceName)
		if id == "" {
			// Specifying a name for P&T templates is optional but encouraged.
			// If there was no name, fall back to using the index. This mirrors
			// the behaviour of the reconciler before aggregation was added.
			id = fmt.Sprintf("%d", i)
		}
		all = append(all, id)

		if !cd.Synced {
			unsynced = append(unsynced, id)
		}
		if !cd.Ready {
			unready = append(unready, id)
		}

		switch cd.FailureReason {
		case xpv2.ReasonWaitingForDependencies:
			waiting = append(waiting, cd)
		case xpv2.ReasonPolicyValidationFailed:
			policyFails = append(policyFails, cd)
		case xpv2.ReasonResourceCreationFailed:
			creationFails = append(creationFails, cd)
		default:
			if !cd.Synced {
				// Legacy composers don't record a failure reason. A resource
				// that didn't sync is still a creation/update failure, not a
				// resource that's merely waiting to become ready.
				creationFails = append(creationFails, cd)
			}
		}
	}

	synced := xpv2.ReconcileSuccess()
	if len(unsynced) > 0 {
		synced = xpv2.ReconcileError(errors.New(errSyncResources)).
			WithMessage(fmt.Sprintf("Unsynced resources: %s", resource.StableNAndSomeMore(resource.DefaultFirstN, unsynced)))
	}

	ready := readyCondition(res, all, unready, waiting, creationFails, policyFails)
	return ready, synced
}

func readyCondition(res CompositionResult, all, unready []string, waiting, creationFails, policyFails []ComposedResource) xpv2.Condition {
	// A function may explicitly decide whether the XR is ready. An explicit
	// "not ready" verdict always wins. We still compute failures below before
	// honouring an explicit "ready" verdict: a composed resource that failed to
	// create must never result in a ready parent, even if a function says so.
	if res.Ready != nil && !*res.Ready {
		return xpv2.Creating()
	}

	// Creation failures are the most actionable state. A resource that could
	// not be created is more important to report than a downstream resource
	// that is merely waiting for it.
	if len(creationFails) > 0 {
		names := affectedNames(creationFails)
		return xpv2.ResourceCreationFailed(names...).
			WithMessage(fmt.Sprintf("Failed to create composed resources: %s", describeFailures(creationFails)))
	}

	// Policy failures usually mean a resource will never converge without a
	// change to the Composition, policy, or credentials. Surface them ahead of
	// ordinary "not ready yet" states.
	if len(policyFails) > 0 {
		names := affectedNames(policyFails)
		return xpv2.PolicyValidationFailed(names...).
			WithMessage(fmt.Sprintf("Composed resources rejected by policy: %s", describeFailures(policyFails)))
	}

	// Waiting resources include the resource itself and the dependencies it is
	// waiting for. This makes it possible to tell "database isn't ready yet"
	// apart from "database failed and the API is waiting on it".
	if len(waiting) > 0 {
		names := affectedNames(waiting)
		return xpv2.WaitingForDependencies(names...).
			WithMessage(fmt.Sprintf("Waiting for dependencies before creating composed resources: %s", describeWaiting(waiting)))
	}

	if res.Ready != nil && *res.Ready {
		if len(all) == 0 {
			return xpv2.Available()
		}
		return xpv2.SyncComplete(all...).WithMessage("All composed resources are ready")
	}

	switch {
	case len(unready) > 0:
		return xpv2.Creating().WithMessage(fmt.Sprintf("Unready resources: %s", resource.StableNAndSomeMore(resource.DefaultFirstN, unready)))
	case len(all) == 0:
		return xpv2.Available()
	default:
		return xpv2.SyncComplete(all...).WithMessage(fmt.Sprintf("Synced %d composed resource(s)", len(all)))
	}
}

func affectedNames(resources []ComposedResource) []string {
	out := make([]string, 0, len(resources))
	for _, cd := range resources {
		out = append(out, cd.AffectedResources...)
		if cd.ResourceName != "" {
			out = append(out, string(cd.ResourceName))
		}
	}
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func describeFailures(resources []ComposedResource) string {
	parts := make([]string, 0, len(resources))
	for _, cd := range resources {
		name := string(cd.ResourceName)
		if name == "" {
			name = "unknown"
		}
		if cd.Message == "" {
			parts = append(parts, fmt.Sprintf("%q", name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%q: %s", name, cd.Message))
	}
	return strings.Join(parts, "; ")
}

func describeWaiting(resources []ComposedResource) string {
	parts := make([]string, 0, len(resources))
	for _, cd := range resources {
		name := string(cd.ResourceName)
		if name == "" {
			name = "unknown"
		}

		deps := make([]string, 0, len(cd.AffectedResources))
		for _, d := range cd.AffectedResources {
			if d != name {
				deps = append(deps, d)
			}
		}
		if len(deps) == 0 && len(cd.Dependencies) > 0 {
			for _, d := range cd.Dependencies {
				deps = append(deps, string(d))
			}
		}

		if len(deps) == 0 {
			parts = append(parts, fmt.Sprintf("%q", name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%q waiting for %s", name, strings.Join(deps, ", ")))
	}
	return strings.Join(parts, "; ")
}

// PlanComposedResourceApply returns the order in which desired resources may be
// applied, and the resources that must wait for dependencies. The plan is
// deterministic: dependencies always come before the resources that depend on
// them, and resources with no ordering relationship are sorted by name.
//
// A dependency is considered satisfied when an observed copy of it exists and
// is ready. A resource with no Ready condition is considered ready once it
// exists. This lets ConfigMaps, Secrets, and similar resources satisfy a
// dependency without requiring a readiness check they'll never have.
func PlanComposedResourceApply(desired, observed ComposedResourceStates) ([]ResourceName, map[ResourceName][]ResourceName, error) {
	deps := make(map[ResourceName][]ResourceName, len(desired))
	for name, cd := range desired {
		d := dependenciesOfState(cd)
		for _, dep := range d {
			if _, ok := desired[dep]; ok {
				continue
			}
			if _, ok := observed[dep]; ok {
				continue
			}
			return nil, nil, errors.Errorf("composed resource %q depends on unknown resource %q", name, dep)
		}
		deps[name] = d
	}

	order, err := topoSortResourceNames(desired, deps)
	if err != nil {
		return nil, nil, err
	}

	ready := make(map[ResourceName]bool, len(observed))
	for name, cd := range observed {
		ready[name] = observedReady(cd)
	}

	waiting := make(map[ResourceName][]ResourceName)
	for _, name := range order {
		var unmet []ResourceName
		for _, dep := range deps[name] {
			if !ready[dep] {
				unmet = append(unmet, dep)
			}
		}
		if len(unmet) > 0 {
			waiting[name] = unmet
		}
	}

	return order, waiting, nil
}

func dependenciesOfState(cd ComposedResourceState) []ResourceName {
	if len(cd.Dependencies) > 0 {
		return cd.Dependencies
	}
	return dependenciesOfResource(cd.Resource)
}

func dependenciesOfResource(r resource.Composed) []ResourceName {
	if r == nil {
		return nil
	}
	if cd, ok := r.(*composed.Unstructured); ok {
		return Dependencies(cd)
	}
	return uniqueResourceNames(strings.Split(r.GetAnnotations()[AnnotationKeyDependsOn], ","))
}

func observedReady(cd ComposedResourceState) bool {
	if cd.Ready {
		return true
	}
	if cd.Resource == nil {
		return false
	}

	conditioned, ok := cd.Resource.(interface {
		GetCondition(xpv2.ConditionType) xpv2.Condition
	})
	if !ok {
		return false
	}

	c := conditioned.GetCondition(xpv2.TypeReady)
	if c.Type == "" {
		// The resource exists but doesn't have a readiness concept. Treat it as
		// ready so it can satisfy dependencies.
		return true
	}
	return c.Status == corev1.ConditionTrue
}

func topoSortResourceNames(desired ComposedResourceStates, deps map[ResourceName][]ResourceName) ([]ResourceName, error) {
	names := make([]ResourceName, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[ResourceName]int, len(names))
	order := make([]ResourceName, 0, len(names))

	var visit func(ResourceName, []ResourceName) error
	visit = func(name ResourceName, path []ResourceName) error {
		switch state[name] {
		case visited:
			return nil
		case visiting:
			return errors.Errorf("dependency cycle between composed resources: %s", strings.Join(append(namesToStrings(path), string(name)), " -> "))
		}

		state[name] = visiting
		d := append([]ResourceName(nil), deps[name]...)
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		for _, dep := range d {
			if _, ok := desired[dep]; !ok {
				// Observed-only dependencies are external to this desired set.
				continue
			}
			if err := visit(dep, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = visited
		order = append(order, name)
		return nil
	}

	for _, name := range names {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func namesToStrings(names []ResourceName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
