FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fleetdeck .

FROM alpine:3.22
# git + ansible for playbooks from repositories; openssh-client is what ansible connects with
RUN apk add --no-cache git openssh-client ansible-core && adduser -D -u 1000 fleetdeck && mkdir -p /data && chown fleetdeck:fleetdeck /data
COPY --from=build /fleetdeck /usr/local/bin/fleetdeck
ENV HOST=0.0.0.0 DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER fleetdeck
CMD ["fleetdeck"]
