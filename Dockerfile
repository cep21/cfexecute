FROM golang:1.11.1
RUN apt-get update
RUN apt-get install -y ca-certificates
WORKDIR /go/src/github.com/cep21/cfexecute
COPY *.go ./
COPY /vendor ./vendor
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o cfexecute .

FROM scratch
COPY --from=0 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
WORKDIR /
COPY --from=0 /go/src/github.com/cep21/cfexecute/cfexecute .
ENTRYPOINT ["/cfexecute"]
