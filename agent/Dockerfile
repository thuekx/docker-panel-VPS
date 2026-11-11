# syntax=docker/dockerfile:1
# Build image for nostek-agent. The binary is copied out and runs on the host (systemd); this image is never run
# as a service. Base image pinned by digest (same as the panel).
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN go vet ./... && go build -ldflags "-s -w -X main.Version=${VERSION}" -o /out/nostek-agent ./cmd/nostek-agent

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="nostek-agent (build output)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/thuekx/docker-panel-VPS" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/nostek-agent /nostek-agent
ENTRYPOINT ["/nostek-agent"]
CMD ["version"]
