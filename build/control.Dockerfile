# Build the control service binary. The build context is the repository root.
# Both digests are the multi-platform index, not one platform's manifest, so the
# build's target platform still selects each base.
# Dependabot moves both lines (`.github/dependabot.yml`, the `/build` entry), rewriting
# tag and digest together.
# go.mod's go directive is 1.27.4 (the gaon operator needs it), so the builder is that release.
FROM mirror.gcr.io/library/golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# .dockerignore at the repository root filters the source.
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o control ./cmd/control

FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
# The Makefile's docker target passes the commit checked out; nothing checks it against the tree.
ARG REVISION=unknown
LABEL org.opencontainers.image.title="garam-agent-operator-control" \
      org.opencontainers.image.source="https://github.com/garamsh/garam-agent-operator" \
      org.opencontainers.image.revision="${REVISION}"
WORKDIR /
COPY --from=builder /workspace/control .
USER 65532:65532

ENTRYPOINT ["/control"]
