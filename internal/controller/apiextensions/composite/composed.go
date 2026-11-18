/*
Copyright 2022 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use
this file except in compliance with the License. You may obtain a copy of the
License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
*/

package composite

import (
	"strings"

	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/unstructured/composed"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

const (
	// AnnotationKeyDependsOn declares the composition resource names that a
	// composed resource depends on. It is a comma-separated list of names from
	// the Composition Function's desired resources map. Crossplane applies a
	// resource only after every declared dependency exists and is ready.
	// This is an extension point used by Composition Functions that need core
	// to order creation, rather than ordering resources inside the function.
	AnnotationKeyDependsOn = "crossplane.io/depends-on"
)

// A ResourceName uniquely identifies the composed resource within a Composition
// and within Composition Function gRPC calls. This is not the metadata.name of
// the actual composed resource instance; rather it is the name of an entry in a
// Composition's resources array, and/or a RunFunctionRequest's observed/desired
// resources object.
type ResourceName string

// A ComposedResource is an output of the composition process.
type ComposedResource struct {
	// ResourceName of the composed resource.
	ResourceName ResourceName

	// Ready indicates whether this composed resource is ready - i.e. whether
	// all of its readiness checks passed. Setting it to false will cause the
	// XR to be marked as not ready.
	Ready bool

	// Synced indicates whether the composition process was able to sync the
	// composed resource with its desired state. Setting it to false will cause
	// the XR to be marked as not synced.
	Synced bool

	// FailureReason explains why this composed resource is not ready. It is
	// empty when the resource is ready, or when it is simply not ready yet
	// (for example because its provider hasn't observed it yet).
	FailureReason xpv2.ConditionReason

	// Message is a human-readable explanation of FailureReason.
	Message string

	// AffectedResources names the resources involved in FailureReason. For a
	// failed resource this includes the failed resource. For a resource that
	// is waiting on dependencies it includes both the waiting resource and
	// the dependencies it is waiting for.
	AffectedResources []string

	// Dependencies lists the composition resource names this resource depends
	// on. It is populated even when the resource is ready, so callers can
	// explain the order resources were applied in.
	Dependencies []ResourceName
}

// ComposedResourceState represents a composed resource (either desired or
// observed).
type ComposedResourceState struct {
	Resource          resource.Composed
	ConnectionDetails managed.ConnectionDetails
	Ready             bool

	// Dependencies lists composition resource names this resource depends on.
	// When empty, dependencies are read from the composed resource's
	// crossplane.io/depends-on annotation or spec.crossplane.dependsOn field.
	Dependencies []ResourceName
}

// Dependencies returns the composition resource names the supplied composed
// resource declares it depends on. Dependencies may be declared using
// AnnotationKeyDependsOn, or using spec.crossplane.dependsOn in the desired
// composed resource. Unknown or malformed entries are ignored here; Compose
// validates them before applying anything.
func Dependencies(cd *composed.Unstructured) []ResourceName {
	if cd == nil {
		return nil
	}

	annotations := cd.GetAnnotations()
	for _, key := range []string{AnnotationKeyDependsOn, "crossplane.io/dependsOn", "crossplane.io/depends_on"} {
		if raw := annotations[key]; raw != "" {
			return uniqueResourceNames(strings.Split(raw, ","))
		}
	}

	// Also accept ordering metadata embedded in the resource itself. These
	// fields aren't part of any composed resource's typed API; they're an
	// opt-in convention for functions that want core to order creation.
	for _, path := range [][]string{
		{"spec", "crossplane", "dependsOn"},
		{"spec", "dependsOn"},
		{"dependsOn"},
	} {
		if values, found, err := kunstructured.NestedStringSlice(cd.Object, path...); err == nil && found {
			return uniqueResourceNames(values)
		}
	}

	return nil
}

func uniqueResourceNames(in []string) []ResourceName {
	out := make([]ResourceName, 0, len(in))
	seen := make(map[ResourceName]bool, len(in))
	for _, s := range in {
		name := ResourceName(strings.TrimSpace(s))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ComposedResourceStates tracks the state of composed resources.
type ComposedResourceStates map[ResourceName]ComposedResourceState
