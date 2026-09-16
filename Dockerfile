# syntax=docker/dockerfile:1

# Pinned to the toolchain go.mod and CI use, rather than whatever a
# platform's native Go runtime ships that day.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cinebook-api ./cmd/cinebook-api

# The binary is static, so the runtime image needs no libc, shell or package
# manager. Migrations and the browser UI are embedded in it.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/cinebook-api /cinebook-api
USER nonroot:nonroot
ENTRYPOINT ["/cinebook-api"]
