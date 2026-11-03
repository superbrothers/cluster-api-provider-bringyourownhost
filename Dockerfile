# Build the manager binary
# GO_VERSION is passed by "make docker-build" from the toolchain directive in go.mod.
ARG GO_VERSION
# The builder runs on the platform of the machine that builds the image and
# cross-compiles the manager for the target platform, so that images for other
# architectures build without emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
# RUN go env -w GOPROXY=https://goproxy.cn,direct
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY apis/ apis/
COPY controllers/ controllers/
COPY installer/ installer/
COPY common/ common/

# Build for the target platform, which BuildKit sets from --platform and
# defaults to the platform of the machine.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
