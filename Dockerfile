# Built by GoReleaser (dockers_v2): the binary for each platform is placed at
# $TARGETPLATFORM/mysqldance in the build context. The binary is static
# (CGO_ENABLED=0); distroless/static adds the CA certificates that TLS
# connections to MySQL (--ssl-mode verify-*) need.
FROM gcr.io/distroless/static:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mysqldance /usr/local/bin/mysqldance
# The MIT License requires the notice to travel with copies of the binary.
COPY LICENSE /licenses/mysqldance/LICENSE
ENTRYPOINT ["/usr/local/bin/mysqldance"]
