FROM node:22-alpine AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

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
COPY --from=ui /ui/dist /usr/share/fleetdeck/ui
ENV HOST=0.0.0.0 DATA_DIR=/data FLEETDECK_UI_DIR=/usr/share/fleetdeck/ui
VOLUME /data
EXPOSE 8080
USER fleetdeck
CMD ["fleetdeck"]
