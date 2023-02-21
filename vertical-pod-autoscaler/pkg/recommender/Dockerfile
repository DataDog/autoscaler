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

FROM golang:1.19 as build-env

ARG ARCH

RUN go install github.com/go-delve/delve/cmd/dlv@latest

#ADD . /dockerdev
#WORKDIR /dockerdev

#RUN go build -gcflags="all=-N -l" ./...
#RUN go build -gcflags="all=-N -l" -o /recommender

FROM golang:1.19

ARG ARCH
EXPOSE 8000 40000
WORKDIR /
COPY --from=build-env /go/bin/dlv /dlv
# Make this last as this is the only part that will change.
COPY recommender-$ARCH /

CMD ["/dlv", "--listen=:40000", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/recommender", "--use-external-metrics=true", "--external-metrics-cpu-metric=cpu", "--external-metrics-memory-metric=mem", "--v", "4" ]
