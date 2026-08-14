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
COPY dashboard/backend/cmd/xuanjian ./dashboard/backend/cmd/xuanjian
COPY dashboard/backend/internal/config ./dashboard/backend/internal/config
COPY dashboard/backend/internal/dashboard ./dashboard/backend/internal/dashboard
COPY shared/model ./shared/model
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" -o /out/xuanjian ./dashboard/backend/cmd/xuanjian

FROM ${RUNTIME_IMAGE}
RUN mkdir -p /data /config
COPY --from=backend-builder /out/xuanjian /opt/xuanjian/dashboard/bin/xuanjian
COPY dashboard/frontend /opt/xuanjian/dashboard/frontend
COPY dashboard/config.yml /config/config.example.yml
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/opt/xuanjian/dashboard/bin/xuanjian"]
