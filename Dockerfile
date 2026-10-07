# syntax=docker/dockerfile:1

# Built by GoReleaser, which puts the binaries it has compiled into the build
# context, one directory for each platform.

# Runs on the machine that builds, whatever the platform of the image.
FROM --platform=$BUILDPLATFORM alpine:3 AS files
# Directories the server writes to: scratch has no shell to make them in.
RUN mkdir -p /out/data /out/tmp && chmod 1777 /out/tmp

FROM scratch
ARG TARGETPLATFORM
COPY --from=files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY $TARGETPLATFORM/freshgo /freshgo
COPY --from=files --chown=65532:65532 /out/data /data
COPY --from=files /out/tmp /tmp
USER 65532:65532
ENV FRESHGO_DATABASE_URL=sqlite:///data/freshgo.sqlite \
	FRESHGO_LISTEN=0.0.0.0:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/freshgo"]
CMD ["serve"]
