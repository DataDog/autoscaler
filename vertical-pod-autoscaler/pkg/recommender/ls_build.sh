#!/usr/bin/env bash
if [[ $(git status --short | grep '.go$' | grep -v '/vendor/'|wc -l) -gt 0 ]] 
then
  echo "Error: Relevant files look dirty:"
  git status --short | grep '.go$' | grep -v '/vendor/'
  exit 0
fi
SHA=$(git rev-parse --short HEAD)
make build-in-docker-amd64 docker-build-amd64 REGISTRY=registry.ddbuild.io TAG=$SHA FULL_COMPONENT=perf/vpa-recommender
echo "SHA is ${SHA} Pushing."
docker push registry.ddbuild.io/perf/vpa-recommender-amd64:$SHA
#cat deployment.yaml| sed -e 's/TAGVERSION/'$(git rev-parse --short HEAD)'/g' | kubectl apply -f -
