ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=alpine:3.22

FROM ${GO_IMAGE} AS backend-builder
ARG VERSION=dev
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG ALL_PROXY
ARG NO_PROXY
ARG GOPROXY=https://proxy.golang.org,direct
ENV HTTP_PROXY=${HTTP_PROXY} HTTPS_PROXY=${HTTPS_PROXY} ALL_PROXY=${ALL_PROXY} NO_PROXY=${NO_PROXY}
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY dashboard/backend/cmd/baize ./dashboard/backend/cmd/baize
COPY dashboard/backend/internal/config ./dashboard/backend/internal/config
COPY dashboard/backend/internal/dashboard ./dashboard/backend/internal/dashboard
COPY shared/model ./shared/model
COPY shared/robotmodel ./shared/robotmodel
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" -o /out/baize ./dashboard/backend/cmd/baize

FROM ${RUNTIME_IMAGE}
RUN mkdir -p /data/control /data/history
COPY --from=backend-builder /out/baize /opt/baize/dashboard/bin/baize
COPY dashboard/frontend /opt/baize/dashboard/frontend
COPY dashboard/config.yml.example /opt/baize/dashboard/config.yml
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/opt/baize/dashboard/bin/baize", "--config", "/opt/baize/dashboard/config.yml"]
