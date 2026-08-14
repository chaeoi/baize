ARG GO_IMAGE=golang:1.24-alpine
ARG RUNTIME_IMAGE=alpine:3.22

FROM ${GO_IMAGE} AS backend-builder
ARG VERSION=dev
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG ALL_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} HTTPS_PROXY=${HTTPS_PROXY} ALL_PROXY=${ALL_PROXY} NO_PROXY=${NO_PROXY}
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY dashboard/backend/cmd/echobot ./dashboard/backend/cmd/echobot
COPY dashboard/backend/internal/config ./dashboard/backend/internal/config
COPY dashboard/backend/internal/dashboard ./dashboard/backend/internal/dashboard
COPY shared/model ./shared/model
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" -o /out/echobot ./dashboard/backend/cmd/echobot

FROM ${RUNTIME_IMAGE}
RUN mkdir -p /data /config
COPY --from=backend-builder /out/echobot /opt/echobot/dashboard/bin/echobot
COPY dashboard/frontend /opt/echobot/dashboard/frontend
COPY dashboard/config.yml /config/config.example.yml
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/opt/echobot/dashboard/bin/echobot"]
