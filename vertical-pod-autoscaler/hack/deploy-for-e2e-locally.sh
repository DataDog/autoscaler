#!/bin/bash

# Copyright 2018 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..

function print_help {
  echo "ERROR! Usage: deploy-for-e2e.sh [suite]*"
  echo "<suite> should be one of:"
  echo " - recommender"
  echo " - recommender-externalmetrics"
  echo "If component is not specified all above will be started."
}

if [ $# -eq 0 ]; then
  print_help
  exit 1
fi

if [ $# -gt 1 ]; then
  print_help
  exit 1
fi

SUITE=$1

case ${SUITE} in
  recommender|recommender-externalmetrics)
    COMPONENTS="${SUITE}"
    ;;
  *)
    print_help
    exit 1
    ;;
esac

# KIND registry
export REGISTRY=localhost:5001
export TAG=latest

kubectl apply -f ${SCRIPT_ROOT}/deploy/vpa-rbac.yaml
# Other-versioned CRDs are irrelevant as we're running a modern-ish cluster.
kubectl apply -f ${SCRIPT_ROOT}/deploy/vpa-v1-crd.yaml
kubectl apply -f ${SCRIPT_ROOT}/deploy/k8s-metrics-server.yaml

for i in ${COMPONENTS}; do
  if [ $i == admission-controller ] ; then
    (cd ${SCRIPT_ROOT}/pkg/${i} && bash ./gencerts.sh || true)
  elif [ $i == recommender-externalmetrics ] ; then
    i=recommender
  fi
  ALL_ARCHITECTURES=amd64 make --directory ${SCRIPT_ROOT}/pkg/${i} release REGISTRY=${REGISTRY} TAG=${TAG}
  kind load docker-image ${REGISTRY}/vpa-${i}-amd64:${TAG}
done


for i in ${COMPONENTS}; do
  if [ $i == recommender-externalmetrics ] ; then
     kubectl delete namespace monitoring --ignore-not-found=true
     kubectl create namespace monitoring
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-configmap.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-rbac.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-deployment.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-service.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-adapter-api-service.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-adapter-configmap.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-adapter-deployment.yaml

     kubectl apply -f ${SCRIPT_ROOT}/deploy/metrics-pump.yaml
  fi
  kubectl apply -f ${SCRIPT_ROOT}/deploy/${i}-deployment.yaml
done
