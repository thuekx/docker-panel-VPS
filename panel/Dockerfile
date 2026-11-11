# syntax=docker/dockerfile:1
# Nostek Panel image. Base images pinned by digest; see ../packages/panel-01/README.md to update them.
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=dev
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/nostek-panel ./cmd/nostek-panel \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
LABEL org.opencontainers.image.title="Nostek Panel" \
      org.opencontainers.image.source="https://github.com/thuekx/docker-panel-VPS" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/nostek-panel /nostek-panel
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV NP_DB=/data/panel.db NP_LISTEN=:8080 NP_DISK=/data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/nostek-panel", "healthcheck"]
ENTRYPOINT ["/nostek-panel"]
CMD ["serve"]
