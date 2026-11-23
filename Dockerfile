FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X github.com/pandokos/pandokos/internal/buildinfo.Version=$VERSION -X github.com/pandokos/pandokos/internal/buildinfo.Commit=$COMMIT" \
    -o /out/pandokos ./cmd/pandokos

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /out/pandokos /pandokos
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/pandokos"]
CMD ["serve"]
