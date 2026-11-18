# Composite Resource Readiness Conditions

This document describes how Crossplane aggregates the state of a composite
resource's composed resources into its `Ready` and `Synced` conditions.

## Conditions

A composite resource reports two system conditions:

- `Synced` says whether Crossplane was able to reconcile the composite resource
  and apply its desired composed resources.
- `Ready` is the aggregate readiness of the composite resource. It is `True`
  only when every composed resource that should exist does exist, synced
  successfully, and passed its readiness checks.

Both conditions include `observedGeneration`. A condition is only current when
its `observedGeneration` matches `metadata.generation`. A result computed for an
older generation is never allowed to replace a condition that has already
observed a newer generation.

`Ready` conditions may include `affectedResources`. These are composition
resource names - the keys in a Composition Function's desired resources map -
not necessarily Kubernetes object names. Tooling should treat the list as a
hint that identifies the resources to investigate, not as a complete graph.

## Ready Reasons

| Reason | Status | Meaning |
| --- | --- | --- |
| `Available` | `True` | The composite resource has no composed resources to wait for. |
| `SyncComplete` | `True` | Every composed resource was applied and is ready. `affectedResources` lists them. |
| `Creating` | `False` | At least one composed resource exists but is not ready yet. This is the ordinary "waiting for a provider" state. |
| `WaitingForDependencies` | `False` | At least one composed resource was not applied because a declared dependency does not exist or is not ready. `affectedResources` names the waiting resource and its unmet dependencies. |
| `ResourceCreationFailed` | `False` | Crossplane could not create or update a composed resource. `affectedResources` names the failed resources and the condition message includes the underlying error. |
| `PolicyValidationFailed` | `False` | A composed resource was rejected by admission, authorization, or another policy. `affectedResources` names the rejected resources. |
| `Deleting` | `False` | The composite resource is being deleted and Crossplane is waiting for its composed resources. |

`ResourceCreationFailed` and `PolicyValidationFailed` take precedence over
`WaitingForDependencies`. A downstream resource that is waiting on a failed
resource must not make the parent look healthy. An explicit `Ready=True` verdict
from a Composition Function also does not override a creation or policy failure.

## Declaring Dependencies

Composition Functions normally decide for themselves when a resource is ready.
When a function wants Crossplane core to order creation, it declares
dependencies on the desired composed resource using the
`crossplane.io/depends-on` annotation:

```yaml
apiVersion: example.org/v1
kind: Database
metadata:
  name: app-database
  annotations:
    crossplane.io/depends-on: app-network
```

The value is a comma-separated list of composition resource names. The same
declaration may be made using `spec.crossplane.dependsOn` as a string array in
the desired resource returned by a function.

Crossplane applies resources in dependency order:

1. Resources with no unmet dependencies are applied.
2. A resource is applied only after every dependency exists and reports
   `Ready=True`. A dependency with no `Ready` condition is considered ready once
   it exists - this lets ConfigMaps, Secrets, and similar resources satisfy a
   dependency.
3. Resources that are not yet allowed to apply are reported as
   `WaitingForDependencies`; they are not created early.

Unknown dependency names and dependency cycles are rejected with a clear
`Synced=False` condition and a warning event. Crossplane does not panic or
silently apply a partial, unordered graph.

## Failure and Recovery

When a resource fails to create, the parent's `Ready` condition becomes `False`
with reason `ResourceCreationFailed` and `affectedResources` naming the failed
resource. Downstream resources remain in `WaitingForDependencies`.

On the next reconcile Crossplane retries the failed resource. Once it is fixed
and becomes ready, only resources whose dependencies were previously unmet are
newly created. Resources that were already created keep their names and
references, so they are never created a second time.

## Troubleshooting

Start with the composite resource's conditions:

```shell
kubectl get xr example -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason} {.message}{"\n"}{end}'
```

If `Ready` is `False`:

- `WaitingForDependencies`: inspect the resources named in `affectedResources`.
  They do not exist yet or do not report `Ready=True`.
- `ResourceCreationFailed`: read the condition message and the
  `ResourceCreationFailed` warning event. The event names the composed resource
  and includes the API server error.
- `PolicyValidationFailed`: check admission policies, RBAC, provider
  credentials, and the composed resource's `managementPolicies`.
- `Creating`: the resource exists but its provider has not reported readiness
  yet. Check the composed resource's own conditions.

Related events are emitted on the composite resource:

```shell
kubectl describe xr example
```

Events with reason `WaitingForDependencies`, `ResourceCreationFailed`, and
`PolicyValidationFailed` mirror the corresponding `Ready` condition reasons.

## Error Handling

The controller never silently drops these failures:

- A missing Composition or CompositionRevision produces `Synced=False` with
  `ReconcileError` and a warning event.
- A Composition Function error produces `Synced=False` with `ReconcileError` and
  a warning event naming the pipeline step.
- An unreadable composed resource produces `Synced=False` with `ReconcileError`
  and a warning event.
- Invalid dependency declarations produce `Synced=False` with an explanatory
  message and a warning event.

In every case the composite resource's existing conditions and resource
references are left intact so a later reconcile can recover.
