# syntax=docker/dockerfile:1

# ---- 构建阶段：同时产出常驻服务与一次性验收程序 ----
FROM golang:1.25 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN go build -ldflags="-s -w" -o /out/server ./cmd/server \
 && go build -ldflags="-s -w" -o /out/acceptance ./cmd/acceptance

# ---- 运行阶段：最小镜像，无 shell，默认启动常驻 API ----
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
COPY --from=build /out/server /out/acceptance /app/
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
