FROM public.ecr.aws/docker/library/golang:alpine3.22 AS build
WORKDIR /app
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY . .
RUN go build -mod=vendor -trimpath -ldflags="-s -w" .

FROM public.ecr.aws/docker/library/alpine:3.22
WORKDIR /app
RUN addgroup -S appuser \
 && adduser -S -G appuser -H -s /sbin/nologin appuser
COPY --from=build --chown=appuser:appuser /app/vore /app/vore

EXPOSE 5544
RUN mkdir -p /app/data && chown appuser:appuser /app/data

USER appuser
ENTRYPOINT ["/app/vore"]
