# Without git history in the image, serve_start logs serve's version as
# (devel).
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -o /neurokit ./cmd/neurokit

FROM python:3.12-slim
# A large library that a real tool imports, so the request times include a
# real handler's start-up cost.
RUN pip install --no-cache-dir google-genai==1.38.0
COPY --from=build /neurokit /usr/local/bin/neurokit
COPY python/neurokit_handler /opt/tool/neurokit_handler/
COPY test/container/manifest.json test/container/handler.py /opt/tool/
# python -m finds the shim and the handler in the working folder.
WORKDIR /opt/tool
ENTRYPOINT ["neurokit", "serve", "/opt/tool/manifest.json"]
