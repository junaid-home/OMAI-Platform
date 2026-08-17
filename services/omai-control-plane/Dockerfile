FROM golang:1.26.6-bookworm AS build
ARG VERSION=dev
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN go install github.com/bufbuild/buf/cmd/buf@v1.72.0
COPY buf.yaml buf.gen.yaml ./
COPY api ./api
RUN buf lint && buf generate
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go test ./... && \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/omai ./cmd/omai-server && \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/omai-workspace-executor ./cmd/omai-workspace-executor && \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/omai-voice-gateway ./cmd/omai-voice-gateway
WORKDIR /src/integration/adk
RUN go mod download && \
    CGO_ENABLED=0 GOOS=linux go test ./... && \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/omai-adk-runtime ./cmd/omai-adk-runtime

FROM debian:bookworm-slim AS runtime
ENV HOME=/tmp
RUN apt-get update && \
    apt-get install -y --no-install-recommends bash ca-certificates curl git tini && \
    rm -rf /var/lib/apt/lists/* && \
    groupadd --gid 65532 omai && \
    useradd --uid 65532 --gid 65532 --no-create-home --home-dir /tmp omai && \
    install -d -o 65532 -g 65532 -m 0750 /var/lib/omai
COPY --from=build /out/omai /omai
COPY --from=build /out/omai-workspace-executor /omai-workspace-executor
COPY --from=build /out/omai-voice-gateway /omai-voice-gateway
COPY --from=build /out/omai-adk-runtime /omai-adk-runtime
COPY configs/models.example.json /etc/omai/models.json
COPY configs/models.dev.snapshot.json /etc/omai/models.dev.snapshot.json
COPY configs/model-sync.docker.json /etc/omai/model-sync.json
COPY configs/runtimes.docker.json /etc/omai/runtimes.json
COPY integration/adk/config/providers.example.json /etc/omai/providers.json
USER omai:omai
EXPOSE 8787 9091

FROM runtime AS server
ENTRYPOINT ["/usr/bin/tini", "--", "/omai"]

FROM runtime AS voice
EXPOSE 8791
ENTRYPOINT ["/usr/bin/tini", "--", "/omai-voice-gateway"]

# Preview execution needs the reviewed language toolchain in the sandbox, not
# in the public control plane. Versions are image inputs and should additionally
# be digest-pinned by the deployment lock used for a release.
FROM rust:1.90.0-bookworm AS rust-toolchain

FROM node:24.19.0-bookworm-slim AS executor-runtime
ENV PATH="/usr/local/go/bin:/usr/local/cargo/bin:${PATH}" \
    HOME=/tmp \
    GOTOOLCHAIN=local
COPY --from=build /usr/local/go /usr/local/go
COPY --from=rust-toolchain /usr/local/cargo /usr/local/cargo
COPY --from=rust-toolchain /usr/local/rustup /usr/local/rustup
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      bash ca-certificates composer curl default-jdk-headless git gradle maven \
      php-cli python3 python3-pip python3-venv tini && \
    rm -f /usr/local/bin/yarn* /usr/local/bin/corepack && \
    npm install --global bun@1.3.14 pnpm@10.28.1 yarn@1.22.22 && \
    rm -rf /root/.npm /var/lib/apt/lists/* && \
    groupadd --gid 65532 omai && \
    useradd --uid 65532 --gid 65532 --no-create-home --home-dir /tmp omai
COPY --from=build /out/omai-workspace-executor /omai-workspace-executor
USER omai:omai

FROM executor-runtime AS executor
EXPOSE 8792
ENTRYPOINT ["/usr/bin/tini", "--", "/omai-workspace-executor"]

FROM runtime AS adk
EXPOSE 8790
ENTRYPOINT ["/usr/bin/tini", "--", "/omai-adk-runtime"]
