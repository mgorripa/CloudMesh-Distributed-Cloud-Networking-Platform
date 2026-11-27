FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/cloudmesh ./cmd/cloudmesh && \
    CGO_ENABLED=0 go build -trimpath -o /out/echo ./cmd/echo
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cloudmesh /cloudmesh
COPY --from=build /out/echo /echo
USER 65532:65532
ENTRYPOINT ["/cloudmesh"]
