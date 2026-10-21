# syntax=docker/dockerfile:1

# ---- build ----------------------------------------------------------------
# Base images are pinned by digest so builds are reproducible. Update them
# deliberately (and re-run the tests), not by accident.
FROM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build

WORKDIR /src

# Dependencies first: this layer is reused until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
# A static binary (no C library), stripped of debug tables, with the build path
# trimmed so the image does not reveal where it was built.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.buildVersion=${VERSION} -X main.buildCommit=${COMMIT}" \
      -o /out/ledgerd ./cmd/ledgerd

# ---- run ------------------------------------------------------------------
# distroless/static: no shell, no package manager, no libc; only the binary,
# CA certificates and timezone data. Runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="ledgerd" \
      org.opencontainers.image.description="A double-entry accounting ledger: exact money, idempotent writes, overdraft protection, reconciliation" \
      org.opencontainers.image.source="https://github.com/CMDevRG/ledgerd" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

COPY --from=build /out/ledgerd /ledgerd

USER nonroot:nonroot
EXPOSE 8085

# The image has no curl, so the binary probes itself.
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=3 \
  CMD ["/ledgerd", "healthcheck"]

ENTRYPOINT ["/ledgerd"]
CMD ["serve"]
