# Multi-stage build of the gaon operator's binary, gaon-operator
# (ADR-0082). The build context is the repository root, and the root
# .dockerignore keeps it to the Go sources and the module manifests.
# The runtime stage is scratch, so the image has no shell.
FROM mirror.gcr.io/library/golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build

WORKDIR /src

# Dependencies resolve from the manifests alone, so this layer survives
# source-only edits; the download is tried three times because the
# module proxy and the private repositories can fail transiently.
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do \
      go mod download && exit 0; \
      [ "$attempt" = 3 ] && exit 1; \
      echo "go mod download: attempt $attempt failed; retrying" >&2; \
      sleep $((attempt * 10)); \
    done

# .dockerignore at the repository root filters the source.
COPY . .
RUN CGO_ENABLED=0 go build -o /out/gaon-operator ./cmd/gaon-operator

# The account the process runs as, named by number so a cluster can
# verify it is not root; scratch has no adduser, so only the lines
# naming it travel and the image carries no root account.
RUN adduser -D -u 10001 gaon-operator \
  && grep '^gaon-operator:' /etc/passwd > /out/passwd \
  && grep '^gaon-operator:' /etc/group > /out/group

FROM scratch

# ca-certificates: the roots a connection verifies against when its
# setting names none of its own.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group

COPY --from=build /out/gaon-operator /usr/local/bin/gaon-operator

# The commit and the version the build was given.
ARG IMAGE_REVISION
ARG IMAGE_VERSION
LABEL org.opencontainers.image.title="garam-agent-operator-gaon-operator" \
  org.opencontainers.image.source="https://github.com/garamsh/garam-agent-operator" \
  org.opencontainers.image.revision=$IMAGE_REVISION \
  org.opencontainers.image.version=$IMAGE_VERSION

USER 10001:10001
ENTRYPOINT ["gaon-operator"]
