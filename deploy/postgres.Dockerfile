# PostgreSQL with ledgerd's role-creation script built in, so that the database
# image is self-contained: no bind mount of deploy/initdb is needed to use it.
# Published as ghcr.io/cmdevrg/ledgerd-postgres. Build with the repository root as context:
#
#   docker build -f deploy/postgres.Dockerfile -t ledgerd-postgres .
FROM postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea

ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="ledgerd-postgres" \
      org.opencontainers.image.description="PostgreSQL 16 with the ledgerd roles (ledgerd_owner, ledgerd_app) created on first start" \
      org.opencontainers.image.source="https://github.com/CMDevRG/ledgerd" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

# Needs LEDGERD_OWNER_PASSWORD and LEDGERD_APP_PASSWORD in the environment on first start.
COPY --chmod=0755 deploy/initdb/ /docker-entrypoint-initdb.d/
