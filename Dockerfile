# Use official Golang image as base
FROM golang:1.22.3-alpine AS build

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum .

RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download -x

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod/ \
  go build -o minilib cmd/main.go

FROM alpine:latest
RUN apk add --no-cache catatonit

# Copy the Pre-built binary file from the previous stage
COPY --from=build /app/minilib /app/minilb

ENTRYPOINT ["catatonit", "--", "/app/minilb"]
