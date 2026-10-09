# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w -X ey/internal/config.BuildVersion=${VERSION}" \
    -o /out/ey ./cmd/ey

# distroless/static ships CA certificates (needed for https webhooks) and a
# non-root user, with no shell or package manager.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/ey /usr/local/bin/ey

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/ey"]
CMD ["-json"]
