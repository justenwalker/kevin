FROM scratch
ARG TARGETARCH
COPY linux/${TARGETARCH}/kevin-relay /kevin-relay
ENTRYPOINT ["/kevin-relay"]
