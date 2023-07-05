ARG BASE_IMAGE

FROM --platform=$BUILDPLATFORM golang:1.19 AS builder
WORKDIR /go/src/k8s.io/autoscaler
COPY . .
ARG TARGETARCH
ENV GO111MODULE "on"
ENV GOARCH=${TARGETARCH}

WORKDIR /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender
RUN env CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=linux go build -o vpa-recommender -buildvcs=false --ldflags "-s"

WORKDIR /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external
RUN env CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=linux go build -o vpa-recommender-external-metrics -buildvcs=false --ldflags "-s"

WORKDIR /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller
RUN env CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=linux go build -o vpa-admission-controller -buildvcs=false --ldflags "-s"

WORKDIR /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/updater
RUN env CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=linux go build -o vpa-updater -buildvcs=false --ldflags "-s"

FROM $BASE_IMAGE

COPY --from=builder /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/vpa-recommender /usr/local/bin/vpa-recommender
COPY --from=builder /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/vpa-recommender-external-metrics /usr/local/bin/vpa-recommender-external-metrics
COPY --from=builder /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/vpa-admission-controller /usr/local/bin/vpa-admission-controller
COPY --from=builder /go/src/k8s.io/autoscaler/vertical-pod-autoscaler/pkg/updater/vpa-updater /usr/local/bin/vpa-updater
