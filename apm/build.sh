#!/bin/bash
set -euxo pipefail

REPO="${GOPATH}/src/k8s.io/autoscaler"
IMAGE="autoscaler-dependency-builder:latest"

docker build --compress -t "${IMAGE}" -f apm/Dockerfile .
docker run --rm -v "${REPO}/cluster-autoscaler:/tmp/out" "${IMAGE}" sh -c "cp cluster-autoscaler /tmp/out/"
( cd "${REPO}/cluster-autoscaler" && make make-image )

VERSION="apm100"

for CLOUD_IMAGE in \
    727006795293.dkr.ecr.us-east-1.amazonaws.com/cluster-autoscaler:v1.14.2-"${VERSION}" \
    gcr.io/datadog-staging/cluster-autoscaler:v1.14.2-"${VERSION}" \
    gcr.io/datadog-prod/cluster-autoscaler:v1.14.2-"${VERSION}"
do
    docker tag staging-k8s.gcr.io/cluster-autoscaler:dev "${CLOUD_IMAGE}"
    docker push "${CLOUD_IMAGE}"
done
