# KEP-XXXX: Patch Well Known Controllers instead of Pods

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
- [Design Details](#design-details)
    - [Test Plan](#test-plan)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
    - [Update the eviction API](#update-the-eviction-api)
<!-- /toc -->

## Summary

The default behaviour of VPA Admission controller is to patch Pods when `UpdateModde`
is different from `Off`. This works fine if we accept vertical scaling outside standard deployments.

For a wide range of application we cannot guarantee that such changes will not
result in degraded performances, or worse, break the application. Because of that
users might want to do that only as part of their regular deployment procedure. This
makes even more sense when those deployments are done to multiple kubernetes clusters
with safeguards between them.

## Motivation

The motivation behind the change is to give VPA users a way to apply resource
changes only when they deploy their application.

### Goals

- Main: allow users to apply resource recommendation on demand (mostly during application deployment)

### Non-Goals

- Get rid of other update modes

## Proposal

The proposal is to extend the existing admission controller.

A new `vpaTrigger` annotation on the controller (deployment/sts) and the associated `UpdateMode` `Trigger`
in the VPA object will be created. When  an admitted resource is handled by the admission controller,
recommendations will be applied only  if the `vpaTrigger` annotation is present and equal to `true`. 

The admission controller will now also watch and mutate creation and updates of
`Deployment` and `StatefulSet`. It will do so by changing the `PodTemplateSpec` in the
same way it updates the `PodSpec` of a `Pod`.

## Design Details

- If `UpdateMode` is set to `Trigger` and the `vpaTrigger` annotation is equal to `true`:
the recommendations are applied and the `vpaTrigger` annotation is set to `triggered`.
- Otherwise, nothing happens.

The intent is to let users trigger a recommendation using:
`kubectl annotate deployment/hamster-deployment vpaTrigger=true`

The will also be able to simply set `vpaTrigger=true` in their charts to
trigger a new recommendation at each `apply` of the chart.

To make the code easier to rebase, the new resource handler will re-use most
of the pod resource handlers. If this ever get merged part of the code will be
re-worked to deal with `PodSpec` instead of `Pod`

### Test Plan

Add unit tests that cover the new code paths.

## Implementation History

- 2022-11-28: initial version
