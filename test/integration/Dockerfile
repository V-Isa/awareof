# Native parity uses the official Node image only as the source of node/npm.
FROM node:26.8.1-bookworm AS node-toolchain

# Exercise the minimum supported Go line at its latest security patch.
FROM golang:1.26.7-bookworm

COPY --from=node-toolchain /usr/local/bin/node /usr/local/bin/node
COPY --from=node-toolchain /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV AWAREOF_INTEGRATION_CONTAINER=1 \
    GOCACHE=/tmp/go-build \
    GOTOOLCHAIN=local \
    HOME=/tmp/home

CMD ["go", "test", "-count=1", "-tags=integration", "./..."]
