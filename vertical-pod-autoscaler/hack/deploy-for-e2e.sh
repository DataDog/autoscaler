#!/bin/bash -x

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
  echo " - updater"
  echo " - admission-controller"
  echo " - actuation"
  echo " - full-vpa"
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
  recommender|recommender-externalmetrics|updater|admission-controller)
    COMPONENTS="${SUITE}"
    ;;
  full-vpa)
    COMPONENTS="recommender updater admission-controller"
    ;;
  actuation)
    COMPONENTS="updater admission-controller"
    ;;
  *)
    print_help
    exit 1
    ;;
esac

#export REGISTRY=gcr.io/`gcloud config get-value core/project`
# KIND registry
export REGISTRY=localhost:5001
export TAG=latest

echo "Configuring registry authentication"
mkdir -p "${HOME}/.docker"
gcloud auth configure-docker -q

for i in ${COMPONENTS}; do
  if [ $i == admission-controller ] ; then
    (cd ${SCRIPT_ROOT}/pkg/${i} && bash ./gencerts.sh || true)
  elif [ $i == recommender-externalmetrics ] ; then
    i=recommender
  fi
  ALL_ARCHITECTURES=amd64 make --directory ${SCRIPT_ROOT}/pkg/${i} release
done

kubectl create -f ${SCRIPT_ROOT}/deploy/vpa-v1-crd-gen.yaml
kubectl create -f ${SCRIPT_ROOT}/deploy/vpa-rbac.yaml

for i in ${COMPONENTS}; do
  ${SCRIPT_ROOT}/hack/vpa-process-yaml.sh  ${SCRIPT_ROOT}/deploy/${i}-deployment.yaml | kubectl create -f -
done

for i in ${COMPONENTS}; do
  if [ $i == recommender-externalmetrics ] ; then
     kubectl create namespace monitoring
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-configmap.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-rbac.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-deployment.yaml
     kubectl apply -f ${SCRIPT_ROOT}/deploy/prometheus-service.yaml
     # This is for prometheus-adapter (making prom an external-metrics-provider), which helm installs
     helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
     helm repo update
     helm install --set prometheus.url=http://prometheus.monitoring.svc prometheus-adapter prometheus-community/prometheus-adapter
  fi
done

