FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/currency ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/currency ./currency
COPY config ./config
COPY migrations ./migrations

EXPOSE 8080
ENTRYPOINT ["/app/currency"]
