# Modified for k9+ by the k9+ contributors; see MODIFICATIONS.md.
# -----------------------------------------------------------------------------
# The base image for building the k9plus binary
FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine3.24@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

WORKDIR /k9plus
COPY go.mod go.sum main.go Makefile ./
COPY LICENSE COPYING NOTICE MODIFICATIONS.md ./
COPY docs/licensing.md docs/licensing.md
COPY scripts/collect-licenses.py scripts/collect-licenses.py
COPY internal internal
COPY cmd cmd
RUN apk --no-cache add --update make python3 libx11-dev git gcc libc-dev curl \
  && python3 scripts/collect-licenses.py --target ${TARGETOS}/${TARGETARCH} \
  && make build

# -----------------------------------------------------------------------------
# Build the final Docker image for the target platform (not the build host).
# Pinning this stage to $BUILDPLATFORM would yield an amd64 runtime image (incl.
# kubectl) even for the arm64 manifest entry, so we let it default to
# $TARGETPLATFORM and use buildx's TARGETARCH to fetch the matching kubectl.
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG KUBECTL_VERSION="v1.35.3"
ARG TARGETARCH

LABEL org.opencontainers.image.title="k9+" \
      org.opencontainers.image.source="https://github.com/jaredpricedev/k9s" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /k9plus/execs/k9plus /bin/k9plus
COPY --from=build /k9plus/LICENSE /k9plus/COPYING /k9plus/NOTICE /k9plus/MODIFICATIONS.md /usr/share/doc/k9plus/
COPY --from=build /k9plus/docs/licensing.md /usr/share/doc/k9plus/docs/licensing.md
COPY --from=build /k9plus/THIRD_PARTY_LICENSES /usr/share/doc/k9plus/THIRD_PARTY_LICENSES
RUN apk --no-cache add --update ca-certificates \
  && apk --no-cache add --update -t deps curl vim \
  && curl -f -L https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl -o /usr/local/bin/kubectl \
  && chmod +x /usr/local/bin/kubectl \
  && apk del --purge deps

ENTRYPOINT [ "/bin/k9plus" ]
