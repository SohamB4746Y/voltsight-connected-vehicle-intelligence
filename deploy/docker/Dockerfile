# One image carries every VoltSight service binary and the web console; the Kubernetes command/args pick the
# service. Multi-stage, static binaries, non-root distroless runtime, no shell, read-only root filesystem friendly.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir /out && for c in vsapi vsgateway vsworker vssink vsalerts vsbatch vsseal vsprivacy vssim vsdb vspki vstopics vsincidents vsrecon; do \
      go build -ldflags="-s -w" -o /out/$c ./cmd/$c; done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
COPY --from=web /web/dist /app/web
COPY db/migrations /app/db/migrations
WORKDIR /app
USER 65532:65532
EXPOSE 8081 8443 9100 9102
ENTRYPOINT ["/app/vsapi"]
