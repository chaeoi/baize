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
COPY dashboard/backend/cmd/echobot-dashboard ./dashboard/backend/cmd/echobot-dashboard
COPY dashboard/backend/internal/dashboard ./dashboard/backend/internal/dashboard
COPY shared/model ./shared/model
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" -o /out/echobot-dashboard ./dashboard/backend/cmd/echobot-dashboard

FROM ${RUNTIME_IMAGE}
RUN mkdir -p /opt/echobot/dashboard/data
COPY --from=backend-builder /out/echobot-dashboard /opt/echobot/dashboard/bin/echobot-dashboard
COPY dashboard/frontend /opt/echobot/dashboard/frontend
USER root
ENV DASHBOARD_LISTEN=:8080
ENV DASHBOARD_DATA_DIR=/opt/echobot/dashboard/data
ENV DASHBOARD_FRONTEND_DIR=/opt/echobot/dashboard/frontend
EXPOSE 8080
VOLUME ["/opt/echobot/dashboard/data"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 CMD if [ -n "$DASHBOARD_TLS_CERT" ]; then wget -q --no-check-certificate -O /dev/null https://127.0.0.1:8080/healthz; else wget -q -O /dev/null http://127.0.0.1:8080/healthz; fi
ENTRYPOINT ["/opt/echobot/dashboard/bin/echobot-dashboard"]
