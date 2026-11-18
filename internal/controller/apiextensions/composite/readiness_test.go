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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func TestAggregateCompositionConditions(t *testing.T) {
	cases := map[string]struct {
		res        CompositionResult
		wantReady  xpv2.Condition
		wantSynced xpv2.Condition
	}{
		"AllReady": {
			res: CompositionResult{Composed: []ComposedResource{
				{ResourceName: "database", Ready: true, Synced: true},
				{ResourceName: "bucket", Ready: true, Synced: true},
			}},
			wantReady:  xpv2.SyncComplete("database", "bucket"),
			wantSynced: xpv2.ReconcileSuccess(),
		},
		"ComposedResourceFailed": {
			res: CompositionResult{Composed: []ComposedResource{
				{ResourceName: "database", Ready: false, Synced: false, FailureReason: xpv2.ReasonResourceCreationFailed, Message: "invalid spec"},
				{ResourceName: "bucket", Ready: false, Synced: true, FailureReason: xpv2.ReasonWaitingForDependencies, AffectedResources: []string{"database"}},
			}},
			wantReady:  xpv2.ResourceCreationFailed("database"),
			wantSynced: xpv2.ReconcileError(errors.New(errSyncResources)).WithMessage("Unsynced resources: database"),
		},
		"PolicyValidationFailed": {
			res: CompositionResult{Composed: []ComposedResource{
				{ResourceName: "database", Ready: false, Synced: false, FailureReason: xpv2.ReasonPolicyValidationFailed, Message: "denied by policy"},
			}},
			wantReady:  xpv2.PolicyValidationFailed("database"),
			wantSynced: xpv2.ReconcileError(errors.New(errSyncResources)).WithMessage("Unsynced resources: database"),
		},
		"WaitingForDependencies": {
			res: CompositionResult{Composed: []ComposedResource{
				{ResourceName: "database", Ready: true, Synced: true},
				{ResourceName: "app", Ready: false, Synced: true, FailureReason: xpv2.ReasonWaitingForDependencies, AffectedResources: []string{"database"}},
			}},
			wantReady:  xpv2.WaitingForDependencies("database", "app"),
			wantSynced: xpv2.ReconcileSuccess(),
		},
		"GenericUnready": {
			res: CompositionResult{Composed: []ComposedResource{
				{ResourceName: "database", Ready: false, Synced: true},
			}},
			wantReady:  xpv2.Creating(),
			wantSynced: xpv2.ReconcileSuccess(),
		},
		"Empty": {
			res:        CompositionResult{},
			wantReady:  xpv2.Available(),
			wantSynced: xpv2.ReconcileSuccess(),
		},
		"ExplicitReady": {
			res: CompositionResult{
				Ready:    new(true),
				Composed: []ComposedResource{{ResourceName: "database", Ready: false, Synced: true}},
			},
			wantReady:  xpv2.SyncComplete("database"),
			wantSynced: xpv2.ReconcileSuccess(),
		},
		"ExplicitNotReady": {
			res: CompositionResult{
				Ready:    new(false),
				Composed: []ComposedResource{{ResourceName: "database", Ready: true, Synced: true}},
			},
			wantReady:  xpv2.Creating(),
			wantSynced: xpv2.ReconcileSuccess(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotReady, gotSynced := AggregateCompositionConditions(tc.res)

			tc.wantReady.LastTransitionTime = gotReady.LastTransitionTime
			tc.wantSynced.LastTransitionTime = gotSynced.LastTransitionTime
			// Messages are covered by TestAggregateCompositionConditionsMessages.
			tc.wantReady.Message = gotReady.Message
			tc.wantSynced.Message = gotSynced.Message

			if diff := cmp.Diff(tc.wantReady, gotReady); diff != "" {
				t.Errorf("Ready: -want, +got:\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantSynced, gotSynced); diff != "" {
				t.Errorf("Synced: -want, +got:\n%s", diff)
			}
		})
	}
}

func TestAggregateCompositionConditionsMessages(t *testing.T) {
	res := CompositionResult{Composed: []ComposedResource{
		{ResourceName: "database", Ready: false, Synced: false, FailureReason: xpv2.ReasonResourceCreationFailed, Message: "spec.replicas must be positive"},
	}}

	ready, _ := AggregateCompositionConditions(res)
	if !strings.Contains(ready.Message, "database") || !strings.Contains(ready.Message, "spec.replicas") {
		t.Errorf("Ready message should name the failed resource and error, got %q", ready.Message)
	}
	if diff := cmp.Diff([]string{"database"}, ready.AffectedResources); diff != "" {
		t.Errorf("Ready affected resources: -want, +got:\n%s", diff)
	}
}

func TestPlanComposedResourceApply(t *testing.T) {
	cases := map[string]struct {
		desired  ComposedResourceStates
		observed ComposedResourceStates
		want     []ResourceName
		waiting  map[ResourceName][]ResourceName
		wantErr  bool
	}{
		"NoDependencies": {
			desired: ComposedResourceStates{
				"b": desiredWithDependencies("b"),
				"a": desiredWithDependencies("a"),
			},
			want: []ResourceName{"a", "b"},
		},
		"Chain": {
			desired: ComposedResourceStates{
				"app":      desiredWithDependencies("app", "database"),
				"database": desiredWithDependencies("database", "cluster"),
				"cluster":  desiredWithDependencies("cluster"),
			},
			want: []ResourceName{"cluster", "database", "app"},
			waiting: map[ResourceName][]ResourceName{
				"database": {"cluster"},
				"app":      {"database"},
			},
		},
		"DependencyReady": {
			desired: ComposedResourceStates{
				"app":      desiredWithDependencies("app", "database"),
				"database": desiredWithDependencies("database", "cluster"),
				"cluster":  desiredWithDependencies("cluster"),
			},
			observed: ComposedResourceStates{
				"cluster": readyObserved("cluster"),
			},
			want: []ResourceName{"cluster", "database", "app"},
			waiting: map[ResourceName][]ResourceName{
				"app": {"database"},
			},
		},
		"UnknownDependency": {
			desired: ComposedResourceStates{
				"app": desiredWithDependencies("app", "missing"),
			},
			wantErr: true,
		},
		"Cycle": {
			desired: ComposedResourceStates{
				"a": desiredWithDependencies("a", "b"),
				"b": desiredWithDependencies("b", "a"),
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, waiting, err := PlanComposedResourceApply(tc.desired, tc.observed)
			if (err != nil) != tc.wantErr {
				t.Fatalf("PlanComposedResourceApply(...) error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("order: -want, +got:\n%s", diff)
			}
			if diff := cmp.Diff(tc.waiting, waiting, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("waiting: -want, +got:\n%s", diff)
			}
		})
	}
}

func TestConditionsForStatus(t *testing.T) {
	existing := []xpv2.Condition{
		{
			Type:               xpv2.TypeReady,
			Status:             corev1.ConditionTrue,
			Reason:             xpv2.ReasonAvailable,
			ObservedGeneration: 1,
		},
		{
			Type:               "DatabaseReady",
			Status:             corev1.ConditionFalse,
			Reason:             "Creating",
			ObservedGeneration: 1,
		},
	}
	produced := []TargetedCondition{
		{
			Condition: xpv2.Condition{
				Type:   "DatabaseReady",
				Status: corev1.ConditionTrue,
				Reason: "Available",
			},
			Target: CompositionTargetComposite,
		},
	}

	got := ConditionsForStatus(2, existing, produced)
	want := []xpv2.Condition{
		{
			Type:               "DatabaseReady",
			Status:             corev1.ConditionTrue,
			Reason:             "Available",
			ObservedGeneration: 2,
		},
	}

	// The stale system Ready condition is dropped. The function-produced
	// condition is retained and stamped with the current generation.
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ConditionsForStatus(...): -want, +got:\n%s", diff)
	}
}

func desiredWithDependencies(name string, deps ...string) ComposedResourceState {
	cd := composed.New()
	cd.SetName(name)
	if len(deps) > 0 {
		cd.SetAnnotations(map[string]string{AnnotationKeyDependsOn: strings.Join(deps, ",")})
	}
	return ComposedResourceState{Resource: cd}
}

func readyObserved(name string) ComposedResourceState {
	cd := composed.New()
	cd.SetName(name)
	cd.SetConditions(xpv2.Condition{
		Type:   xpv2.TypeReady,
		Status: corev1.ConditionTrue,
	})
	return ComposedResourceState{Resource: cd, Ready: true}
}
