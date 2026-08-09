# Multi-stage build: compile a static binary, ship it on distroless so the
# runtime image has no shell, package manager, or root user — matching the
# manifests' securityContext (runAsNonRoot, drop ALL, RuntimeDefault seccomp).
FROM docker.io/library/golang:1.24 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]
