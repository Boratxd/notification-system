FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -o /out/mockprovider ./cmd/mockprovider

FROM alpine:3.20
RUN adduser -D app
USER app
COPY --from=build /out/ /usr/local/bin/