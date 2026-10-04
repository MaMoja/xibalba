# Builds the Xibalba image: one static program, nothing else.
#
#   docker build -t xibalba .
#
# The image holds no shell and no other program. Its health check asks the
# running Xibalba itself (xibalba -healthcheck).

# The build runs on the machine's own kind of processor and produces the
# program for the kind the image is for, so images for a Raspberry Pi are
# built without emulation.
FROM --platform=$BUILDPLATFORM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
ARG TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} go build -trimpath \
    -ldflags "-s -w -X github.com/MaMoja/xibalba/internal/buildinfo.Version=${VERSION}" \
    -o /out/xibalba ./cmd/xibalba
# An empty directory for what Xibalba keeps (signing key, caches).
RUN mkdir -p /out/state

FROM scratch
# Certificates of the public authorities, for downloads over HTTPS (crawler
# address lists, country database).
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/xibalba /xibalba
# Xibalba's own directory, writable for its user. A volume mounted here
# takes over this ownership.
COPY --from=build --chown=65532:65532 /out/state /var/lib/xibalba
# An unprivileged user. It has no name inside the image; the number is what counts.
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/xibalba", "-config", "/etc/xibalba/xibalba.yaml", "-healthcheck"]
ENTRYPOINT ["/xibalba"]
CMD ["-config", "/etc/xibalba/xibalba.yaml"]
