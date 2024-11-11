FROM golang:1.22 AS builder
ARG VERSION
ENV VERSION=$VERSION
WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -v -o /usr/local/bin/app ./...
FROM almalinux
WORKDIR /app
COPY entrypoint.sh /usr/local/bin/entrypoint
RUN chmod +x /usr/local/bin/entrypoint
RUN yum update -y
COPY --from=builder /usr/local/bin/app .
CMD [ "/usr/local/bin/entrypoint" ]
