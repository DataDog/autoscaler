#!/usr/bin/env bash
# Copyright 2022 The Kubernetes Authors.
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
