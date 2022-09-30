# Copyright 2017 The Kubernetes Authors. All rights reserved
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

#FROM gcr.io/distroless/static:latest
#MAINTAINER Krzysztof Grygiel "kgrygiel@google.com"
FROM golang:1.16.6

# Add Delve
RUN go install github.com/go-delve/delve/cmd/dlv@latest

ARG ARCH
COPY recommender-$ARCH /recommender

EXPOSE 8000 40000

CMD ["/go/bin/dlv", "--listen=:40000", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/recommender", "--v=4", "--stderrthreshold=info", "--prometheus-address=http://prometheus.monitoring.svc", "--dd-cluster-name", "snowver", "--source", "datadog"]
#ENTRYPOINT ["/recommender"]

#CMD ["--v=4", "--stderrthreshold=info", "--prometheus-address=http://prometheus.monitoring.svc"]
