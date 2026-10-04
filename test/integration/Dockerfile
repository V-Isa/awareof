# Native parity uses the official Node image for node/npm and exact JavaScript tool packages.
FROM node:26.8.1-bookworm AS node-toolchain

RUN npm install --prefix /opt/typescript6 --ignore-scripts --no-audit --no-fund --omit=dev typescript@6.0.3 \
    && npm install --prefix /opt/typescript7 --ignore-scripts --no-audit --no-fund --omit=dev typescript@7.0.2 \
    && npm install --prefix /opt/prettier300 --ignore-scripts --no-audit --no-fund --omit=dev prettier@3.0.0 \
    && npm install --prefix /opt/prettier325 --ignore-scripts --no-audit --no-fund --omit=dev prettier@3.2.5 \
    && npm install --prefix /opt/prettier330 --ignore-scripts --no-audit --no-fund --omit=dev prettier@3.3.0 \
    && npm install --prefix /opt/prettier362 --ignore-scripts --no-audit --no-fund --omit=dev prettier@3.6.2 \
    && npm install --prefix /opt/prettier385 --ignore-scripts --no-audit --no-fund --omit=dev prettier@3.8.5 \
    && npm install --prefix /opt/prettier399 --ignore-scripts --no-audit --no-fund --omit=dev prettier@3.9.9

# Exercise the minimum supported Go line at its latest security patch.
FROM golang:1.26.7-bookworm

COPY --from=node-toolchain /usr/local/bin/node /usr/local/bin/node
COPY --from=node-toolchain /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
COPY --from=node-toolchain /opt/typescript6 /opt/typescript6
COPY --from=node-toolchain /opt/typescript7 /opt/typescript7
COPY --from=node-toolchain /opt/prettier300 /opt/prettier300
COPY --from=node-toolchain /opt/prettier325 /opt/prettier325
COPY --from=node-toolchain /opt/prettier330 /opt/prettier330
COPY --from=node-toolchain /opt/prettier362 /opt/prettier362
COPY --from=node-toolchain /opt/prettier385 /opt/prettier385
COPY --from=node-toolchain /opt/prettier399 /opt/prettier399
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /opt/typescript6/node_modules/typescript/bin/tsc /usr/local/bin/tsc

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV AWAREOF_INTEGRATION_CONTAINER=1 \
    GOCACHE=/tmp/go-build \
    GOTOOLCHAIN=local \
    HOME=/tmp/home

CMD ["go", "test", "-count=1", "-tags=integration", "./..."]
