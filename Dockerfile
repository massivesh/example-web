FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
# A build variable: set APP_VERSION as a *build* var in minicoolify, then deploy.
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/web .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/web /web
EXPOSE 8000
ENTRYPOINT ["/web"]
