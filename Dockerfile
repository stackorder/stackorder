FROM node:24-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/ui/dist ./internal/ui/dist
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X github.com/stackorder/stackorder/internal/version.Version=${VERSION} -X github.com/stackorder/stackorder/internal/version.Commit=${COMMIT} -X github.com/stackorder/stackorder/internal/version.Date=${DATE}" \
    -o /out/stackorder-server ./cmd/stackorder-server

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/stackorder-server /stackorder-server
USER nonroot:nonroot
EXPOSE 8080
ENV STACKORDER_LISTEN=:8080
ENTRYPOINT ["/stackorder-server"]
