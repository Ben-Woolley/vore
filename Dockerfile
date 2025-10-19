FROM public.ecr.aws/docker/library/golang:alpine3.22 AS build
WORKDIR /app
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
COPY . .
RUN go build -mod=vendor -trimpath -ldflags="-s -w" .

FROM public.ecr.aws/docker/library/alpine:3.22
WORKDIR /app
COPY --from=build /app/vore /app/vore
EXPOSE 5544
RUN mkdir -p /app/data

ENTRYPOINT ["/app/vore"]
