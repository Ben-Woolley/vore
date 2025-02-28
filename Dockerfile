FROM public.ecr.aws/docker/library/golang:1.24.0

COPY . .
RUN go build .
RUN chmod +x vore
EXPOSE 5544
ENTRYPOINT ["./vore"]
