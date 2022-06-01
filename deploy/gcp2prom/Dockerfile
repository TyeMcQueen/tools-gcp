FROM golang:alpine

WORKDIR /app/build
COPY . .

RUN go install -mod vendor ./cmd/gcp2prom/gcp2prom.go


FROM google/cloud-sdk:alpine

WORKDIR /app/bin

COPY --from=0 /go/bin/gcp2prom .
COPY --from=0 /app/build/gcp2prom.yaml .

ENTRYPOINT [ "/app/bin/gcp2prom" ]
