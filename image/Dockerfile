# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f image/Dockerfile --build-arg IMAGE_VER=v3 -t tenon-sandbox:v3 .

FROM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY go.mod go.work go.work.sum ./
COPY pkg/contract ./pkg/contract
COPY cmd/wsd ./cmd/wsd
COPY internal/wsd ./internal/wsd
COPY agent-runtime ./agent-runtime
RUN mkdir -p /out \
    && GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/wsd ./cmd/wsd \
    && GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agent-runtime ./agent-runtime/cmd/agent-runtime

FROM node:20-bookworm AS template-build
COPY --from=go-build /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" \
    GOTOOLCHAIN=local \
    GOMODCACHE=/opt/gomodcache \
    GOFLAGS=-modcacherw
WORKDIR /opt/templates/react-go-sqlite
COPY image/templates/react-go-sqlite/ ./
RUN cd web && npm ci --no-audit --no-fund && npm run build
RUN go mod download && CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -o /tmp/template-server ./cmd/server

FROM node:20-bookworm
ARG IMAGE_VER=dev
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl git procps python3 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 agent \
    && useradd --uid 10001 --gid 10001 --create-home --shell /bin/bash agent
COPY --from=go-build /usr/local/go /usr/local/go
COPY --from=go-build /out/wsd /usr/local/bin/wsd
COPY --from=go-build /out/agent-runtime /usr/local/bin/agent-runtime
COPY agent-runtime/prompt/system_prompt.md /opt/agent-runtime/system_prompt.md
COPY agent-runtime/skills/ /opt/agent-runtime/skills/
COPY image/skills/ /opt/agent-runtime/skills/
COPY image/system_prompt.append.md /opt/agent-runtime/system_prompt.append.md
COPY --from=template-build /opt/templates/react-go-sqlite/ /opt/templates/react-go-sqlite/
COPY --from=template-build /opt/gomodcache/ /opt/gomodcache/
ENV PATH="/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    HOME=/workspace/.home \
    LANG=C.UTF-8 \
    GOTOOLCHAIN=local \
    GOMODCACHE=/opt/gomodcache \
    GOFLAGS=-modcacherw \
    WSD_LISTEN=0.0.0.0:7000 \
    WORKSPACE_ROOT=/workspace \
    WSD_TEMPLATE_DIR=/opt/templates/react-go-sqlite \
    WSD_AGENT_RUNTIME_BIN=/usr/local/bin/agent-runtime \
    WSD_PREVIEW_PORT=3000 \
    WSD_IMAGE_VER=${IMAGE_VER}
RUN mkdir -p /workspace/app /workspace/appdata /workspace/.home /workspace/.agent-runtime \
    && chown -R 10001:10001 /workspace /opt/gomodcache \
    && chmod 0755 /usr/local/bin/wsd /usr/local/bin/agent-runtime
USER 10001:10001
WORKDIR /workspace
EXPOSE 7000 3000
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/wsd"]
