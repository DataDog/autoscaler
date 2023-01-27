# VPA Recommender

- [Intro](#intro)
- [Running](#running)
- [Implementation](#implementation)

## Intro

Recommender-External is a different implementation of the Vertical Pod Autoscaler
Recommender (see its [../recommender/README.md](README.md).

It computes the recommended resource requests for pods based on external metrics,
those external metrics directly give the recommender a raw recommendation which it
might post-process according to VPA settings.

The current recommendations are put in status of the VPA resource, where they
can be inspected.

## Running

* In order to have historical data pulled in by the recommender, install
  Prometheus in your cluster and pass its address through a flag.
* Create RBAC configuration from `../deploy/vpa-rbac.yaml`.
* Create a deployment with the recommender pod from
  `../deploy/recommender-external-deployment.yaml`.
* The recommender-external will start running and pushing its recommendations to VPA
  object statuses.

## Implementation

The recommender is based on a model of the cluster that it builds in its memory.
The model contains Kubernetes resources: *Pods*, *VerticalPodAutoscalers*, with
their configuration (e.g. labels). It also keeps in memory the last know recommendations
fetched from external metrics.

It then runs in a loop and at each step performs the following actions:

* update model with recent information on resources (using listers based on
  watch),
* update model with fresh recommendations from external metrics.
* compute new recommendation for each VPA,
* put any changed recommendations into the VPA resources.
